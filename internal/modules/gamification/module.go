package gamification

import (
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	cmod "goforum/internal/modules/community/contracts"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/config"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/permission"
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

func (m *Module) Configure(st *settings.Store, community cmod.Reader, auditWriter audit.Writer, bus events.Bus, renderer *view.Renderer, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, version string) {
	m.svc.Configure(st, community, auditWriter, bus)
	m.settings = st
	m.renderer = renderer
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.version = version
}

func (m *Module) SetRegistry(r adminui.Registry) { m.registry = r }

func (m *Module) Name() string                    { return "gamification" }
func (m *Module) DependsOn() []string             { return []string{"auth", "community"} }
func (m *Module) Models() []any                   { return Models() }
func (m *Module) Subscribe(bus events.Bus) error  { return m.svc.Subscribe(bus) }
func (m *Module) Settings() []settings.Definition { return nil }

func (m *Module) Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "bounty.create", Label: "发布悬赏", Group: "运营"},
		{Key: "lottery.join", Label: "参与抽奖", Group: "运营"},
		{Key: "invite.create", Label: "创建邀请码", Group: "运营"},
		{Key: "admin.gamification.manage", Label: "管理运营玩法", Group: "后台"},
	}
}

func (m *Module) Admin() adminui.Contribution {
	return adminui.Contribution{
		Nav: []adminui.NavItem{
			{Group: "ops", Label: "运营面板", URL: "/admin/gamification", Permission: "admin.gamification.manage", Order: 10},
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
	bountyPerm := middleware.RequirePermission(md, "bounty.create")
	lotteryPerm := middleware.RequirePermission(md, "lottery.join")
	invitePerm := middleware.RequirePermission(md, "invite.create")
	adminPerm := middleware.RequirePermission(md, "admin.gamification.manage")

	web.GET("/points", login, m.pointsPage)
	web.POST("/checkin", login, m.checkin)

	web.GET("/invites", login, m.invitesPage)
	web.POST("/invites", login, invitePerm, m.inviteCreate)

	web.GET("/lotteries", m.lotteriesPage)
	web.GET("/lotteries/:id", m.lotteryPage)
	web.POST("/lotteries/:id/join", login, lotteryPerm, m.lotteryJoin)

	web.GET("/bounties", login, m.bountiesPage)
	web.POST("/bounties", login, bountyPerm, m.bountyCreate)
	web.POST("/bounties/:id/settle", login, m.bountySettle)
	web.POST("/bounties/:id/refund", login, m.bountyRefund)

	web.GET("/admin/gamification", login, adminPerm, m.adminPage)
	web.POST("/admin/gamification/action", login, adminPerm, m.adminAction)
}

type pointsData struct {
	Balance      *PointBalance
	Status       *CheckinStatus
	Levels       []Level
	Transactions []PointTransaction
	Total        int64
	Page         int
	TotalPages   int
	PerPage      int
	Badges       []Badge
	NextLevel    *Level
	Error        string
}

func (m *Module) page(c *gin.Context, title string) view.Data {
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.SetPageTitle(title, m.settings.GetString("seo.title_template"))
	return data
}

func (m *Module) pointsPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	bal, err := m.svc.Balance(c.Request.Context(), user.ID)
	if err != nil {
		m.log.Error("load points balance failed", "error", err)
	}
	status, _ := m.svc.CheckinStatus(c.Request.Context(), user.ID)
	levels, _ := m.svc.ListLevels(c.Request.Context())
	const perPage = 20
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	txs, total, _ := m.svc.Transactions(c.Request.Context(), user.ID, perPage, (page-1)*perPage)
	totalPages := int((total + int64(perPage) - 1) / int64(perPage))
	badges, _ := m.svc.BadgesOf(c.Request.Context(), user.ID)
	var next *Level
	for i := range levels {
		if levels[i].MinPoints > bal.Balance {
			next = &levels[i]
			break
		}
	}
	data := m.page(c, "积分与签到")
	data.Data = pointsData{
		Balance: bal, Status: status, Levels: levels, Transactions: txs,
		Total: total, Page: page, TotalPages: totalPages, PerPage: perPage,
		Badges: badges, NextLevel: next, Error: c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "points", data)
}

func (m *Module) checkin(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if _, _, err := m.svc.Checkin(c.Request.Context(), user.ID); err != nil {
		c.Redirect(http.StatusFound, "/points?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/points")
}

type invitesData struct {
	Rows       []Invite
	Code       string
	Total      int64
	Page       int
	TotalPages int
	BasePath   string
	Error      string
}

func (m *Module) invitesPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	const perPage = 20
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	rows, total, err := m.svc.InvitesByPage(c.Request.Context(), user.ID, perPage, (page-1)*perPage)
	if err != nil {
		m.log.Error("list invites failed", "error", err)
	}
	pages := int((total + int64(perPage) - 1) / int64(perPage))
	if pages < 1 {
		pages = 1
	}
	data := m.page(c, "邀请")
	data.Robots = "noindex,nofollow"
	data.Data = invitesData{
		Rows: rows, Code: c.Query("code"), Total: total, Page: page,
		TotalPages: pages, BasePath: "/invites", Error: c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "invites", data)
}

func (m *Module) inviteCreate(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	inv, err := m.svc.CreateInvite(c.Request.Context(), user.ID, c.PostForm("email"))
	if err != nil {
		c.Redirect(http.StatusFound, "/invites?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/invites?code="+inv.Code)
}

type lotteriesData struct {
	Rows  []Lottery
	Error string
}

func (m *Module) lotteriesPage(c *gin.Context) {
	rows, _ := m.svc.ListLotteries(c.Request.Context())
	data := m.page(c, "抽奖")
	data.Data = lotteriesData{Rows: rows, Error: c.Query("error")}
	m.renderer.Render(c, http.StatusOK, "lotteries", data)
}

func (m *Module) lotteryPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}
	v, err := m.svc.GetLottery(c.Request.Context(), c.Param("id"), userID)
	if err != nil {
		data := m.page(c, "抽奖")
		data.Data = view.ErrorData{Code: 404, Message: "抽奖不存在", RequestID: c.GetString("request_id")}
		m.renderer.Render(c, http.StatusNotFound, "error", data)
		return
	}
	data := m.page(c, v.Lottery.Title)
	data.Data = struct {
		View  *LotteryView
		Error string
	}{View: v, Error: c.Query("error")}
	m.renderer.Render(c, http.StatusOK, "lottery_detail", data)
}

