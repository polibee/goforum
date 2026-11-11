package moderation

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
	mcontracts "goforum/internal/modules/messaging/contracts"
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

func (m *Module) Configure(st *settings.Store, community cmod.Reader, moderator cmod.Moderator, messages mcontracts.Reader, auditWriter audit.Writer, bus events.Bus, renderer *view.Renderer, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, version string) {
	m.svc.Configure(st, community, moderator, auth, messages, auditWriter, bus)
	m.settings = st
	m.renderer = renderer
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.version = version
}

func (m *Module) SetRegistry(r adminui.Registry) { m.registry = r }

func (m *Module) Name() string               { return "moderation" }
func (m *Module) DependsOn() []string        { return []string{"auth", "community", "messaging"} }
func (m *Module) Models() []any              { return Models() }
func (m *Module) Subscribe(events.Bus) error { return nil }

func (m *Module) Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "flag.create", Label: "举报", Group: "治理"},
		{Key: "flag.review", Label: "处理举报", Group: "治理"},
		{Key: "moderation.queue.view", Label: "查看审核队列", Group: "治理"},
		{Key: "moderation.action", Label: "执行审核动作", Group: "治理"},
		{Key: "moderation.word.manage", Label: "管理敏感词", Group: "治理"},
		{Key: "moderation.audit.view", Label: "查看审计日志", Group: "治理"},
	}
}

func (m *Module) Settings() []settings.Definition {
	return []settings.Definition{
		{Namespace: "moderation", Key: "new_user_review", Group: "antispam", Label: "新用户内容进审核", Type: settings.TypeBool, Default: false, Permission: "settings.write"},
		{Namespace: "moderation", Key: "auto_hide_flags", Group: "antispam", Label: "自动隐藏举报阈值", Type: settings.TypeInt, Default: 3, Min: 1, Max: 100, Permission: "settings.write"},
	}
}

