package billing

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	gcontracts "goforum/internal/modules/gamification/contracts"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/config"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/seo"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
)

type Module struct {
	db       *gorm.DB
	svc      *Service
	renderer *view.Renderer
	settings *settings.Store
	auth     contracts.Authenticator
	sessions *session.Store
	cfg      *config.Config
	log      *slog.Logger
	version  string
	registry adminui.Registry
}

func New(db *gorm.DB, svc *Service) *Module { return &Module{db: db, svc: svc} }

func (m *Module) Configure(st *settings.Store, points gcontracts.Points, auditWriter audit.Writer, bus events.Bus, renderer *view.Renderer, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, version string) {
	m.svc.Configure(st, points, auditWriter, bus, log)
	m.settings = st
	m.renderer = renderer
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.version = version
}

func (m *Module) SetRegistry(r adminui.Registry) { m.registry = r }

func (m *Module) Name() string                    { return "billing" }
func (m *Module) DependsOn() []string             { return []string{"auth", "gamification"} }
func (m *Module) Models() []any                   { return Models() }
func (m *Module) Subscribe(events.Bus) error      { return nil }
func (m *Module) Settings() []settings.Definition { return nil }

func (m *Module) Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "admin.billing.view", Label: "查看商业化数据", Group: "商业化"},
		{Key: "admin.billing.manage", Label: "管理商品、订单与支付渠道", Group: "商业化"},
		{Key: "admin.billing.refund", Label: "订单退款", Group: "商业化"},
		{Key: "admin.billing.channel", Label: "管理支付渠道", Group: "商业化"},
	}
}

func (m *Module) Admin() adminui.Contribution {
	return adminui.Contribution{
		Nav: []adminui.NavItem{
			{Group: "billing", Label: "商业化", URL: "/admin/billing", Permission: "admin.billing.view", Order: 10},
		},
	}
}

func (m *Module) md() middleware.Deps {
	return middleware.Deps{
		Config: m.cfg, Sessions: m.sessions, Users: contracts.AsLoader(m.auth),
		Log: m.log, Renderer: m.renderer, Settings: m.settings, Version: m.version,
	}
}

func (m *Module) Routes(web, api *gin.RouterGroup) {
	if m.renderer == nil {
		return
	}
	md := m.md()
	login := middleware.RequireLogin(md)
	adminView := middleware.RequirePermission(md, "admin.billing.view")
	adminManage := middleware.RequirePermission(md, "admin.billing.manage")
	adminRefund := middleware.RequirePermission(md, "admin.billing.refund")
	adminChannel := middleware.RequirePermission(md, "admin.billing.channel")

	web.GET("/pricing", m.pricingPage)
	web.POST("/orders", login, middleware.RateLimit(md, "order_create", 20, 10*time.Minute), m.createOrder)
	web.GET("/checkout/:out_trade_no", login, m.checkoutPage)
	web.GET("/payments/mock/:out_trade_no", login, middleware.RateLimit(md, "mock_pay", 30, 10*time.Minute), m.mockPay)
	web.POST("/payments/nowpayments/notify", m.nowPaymentsNotify)
	web.POST("/payments/xcash/notify", m.xcashNotify)
	web.POST("/payments/creem/notify", m.creemNotify)
	web.POST("/payments/lemonsqueezy/notify", m.lemonSqueezyNotify)
	web.GET("/billing/orders", login, m.ordersPage)
	web.GET("/billing/subscription", login, m.subscriptionPage)

	web.GET("/admin/billing", login, adminView, m.adminPage)
	web.POST("/admin/billing/action", login, adminManage, m.adminAction)
	web.POST("/admin/billing/refund", login, adminRefund, m.adminRefund)
	web.POST("/admin/billing/channel", login, adminChannel, m.adminChannel)
	web.POST("/admin/billing/deipay-tokens", login, adminChannel, m.adminDeipayTokens)
}

type pricingData struct {
	Products []Product
	Channels []PaymentChannel
	Balance  int64
	Error    string
}

