package ads

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/modules/billing"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/config"
	"goforum/internal/platform/events"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
)

type Module struct {
	db *interface { /* unused */
	}
	svc      *Service
	settings *settings.Store
	renderer *view.Renderer
	auth     contracts.Authenticator
	sessions *session.Store
	cfg      *config.Config
	log      *slog.Logger
	version  string
	registry adminui.Registry
}

func New(svc *Service) *Module { return &Module{svc: svc} }

func (m *Module) Configure(st *settings.Store, renderer *view.Renderer, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, version string) {
	m.settings = st
	m.renderer = renderer
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.version = version
	m.svc.Configure(st, nil)
}

func (m *Module) SetBilling(b *billing.Service)  { m.svc.Configure(m.settings, b) }
func (m *Module) SetRegistry(r adminui.Registry) { m.registry = r }

func (m *Module) Name() string                   { return "ads" }
func (m *Module) DependsOn() []string            { return []string{"auth", "billing"} }
func (m *Module) Models() []any                  { return Models() }
func (m *Module) Subscribe(bus events.Bus) error { return m.svc.Subscribe(bus) }

func (m *Module) Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "ads.manage", Label: "管理广告", Group: "运营"},
	}
}

func (m *Module) Settings() []settings.Definition {
	return []settings.Definition{
		{Namespace: "ads", Key: "self_serve.enabled", Group: "ops", Label: "开放自助推广", Type: settings.TypeBool, Default: true, Permission: "settings.write"},
		{Namespace: "ads", Key: "self_serve.review", Group: "ops", Label: "自助广告先审核后投放", Type: settings.TypeBool, Default: false, Permission: "settings.write"},
		{Namespace: "ads", Key: "self_serve.max_months", Group: "ops", Label: "自助投放最长月数", Type: settings.TypeInt, Default: 12, Min: 1, Max: 36, Permission: "settings.write"},
	}
}

func (m *Module) Admin() adminui.Contribution {
	return adminui.Contribution{
		Nav: []adminui.NavItem{
			{Group: "ops", Label: "广告管理", URL: "/admin/ads", Permission: "ads.manage", Order: 90},
			{Group: "ops", Label: "广告位管理", URL: "/admin/ads/slots", Permission: "ads.manage", Order: 92},
			{Group: "ops", Label: "广告设置", URL: "/admin/ads/settings", Permission: "ads.manage", Order: 95},
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
	manage := middleware.RequirePermission(md, "ads.manage")

	web.GET("/promote", login, m.promotePage)
	web.POST("/promote", login, middleware.RateLimit(md, "ad_purchase", 3, time.Hour), m.purchase)
	web.GET("/ads/:id/click", m.click)
	web.GET("/ads/:id/frame", m.frame)

	web.GET("/admin/ads", login, manage, m.adminPage)
	web.POST("/admin/ads/save", login, manage, m.adminSave)
	web.POST("/admin/ads/action", login, manage, m.adminAction)
	web.GET("/admin/ads/slots", login, manage, m.slotsPage)
	web.POST("/admin/ads/slots/save", login, manage, m.slotsSave)
	web.POST("/admin/ads/slots/action", login, manage, m.slotsAction)
	web.GET("/admin/ads/settings", login, manage, m.settingsPage)
	web.POST("/admin/ads/settings", login, manage, m.settingsSave)
}

type promotePlan struct {
	Placement
	TextPrice  int64
	ImagePrice int64
}

type promoteData struct {
	Enabled    bool
	Plans      []promotePlan
	Prices     map[string]int64
	Sizes      map[string]string
	Channels   []Channel
	MaxMonths  int
	MyAds      []Ad
	Total      int64
	Page       int
	TotalPages int
	BasePath   string
	Error      string
	Success    bool
}

type adminData struct {
	Ads        []Ad
	Editing    *Ad
	Slots      []AdSlot
	Status     string
	Slot       string
	Total      int64
	Page       int
	TotalPages int
	BasePath   string
	Error      string
}

type settingsData struct {
	Enabled   bool
	Review    bool
	MaxMonths int
	Saved     bool
	Error     string
}

type slotsData struct {
	Slots   []AdSlot
	Editing *AdSlot
	Saved   bool
	Error   string
}

var adminStatuses = map[string]bool{
	"": true, "pending_payment": true, "pending_review": true, "active": true, "paused": true, "banned": true, "rejected": true,
}

func (m *Module) base(c *gin.Context, title string) view.Data {
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.SetPageTitle(title, m.settings.GetString("seo.title_template"))
	if p := data.User; p != nil && m.registry != nil {
		data.AdminNav = m.registry.NavFor(p)
		data.AdminGroups = adminui.BuildGroups(data.AdminNav)
	}
	return data
}

func (m *Module) promoteDataFor(c *gin.Context, errMsg string) promoteData {
	user := authn.FromContext(c.Request.Context())
	slots, _ := m.svc.SlotConfigs(c.Request.Context(), true)
	plans := make([]promotePlan, 0, len(slots))
	prices := make(map[string]int64, len(slots)*2)
	sizes := make(map[string]string, len(slots))
	for _, slot := range slots {
		if !slot.SellEnabled || (slot.PriceText == 0 && slot.PriceImage == 0) {
			continue
		}
		plans = append(plans, promotePlan{
			Placement: Placement{Key: slot.Key, Label: slot.Name, Description: slot.Description, Size: slot.Size, Height: slot.Height},
			TextPrice: slot.PriceText, ImagePrice: slot.PriceImage,
		})
		prices[slot.Key+".text"] = slot.PriceText
		prices[slot.Key+".image"] = slot.PriceImage
		sizes[slot.Key] = slot.Size
	}
	const perPage = 20
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	ads, total, err := m.svc.ListUserPage(c.Request.Context(), user.ID, perPage, (page-1)*perPage)
	if err != nil {
		m.log.Error("list user ads failed", "error", err)
	}
	return promoteData{
		Enabled: m.svc.SelfServeEnabled(), Plans: plans, Prices: prices, Sizes: sizes, Channels: m.svc.Channels(c.Request.Context()),
		MaxMonths: m.svc.MaxMonths(), MyAds: ads, Total: total, Page: page,
		TotalPages: totalPages(total, perPage), BasePath: "/promote", Error: errMsg,
		Success: c.Query("success") == "1",
	}
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