func (m *Module) Admin() adminui.Contribution {
	return adminui.Contribution{
		Nav: []adminui.NavItem{
			{Group: "moderation", Label: "审核队列", URL: "/admin/review", Permission: "moderation.queue.view", Order: 10},
			{Group: "moderation", Label: "申诉", URL: "/admin/appeals", Permission: "moderation.queue.view", Order: 15},
			{Group: "moderation", Label: "敏感词", URL: "/admin/banned-words", Permission: "moderation.word.manage", Order: 20},
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
	flagPerm := middleware.RequirePermission(md, "flag.create")
	queuePerm := middleware.RequirePermission(md, "moderation.queue.view")
	actionPerm := middleware.RequirePermission(md, "moderation.action")
	wordPerm := middleware.RequirePermission(md, "moderation.word.manage")

	web.GET("/report", login, flagPerm, m.reportPage)
	web.POST("/report", login, flagPerm, m.reportSubmit)
	api.POST("/flags", login, flagPerm, m.apiFlag)

	web.GET("/appeal", m.appealPage)
	web.POST("/appeal", middleware.RateLimit(md, "appeal", 5, time.Hour), m.appealSubmit)

	web.GET("/admin/review", login, queuePerm, m.reviewPage)
	web.GET("/admin/appeals", login, queuePerm, m.appealsPage)
	web.POST("/admin/appeals/:id/decide", login, actionPerm, m.appealDecide)
	web.POST("/admin/review/:id/claim", login, actionPerm, m.reviewClaim)
	web.POST("/admin/review/:id/decide", login, actionPerm, m.reviewDecide)

	web.GET("/admin/banned-words", login, wordPerm, m.wordsPage)
	web.POST("/admin/banned-words", login, wordPerm, m.wordCreate)
	web.POST("/admin/banned-words/:id/delete", login, wordPerm, m.wordDelete)
}

type reportData struct {
	TargetType string
	TargetID   string
	Title      string
	Author     string
	Error      string
}

func (m *Module) reportPage(c *gin.Context) {
	targetType := c.Query("target_type")
	targetID := c.Query("target_id")
	title, author, err := m.svc.TargetInfo(c.Request.Context(), targetType, targetID)
	if err != nil {
		data := view.BaseData(c, m.settings.SiteInfo(), m.version)
		data.Title = "举报 - " + data.Site.Name
		data.Data = view.ErrorData{Code: 404, Message: httpx.ErrorOf(err).Message, RequestID: c.GetString("request_id")}
		m.renderer.Render(c, http.StatusNotFound, "error", data)
		return
	}
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.Title = "举报 - " + data.Site.Name
	data.Robots = "noindex,nofollow"
	data.Data = reportData{TargetType: targetType, TargetID: targetID, Title: title, Author: author}
	m.renderer.Render(c, http.StatusOK, "report", data)
}

func (m *Module) reportSubmit(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	err := m.svc.Flag(c.Request.Context(), user.ID,
		c.PostForm("target_type"), c.PostForm("target_id"),
		c.PostForm("reason_code"), c.PostForm("reason_text"))
	if err != nil {
		data := view.BaseData(c, m.settings.SiteInfo(), m.version)
		data.Title = "举报 - " + data.Site.Name
		data.Data = reportData{
			TargetType: c.PostForm("target_type"), TargetID: c.PostForm("target_id"),
			Title: "", Error: httpx.ErrorOf(err).Message,
		}
		m.renderer.Render(c, http.StatusUnprocessableEntity, "report", data)
		return
	}
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.Title = "举报已提交 - " + data.Site.Name
	data.Data = map[string]any{
		"Title": "举报已提交", "Message": "感谢反馈，版主会尽快处理。",
		"Link": "/", "LinkLabel": "返回首页", "DevURL": "",
	}
	m.renderer.Render(c, http.StatusOK, "notice", data)
}

func (m *Module) apiFlag(c *gin.Context) {
	var in struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
		ReasonCode string `json:"reason_code"`
		ReasonText string `json:"reason_text"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"body": "请求体格式错误"}))
		return
	}
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.Flag(c.Request.Context(), user.ID, in.TargetType, in.TargetID, in.ReasonCode, in.ReasonText); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": gin.H{"ok": true}})
}

type reviewData struct {
	Rows       []ReviewRow
	Stats      QueueStats
	Total      int64
	Page       int
	TotalPages int
	Status     string
	Error      string
}

func (m *Module) reviewPage(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	status := c.Query("status")
	rows, total, err := m.svc.ListQueue(c.Request.Context(), status, page, 20)
	if err != nil {
		m.log.Error("list review queue failed", "error", err)
	}
	pages := int((total + 19) / 20)
	if pages < 1 {
		pages = 1
	}
	stats, _ := m.svc.Stats(c.Request.Context())
	data := m.page(c, "审核队列")
	data.Data = reviewData{Rows: rows, Stats: stats, Total: total, Page: page, TotalPages: pages, Status: status, Error: c.Query("error")}
	m.renderer.Render(c, http.StatusOK, "admin_review", data)
}

func (m *Module) page(c *gin.Context, title string) view.Data {
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.SetPageTitle(title, m.settings.GetString("seo.title_template"))
	if p := data.User; p != nil && m.registry != nil {
		data.AdminNav = m.registry.NavFor(p)
		data.AdminGroups = adminui.BuildGroups(data.AdminNav)
	}
	return data
}

func (m *Module) reviewClaim(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.Claim(c.Request.Context(), c.Param("id"), user.ID); err != nil {
		c.Redirect(http.StatusFound, adminError("/admin/review", httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/review")
}

func (m *Module) reviewDecide(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	action := c.PostForm("action")
	note := c.PostForm("note")
	var until *time.Time
	if action == "suspend" || action == "ban" {
		switch c.PostForm("duration") {
		case "1d":
			t := time.Now().UTC().AddDate(0, 0, 1)
			until = &t
		case "7d":
			t := time.Now().UTC().AddDate(0, 0, 7)
			until = &t
		case "30d":
			t := time.Now().UTC().AddDate(0, 0, 30)
			until = &t
		}
	}
	if err := m.svc.Decide(c.Request.Context(), c.Param("id"), user.ID, action, note, until); err != nil {
		c.Redirect(http.StatusFound, adminError("/admin/review", httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/review")
}

type appealPageData struct {
	Username  string
	Reason    string
	Submitted bool
	Error     string
}

func (m *Module) appealPage(c *gin.Context) {
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.Title = "账号申诉 - " + data.Site.Name
	data.Robots = "noindex,nofollow"
	data.Data = appealPageData{Submitted: c.Query("submitted") == "1"}
	m.renderer.Render(c, http.StatusOK, "appeal", data)
}

func (m *Module) appealSubmit(c *gin.Context) {
	username := c.PostForm("username")
	reason := c.PostForm("reason")
	if err := m.svc.SubmitAppeal(c.Request.Context(), username, reason); err != nil {
		data := view.BaseData(c, m.settings.SiteInfo(), m.version)
		data.Title = "账号申诉 - " + data.Site.Name
		data.Robots = "noindex,nofollow"
		data.Data = appealPageData{Username: username, Reason: reason, Error: httpx.ErrorOf(err).Message}
		m.renderer.Render(c, http.StatusUnprocessableEntity, "appeal", data)
		return
	}
	c.Redirect(http.StatusFound, "/appeal?submitted=1")
}

type appealsData struct {
	Rows       []AppealRow
	Total      int64
	Page       int
	TotalPages int
	Status     string
	Stats      AppealStats
}

type AppealStats struct {
	Pending  int64
	Approved int64
	Rejected int64
}

func (m *Module) appealsPage(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	status := c.Query("status")
	rows, total, err := m.svc.ListAppeals(c.Request.Context(), status, page, 20)
	if err != nil {
		m.log.Error("list appeals failed", "error", err)
	}
	pages := int((total + 19) / 20)
	if pages < 1 {
		pages = 1
	}
	data := m.page(c, "申诉")
	stats, _ := m.svc.AppealStats(c.Request.Context())
	data.Data = appealsData{Rows: rows, Total: total, Page: page, TotalPages: pages, Status: status, Stats: stats}
	m.renderer.Render(c, http.StatusOK, "admin_appeals", data)
}

func (m *Module) appealDecide(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	action := c.PostForm("action")
	note := c.PostForm("note")
	if err := m.svc.DecideAppeal(c.Request.Context(), c.Param("id"), user.ID, action, note); err != nil {
		c.Redirect(http.StatusFound, adminError("/admin/appeals", httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/appeals")
}

type wordsData struct {
	Rows       []BannedWord
	Total      int64
	Page       int
	TotalPages int
	BasePath   string
	Error      string
}

func (m *Module) wordsPage(c *gin.Context) {
	const perPage = 20
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	rows, total, err := m.svc.ListWordsPage(c.Request.Context(), perPage, (page-1)*perPage)
	if err != nil {
		m.log.Error("list banned words failed", "error", err)
	}
	pages := int((total + int64(perPage) - 1) / int64(perPage))
	if pages < 1 {
		pages = 1
	}
	data := m.page(c, "敏感词")
	data.Data = wordsData{
		Rows: rows, Total: total, Page: page, TotalPages: pages,
		BasePath: "/admin/banned-words", Error: c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "admin_banned_words", data)
}

func (m *Module) wordCreate(c *gin.Context) {
	if err := m.svc.CreateWord(c.Request.Context(),
		c.PostForm("word"), c.PostForm("match_type"), c.PostForm("action"),
		c.PostForm("replacement")); err != nil {
		c.Redirect(http.StatusFound, adminError("/admin/banned-words", httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/banned-words")
}

func (m *Module) wordDelete(c *gin.Context) {
	if err := m.svc.DeleteWord(c.Request.Context(), c.Param("id")); err != nil {
		c.Redirect(http.StatusFound, adminError("/admin/banned-words", httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/banned-words")
}

func adminError(path, message string) string {
	sep := "?"
	if contains(path, "?") {
		sep = "&"
	}
	return path + sep + "error=" + url.QueryEscape(message)
}

func contains(s, sub string) bool { return len(s) >= len(sub) && strings.Contains(s, sub) }