func (m *Module) pricingPage(c *gin.Context) {
	products, _ := m.svc.ListProducts(c.Request.Context(), true)
	channels, _ := m.svc.ListChannels(c.Request.Context(), true)
	bal := int64(0)
	if user := authn.FromContext(c.Request.Context()); user != nil {
		bal, _ = m.svc.UserBalance(c.Request.Context(), user.ID)
	}
	data := m.page(c, "充值 / 套餐")
	data.Description = "购买积分包、VIP 套餐与邀请包，支持站内余额与第三方支付渠道。"
	if data.Site.URL != "" && len(products) > 0 {
		base := strings.TrimRight(data.Site.URL, "/")
		docs := make([]template.JS, 0, len(products))
		for _, p := range products {
			docs = append(docs, seo.ProductOffer(p.Name, base+"/pricing", p.Description, p.PriceMinor, p.Currency))
		}
		data.JSONLD = seo.Graph(docs...)
	}
	data.Data = pricingData{Products: products, Channels: channels, Balance: bal, Error: c.Query("error")}
	m.renderer.Render(c, http.StatusOK, "pricing", data)
}

func (m *Module) page(c *gin.Context, title string) view.Data {
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.SetPageTitle(title, m.settings.GetString("seo.title_template"))
	return data
}

func (m *Module) createOrder(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	order, _, err := m.svc.CreateOrder(c.Request.Context(), user.ID,
		c.PostForm("product_id"), c.PostForm("channel_id"),
		c.PostForm("coupon_code"), c.ClientIP())
	if err != nil {
		c.Redirect(http.StatusFound, "/pricing?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/checkout/"+order.OutTradeNo)
}

type checkoutData struct {
	Order    *Order
	PayURL   string
	Channels []PaymentChannel
	Balance  int64
	Error    string
	Paid     bool
}

func (m *Module) checkoutPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	order, err := m.svc.GetOrderByNo(c.Request.Context(), c.Param("out_trade_no"))
	if err != nil || order.UserID != user.ID {
		data := m.page(c, "收银台")
		data.Data = view.ErrorData{Code: 404, Message: "订单不存在", RequestID: c.GetString("request_id")}
		m.renderer.Render(c, http.StatusNotFound, "error", data)
		return
	}
	channels, _ := m.svc.ListChannels(c.Request.Context(), true)
	bal, _ := m.svc.UserBalance(c.Request.Context(), user.ID)
	payURL := ""
	if order.Status == "pending" {
		payURL = order.PayURL
		if payURL == "" && (order.ChannelCode == "alipay" || order.ChannelCode == "wechat") {
			payURL = "/payments/mock/" + order.OutTradeNo + "?channel=" + order.ChannelCode
		}
	}
	data := m.page(c, "收银台")
	data.Robots = "noindex,nofollow"
	data.Data = checkoutData{
		Order: order, PayURL: payURL, Channels: channels, Balance: bal,
		Error: c.Query("error"), Paid: c.Query("paid") == "1",
	}
	m.renderer.Render(c, http.StatusOK, "checkout", data)
}

