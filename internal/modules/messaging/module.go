package messaging

import (
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/config"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/markdown"
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
}

func New(db *gorm.DB, svc *Service) *Module { return &Module{db: db, svc: svc} }

func (m *Module) Configure(md *markdown.Renderer, bus events.Bus, renderer *view.Renderer, st *settings.Store, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, version string) {
	m.svc.Configure(auth, md, bus)
	m.renderer = renderer
	m.settings = st
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.version = version
}

func (m *Module) Name() string                    { return "messaging" }
func (m *Module) DependsOn() []string             { return []string{"auth"} }
func (m *Module) Models() []any                   { return Models() }
func (m *Module) Subscribe(events.Bus) error      { return nil }
func (m *Module) Settings() []settings.Definition { return nil }
func (m *Module) Admin() adminui.Contribution     { return adminui.Contribution{} }

func (m *Module) Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "message.send", Label: "发送私信", Group: "私信"},
		{Key: "message.block", Label: "屏蔽用户", Group: "私信"},
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
	login := middleware.RequireLogin(m.md())
	sendPerm := middleware.RequirePermission(m.md(), "message.send")

	web.GET("/messages", login, m.listPage)
	web.POST("/messages", login, sendPerm, m.startConversation)
	web.GET("/messages/:id", login, m.threadPage)
	web.POST("/messages/:id", login, sendPerm, m.sendMessage)
	web.POST("/messages/:id/block", login, m.blockConversation)

	api.GET("/conversations", login, m.apiList)
	api.POST("/conversations", login, sendPerm, m.apiStart)
	api.GET("/conversations/:id", login, m.apiGet)
	api.POST("/conversations/:id/messages", login, sendPerm, m.apiSend)
}

type listData struct {
	Rows       []ConversationRow
	Total      int64
	Page       int
	TotalPages int
	Query      string
	UnreadOnly bool
	Error      string
}

func (m *Module) listPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const perPage = 20
	q := strings.TrimSpace(c.Query("q"))
	otherID := ""
	if q != "" {
		if sums, err := m.auth.FindByUsernames(c.Request.Context(), []string{strings.ToLower(q)}); err == nil {
			if target, ok := sums[strings.ToLower(q)]; ok {
				otherID = target.ID
			}
		}
	}
	msgQuery := q
	if otherID != "" {
		msgQuery = ""
	}
	unreadOnly := c.Query("unread") == "1"
	rows, total, err := m.svc.List(c.Request.Context(), user.ID, perPage, (page-1)*perPage, msgQuery, otherID, unreadOnly)
	if err != nil {
		m.log.Error("list conversations failed", "error", err)
	}
	pages := int((total + perPage - 1) / perPage)
	if pages < 1 {
		pages = 1
	}
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.Title = "私信 - " + data.Site.Name
	data.Robots = "noindex,nofollow"
	data.Data = listData{Rows: rows, Total: total, Page: page, TotalPages: pages, Query: q, UnreadOnly: unreadOnly, Error: c.Query("error")}
	m.renderer.Render(c, http.StatusOK, "messages", data)
}

func (m *Module) startConversation(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	conv, err := m.svc.StartDirect(c.Request.Context(), user.ID, c.PostForm("username"))
	if err != nil {
		c.Redirect(http.StatusFound, "/messages?error="+urlQueryEscape(errorText(err)))
		return
	}
	c.Redirect(http.StatusFound, "/messages/"+conv.ID)
}

func (m *Module) threadPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	viewData, err := m.svc.GetPage(c.Request.Context(), c.Param("id"), user.ID, c.Query("before"), 50)
	if err != nil {
		data := view.BaseData(c, m.settings.SiteInfo(), m.version)
		data.Title = "会话不存在 - " + data.Site.Name
		data.Data = view.ErrorData{Code: 404, Message: "会话不存在", RequestID: c.GetString("request_id")}
		m.renderer.Render(c, http.StatusNotFound, "error", data)
		return
	}
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.Title = "私信 - " + data.Site.Name
	data.Robots = "noindex,nofollow"
	data.Data = struct {
		View  *ConversationView
		Error string
	}{View: viewData, Error: c.Query("error")}
	m.renderer.Render(c, http.StatusOK, "conversation", data)
}

func (m *Module) sendMessage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if _, err := m.svc.Send(c.Request.Context(), c.Param("id"), user.ID, c.PostForm("raw")); err != nil {
		c.Redirect(http.StatusFound, "/messages/"+c.Param("id")+"?error="+urlQueryEscape(errorText(err)))
		return
	}
	c.Redirect(http.StatusFound, "/messages/"+c.Param("id"))
}

func (m *Module) blockConversation(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	other := m.svc.OtherParticipant(c.Request.Context(), c.Param("id"), user.ID)
	if other == "" {
		c.Redirect(http.StatusFound, "/messages?error="+urlQueryEscape("会话不存在"))
		return
	}
	if _, err := m.svc.ToggleBlock(c.Request.Context(), user.ID, other); err != nil {
		c.Redirect(http.StatusFound, "/messages/"+c.Param("id")+"?error="+urlQueryEscape(errorText(err)))
		return
	}
	c.Redirect(http.StatusFound, "/messages/"+c.Param("id"))
}

func (m *Module) apiList(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	rows, total, err := m.svc.List(c.Request.Context(), user.ID, 20, 0, "", "", false)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"conversations": rows, "total": total})
}

func (m *Module) apiStart(c *gin.Context) {
	var in struct {
		Username string `json:"username"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"body": "请求体格式错误"}))
		return
	}
	user := authn.FromContext(c.Request.Context())
	conv, err := m.svc.StartDirect(c.Request.Context(), user.ID, in.Username)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, conv)
}

func (m *Module) apiGet(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	conv, err := m.svc.Get(c.Request.Context(), c.Param("id"), user.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, conv)
}

func (m *Module) apiSend(c *gin.Context) {
	var in struct {
		Raw string `json:"raw"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"body": "请求体格式错误"}))
		return
	}
	user := authn.FromContext(c.Request.Context())
	msg, err := m.svc.Send(c.Request.Context(), c.Param("id"), user.ID, in.Raw)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, msg)
}

func errorText(err error) string {
	e := httpx.ErrorOf(err)
	if len(e.Fields) > 0 {
		for _, v := range e.Fields {
			return v
		}
	}
	if e.Message != "" {
		return e.Message
	}
	return "操作失败，请稍后重试"
}

func urlQueryEscape(s string) string {
	return url.QueryEscape(s)
}