func (m *Module) lotteryJoin(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.JoinLottery(c.Request.Context(), user.ID, c.Param("id")); err != nil {
		c.Redirect(http.StatusFound, "/lotteries/"+c.Param("id")+"?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/lotteries/"+c.Param("id"))
}

type bountiesData struct {
	Rows  []Bounty
	Error string
}

func (m *Module) bountiesPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	rows, _ := m.svc.ListBounties(c.Request.Context(), user.ID)
	data := m.page(c, "悬赏")
	data.Robots = "noindex,nofollow"
	data.Data = bountiesData{Rows: rows, Error: c.Query("error")}
	m.renderer.Render(c, http.StatusOK, "bounties", data)
}

func (m *Module) bountyCreate(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	amount, _ := strconv.ParseInt(c.PostForm("amount"), 10, 64)
	if _, err := m.svc.CreateBounty(c.Request.Context(), user.ID, c.PostForm("topic_id"), amount); err != nil {
		c.Redirect(http.StatusFound, "/bounties?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/bounties")
}

func (m *Module) bountySettle(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.SettleBounty(c.Request.Context(), user.ID, c.Param("id"), c.PostForm("post_id")); err != nil {
		c.Redirect(http.StatusFound, "/bounties?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/bounties")
}

func (m *Module) bountyRefund(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.RefundBounty(c.Request.Context(), user.ID, c.Param("id"), false); err != nil {
		c.Redirect(http.StatusFound, "/bounties?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/bounties")
}

type adminData struct {
	Rules     []PointRule
	Levels    []Level
	Badges    []Badge
	Invites   []Invite
	Lotteries []Lottery
	Bounties  []Bounty
	Error     string
}

func (m *Module) adminPage(c *gin.Context) {
	rules, _ := m.svc.ListRules(c.Request.Context())
	levels, _ := m.svc.ListLevels(c.Request.Context())
	badges, _ := m.svc.ListBadges(c.Request.Context())
	invites, _ := m.svc.AllInvites(c.Request.Context())
	lotteries, _ := m.svc.ListLotteries(c.Request.Context())
	bounties, _ := m.svc.ListBounties(c.Request.Context(), "")
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.SetPageTitle("运营面板", m.settings.GetString("seo.title_template"))
	if p := data.User; p != nil && m.registry != nil {
		data.AdminNav = m.registry.NavFor(p)
		data.AdminGroups = adminui.BuildGroups(data.AdminNav)
	}
	data.Data = adminData{
		Rules: rules, Levels: levels, Badges: badges, Invites: invites,
		Lotteries: lotteries, Bounties: bounties, Error: c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "admin_gamification", data)
}

func (m *Module) adminAction(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	actor := user.ID
	action := c.PostForm("action")
	var err error
	switch action {
	case "update_rule":
		points, _ := strconv.ParseInt(c.PostForm("points"), 10, 64)
		limit, _ := strconv.Atoi(c.PostForm("daily_limit"))
		err = m.svc.UpdateRule(c.Request.Context(), c.PostForm("rule_id"), points, limit, c.PostForm("enabled") != "", actor)
	case "update_level":
		minPoints, _ := strconv.ParseInt(c.PostForm("min_points"), 10, 64)
		level, _ := strconv.Atoi(c.PostForm("level"))
		err = m.svc.UpdateLevel(c.Request.Context(), level, minPoints, c.PostForm("name"), c.PostForm("color"), actor)
	case "create_badge":
		slug := strings.TrimSpace(c.PostForm("slug"))
		if slug == "" {
			slug = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(c.PostForm("name")), " ", "-"))
		}
		err = m.svc.CreateBadge(c.Request.Context(), &Badge{
			Name: strings.TrimSpace(c.PostForm("name")), Slug: slug,
			Description: c.PostForm("description"), Icon: c.PostForm("icon"),
		})
	case "create_lottery":
		winners, _ := strconv.Atoi(c.PostForm("winner_count"))
		cost, _ := strconv.ParseInt(c.PostForm("entry_cost"), 10, 64)
		minLevel, _ := strconv.Atoi(c.PostForm("min_level"))
		ends := time.Now().UTC().Add(7 * 24 * time.Hour)
		if raw := c.PostForm("ends_at"); raw != "" {
			if t, e := time.Parse("2006-01-02T15:04", raw); e == nil {
				ends = t.UTC()
			}
		}
		_, err = m.svc.CreateLottery(c.Request.Context(), c.PostForm("title"), c.PostForm("prize"),
			c.PostForm("description"), winners, cost, minLevel, ends)
		if err == nil {
			m.svc.writeAudit(c.Request.Context(), actor, "lottery.create", "lottery", "", "")
		}
	case "draw_lottery":
		_, err = m.svc.DrawLottery(c.Request.Context(), c.PostForm("lottery_id"), actor)
	case "settle_bounty":
		var b *Bounty
		b, err = m.svc.GetBounty(c.Request.Context(), c.PostForm("bounty_id"))
		if err == nil {
			err = m.svc.SettleBounty(c.Request.Context(), b.UserID, b.ID, c.PostForm("post_id"))
		}
	case "refund_bounty":
		err = m.svc.RefundBounty(c.Request.Context(), actor, c.PostForm("bounty_id"), true)
	default:
		err = httpx.Invalid(map[string]string{"action": "未知操作"})
	}
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/gamification?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/gamification")
}