func (m *Module) mockPay(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	order, err := m.svc.GetOrderByNo(c.Request.Context(), c.Param("out_trade_no"))
	if err != nil || order.UserID != user.ID {
		c.Redirect(http.StatusFound, "/billing/orders?error="+url.QueryEscape("订单不存在"))
		return
	}
	if order.ChannelCode != "alipay" && order.ChannelCode != "wechat" {
		c.Redirect(http.StatusFound, "/checkout/"+order.OutTradeNo+"?error="+url.QueryEscape("该渠道不支持模拟支付"))
		return
	}
	if m.cfg.Env == "production" {
		c.Redirect(http.StatusFound, "/checkout/"+order.OutTradeNo+"?error="+url.QueryEscape("沙箱支付已禁用"))
		return
	}
	if err := m.svc.PayOrder(c.Request.Context(), order.OutTradeNo, order.ChannelCode,
		"mock-"+order.ID, order.AmountMinor); err != nil {
		c.Redirect(http.StatusFound, "/checkout/"+order.OutTradeNo+"?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/checkout/"+order.OutTradeNo+"?paid=1")
}

func (m *Module) nowPaymentsNotify(c *gin.Context) {
	status, body := m.svc.HandleNotify(c.Request.Context(), "nowpayments", c.Request)
	c.String(status, body)
}

func (m *Module) xcashNotify(c *gin.Context) {
	status, body := m.svc.HandleNotify(c.Request.Context(), "xcash", c.Request)
	c.String(status, body)
}

func (m *Module) creemNotify(c *gin.Context) {
	status, body := m.svc.HandleNotify(c.Request.Context(), "creem", c.Request)
	c.String(status, body)
}

func (m *Module) lemonSqueezyNotify(c *gin.Context) {
	status, body := m.svc.HandleNotify(c.Request.Context(), "lemonsqueezy", c.Request)
	c.String(status, body)
}

type ordersData struct {
	Rows       []Order
	Total      int64
	Page       int
	TotalPages int
	BasePath   string
	Error      string
}

func (m *Module) ordersPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	const perPage = 20
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	rows, total, err := m.svc.ListOrdersPage(c.Request.Context(), user.ID, perPage, (page-1)*perPage)
	if err != nil {
		m.log.Error("list orders failed", "error", err)
	}
	data := m.page(c, "我的订单")
	data.Robots = "noindex,nofollow"
	data.Data = ordersData{
		Rows: rows, Total: total, Page: page, TotalPages: totalPages(total, perPage),
		BasePath: "/billing/orders", Error: c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "orders", data)
}

type subscriptionData struct {
	Subs         []Subscription
	Entitlements []Entitlement
	Total        int64
	Page         int
	TotalPages   int
	BasePath     string
}

func (m *Module) subscriptionPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	const perPage = 20
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	subs, total, err := m.svc.ListSubscriptionsPage(c.Request.Context(), user.ID, perPage, (page-1)*perPage)
	if err != nil {
		m.log.Error("list subscriptions failed", "error", err)
	}
	ents, _ := m.svc.ActiveEntitlements(c.Request.Context(), user.ID)
	data := m.page(c, "我的订阅")
	data.Robots = "noindex,nofollow"
	data.Data = subscriptionData{
		Subs: subs, Entitlements: ents, Total: total, Page: page,
		TotalPages: totalPages(total, perPage), BasePath: "/billing/subscription",
	}
	m.renderer.Render(c, http.StatusOK, "subscription", data)
}

type adminData struct {
	Products       []Product
	EditingProduct *Product
	Channels       []PaymentChannel
	Orders         []Order
	Coupons        []Coupon
	Subscriptions  []Subscription
	Runs           []ReconciliationRun
	Report         []ReportRow
	Error          string

	// Deipay token picker
	TokenGroups      []ChainGroup
	SelectedTokens   []TokenOption
	SelectedTokenIDs []string
	DeipayChainID    int64
	DeipayDeadline   int64
	TokenSaved       bool
	TokenError       string
	// OpenTab preselects a billing tab (?tab=channels after saving tokens).
	OpenTab string
}

func (m *Module) adminPage(c *gin.Context) {
	products, _ := m.svc.ListProducts(c.Request.Context(), false)
	channels, _ := m.svc.ListChannels(c.Request.Context(), false)
	orders, _ := m.svc.ListOrders(c.Request.Context(), "", 100)
	coupons, _ := m.svc.ListCoupons(c.Request.Context())
	subs, _ := m.svc.ListSubscriptions(c.Request.Context(), "")
	runs, _ := m.svc.ListReconciliation(c.Request.Context())
	report, _ := m.svc.Report(c.Request.Context(), 7)
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.SetPageTitle("商业化", m.settings.GetString("seo.title_template"))
	if p := data.User; p != nil && m.registry != nil {
		data.AdminNav = m.registry.NavFor(p)
		data.AdminGroups = adminui.BuildGroups(data.AdminNav)
	}
	var editingProduct *Product
	if id := c.Query("product"); id != "" {
		if p, err := m.svc.GetProduct(c.Request.Context(), id); err == nil {
			editingProduct = p
		}
	}
	data.Data = adminData{
		Products: products, EditingProduct: editingProduct, Channels: channels, Orders: orders, Coupons: coupons,
		Subscriptions: subs, Runs: runs, Report: report, Error: c.Query("error"),
		TokenGroups:      TokenGroups(),
		SelectedTokens:   SelectedTokens(m.settings.GetString("billing.deipay.tokens")),
		SelectedTokenIDs: ParseTokenList(m.settings.GetString("billing.deipay.tokens")),
		DeipayChainID:    m.settings.GetInt("billing.deipay.chain_id"),
		DeipayDeadline:   m.settings.GetInt("billing.deipay.deadline_secs"),
		TokenSaved:       c.Query("tokens_saved") == "1",
		TokenError:       c.Query("token_error"),
		OpenTab:          c.DefaultQuery("tab", ""),
	}
	m.renderer.Render(c, http.StatusOK, "admin_billing", data)
}

func (m *Module) adminAction(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	actor := user.ID
	action := c.PostForm("action")
	var err error
	switch action {
	case "create_product":
		price, _ := strconv.ParseInt(c.PostForm("price_minor"), 10, 64)
		pointsAmount, _ := strconv.ParseInt(c.PostForm("points_amount"), 10, 64)
		duration, _ := strconv.Atoi(c.PostForm("duration_days"))
		err = m.svc.CreateProduct(c.Request.Context(), &Product{
			Type: c.PostForm("type"), Name: c.PostForm("name"), Slug: c.PostForm("slug"),
			Description: c.PostForm("description"), PriceMinor: price, Currency: "CNY",
			PointsAmount: pointsAmount, DurationDays: duration, Active: true,
		})
	case "toggle_product":
		err = m.svc.ToggleProduct(c.Request.Context(), c.PostForm("product_id"), c.PostForm("active") != "", actor)
	case "update_product":
		price, _ := strconv.ParseInt(c.PostForm("price_minor"), 10, 64)
		pointsAmount, _ := strconv.ParseInt(c.PostForm("points_amount"), 10, 64)
		duration, _ := strconv.Atoi(c.PostForm("duration_days"))
		sort, _ := strconv.Atoi(c.PostForm("sort"))
		err = m.svc.UpdateProduct(c.Request.Context(), &Product{
			ID: c.PostForm("product_id"), Type: c.PostForm("type"), Name: c.PostForm("name"),
			Slug: c.PostForm("slug"), Description: c.PostForm("description"),
			PriceMinor: price, Currency: c.PostForm("currency"), PointsAmount: pointsAmount,
			DurationDays: duration, Sort: sort, Active: c.PostForm("active") != "",
		}, actor)
	case "delete_product":
		err = m.svc.DeleteProduct(c.Request.Context(), c.PostForm("product_id"), actor)
	case "create_coupon":
		value, _ := strconv.ParseInt(c.PostForm("value"), 10, 64)
		minAmount, _ := strconv.ParseInt(c.PostForm("min_amount_minor"), 10, 64)
		total, _ := strconv.Atoi(c.PostForm("total"))
		ends := time.Now().UTC().AddDate(0, 1, 0)
		if raw := c.PostForm("ends_at"); raw != "" {
			if t, e := time.Parse("2006-01-02", raw); e == nil {
				ends = t.UTC()
			}
		}
		err = m.svc.CreateCoupon(c.Request.Context(), &Coupon{
			Code: c.PostForm("code"), Type: c.PostForm("type"), Value: value,
			MinAmountMinor: minAmount, Total: total, PerUserLimit: 1,
			StartsAt: time.Now().UTC(), EndsAt: ends, Enabled: true,
		})
	case "confirm_order":
		err = m.svc.ConfirmManual(c.Request.Context(), c.PostForm("order_id"), actor)
	case "run_reconciliation":
		_, err = m.svc.RunReconciliation(c.Request.Context(), c.PostForm("channel_code"), c.PostForm("biz_date"))
	case "adjust_balance":
		amount, _ := strconv.ParseInt(c.PostForm("amount_minor"), 10, 64)
		err = m.svc.AdjustBalance(c.Request.Context(), c.PostForm("user_id"), amount, actor, c.PostForm("reason"))
	default:
		err = httpx.Invalid(map[string]string{"action": "未知操作"})
	}
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/billing?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/billing")
}

func (m *Module) adminRefund(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.RefundOrder(c.Request.Context(), c.PostForm("order_id"), user.ID, c.PostForm("reason")); err != nil {
		c.Redirect(http.StatusFound, "/admin/billing?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/billing")
}

func (m *Module) adminChannel(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.ToggleChannel(c.Request.Context(), c.PostForm("channel_id"), c.PostForm("enabled") != "", user.ID); err != nil {
		c.Redirect(http.StatusFound, "/admin/billing?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/billing")
}

// adminDeipayTokens saves the token picker selection. The form posts token IDs
// (chainId:address) picked from the catalog; no manual contract typing needed.
func (m *Module) adminDeipayTokens(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if err := c.Request.ParseForm(); err != nil {
		c.Redirect(http.StatusFound, "/admin/billing?tab=channels&token_error="+url.QueryEscape("表单解析失败"))
		return
	}

	ids := make([]string, 0, 8)
	seen := map[string]bool{}
	chainID := int64(0)
	for _, raw := range c.Request.PostForm["tokens"] {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		token, ok := TokenByID(raw)
		if !ok {
			continue // ignore anything not in the catalog
		}
		key := strings.ToLower(raw)
		if seen[key] {
			continue
		}
		// Deipay invoices carry one chainId, so all tokens must share a chain.
		if chainID == 0 {
			chainID = token.ChainID
		} else if token.ChainID != chainID {
			c.Redirect(http.StatusFound, "/admin/billing?tab=channels&token_error="+
				url.QueryEscape("一张账单只能使用同一个网络，请只选择同一链上的币种"))
			return
		}
		seen[key] = true
		ids = append(ids, key)
	}
	if len(ids) == 0 {
		c.Redirect(http.StatusFound, "/admin/billing?tab=channels&token_error="+
			url.QueryEscape("请至少选择一个接收币种"))
		return
	}

	values := map[string]any{"billing.deipay.tokens": strings.Join(ids, ",")}
	if chainID > 0 {
		values["billing.deipay.chain_id"] = int(chainID)
	}
	if raw := strings.TrimSpace(c.PostForm("deadline_secs")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 300 {
			values["billing.deipay.deadline_secs"] = int(n)
		}
	}
	if c.PostForm("custom_deadline") == "" {
		// keep existing value
		delete(values, "billing.deipay.deadline_secs")
	}

	if err := m.settings.SetMany(c.Request.Context(), values, user.ID); err != nil {
		c.Redirect(http.StatusFound, "/admin/billing?tab=channels&token_error="+
			url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/billing?tab=channels&tokens_saved=1")
}

// totalPages 根据总数与每页条数计算页数，至少为 1 页。
func totalPages(total int64, perPage int) int {
	if perPage <= 0 {
		perPage = 20
	}
	n := int((total + int64(perPage) - 1) / int64(perPage))
	if n < 1 {
		n = 1
	}
	return n
}

func orderStatusLabel(status string) string {
	switch status {
	case "paid":
		return "已支付"
	case "pending":
		return "待支付"
	case "refunded":
		return "已退款"
	case "failed":
		return "失败"
	case "closed":
		return "已关闭"
	default:
		return status
	}
}
