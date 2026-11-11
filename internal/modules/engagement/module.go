package engagement

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	cmod "goforum/internal/modules/community/contracts"
	"goforum/internal/platform/adminui"
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
}

func New(db *gorm.DB, svc *Service) *Module { return &Module{db: db, svc: svc} }

func (m *Module) Configure(content cmod.Reader, bus events.Bus, renderer *view.Renderer, st *settings.Store, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, version string) {
	m.svc.Configure(content, bus)
	m.renderer = renderer
	m.settings = st
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.version = version
}

func (m *Module) Name() string                    { return "engagement" }
func (m *Module) DependsOn() []string             { return []string{"auth", "community"} }
func (m *Module) Models() []any                   { return Models() }
func (m *Module) Subscribe(events.Bus) error      { return nil }
func (m *Module) Settings() []settings.Definition { return nil }
func (m *Module) Admin() adminui.Contribution     { return adminui.Contribution{} }

func (m *Module) Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "reaction.create", Label: "点赞", Group: "互动"},
		{Key: "bookmark.create", Label: "收藏", Group: "互动"},
		{Key: "subscription.create", Label: "关注", Group: "互动"},
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
	reactionPerm := middleware.RequirePermission(m.md(), "reaction.create")
	bookmarkPerm := middleware.RequirePermission(m.md(), "bookmark.create")
	subscribePerm := middleware.RequirePermission(m.md(), "subscription.create")

	web.GET("/bookmarks", login, m.bookmarksPage)
	web.GET("/following", login, m.followingPage)

	api.POST("/reactions", login, reactionPerm, m.toggleReaction)
	api.POST("/bookmarks", login, bookmarkPerm, m.toggleBookmark)
	api.POST("/subscriptions", login, subscribePerm, m.toggleSubscription)
	api.GET("/engagement/state", login, m.state)
}

func (m *Module) bookmarksPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const perPage = 20
	rows, total, err := m.svc.ListBookmarks(c.Request.Context(), user.ID, perPage, (page-1)*perPage)
	if err != nil {
		m.log.Error("list bookmarks failed", "error", err)
	}
	pages := int((total + perPage - 1) / perPage)
	if pages < 1 {
		pages = 1
	}
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.Title = "收藏 - " + data.Site.Name
	data.Robots = "noindex,nofollow"
	data.Data = struct {
		Rows       []BookmarkRow
		Total      int64
		Page       int
		TotalPages int
	}{Rows: rows, Total: total, Page: page, TotalPages: pages}
	m.renderer.Render(c, http.StatusOK, "bookmarks", data)
}

func (m *Module) followingPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	tab := c.Query("type")
	switch tab {
	case "topic", "user", "category", "tag":
	default:
		tab = "user"
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const perPage = 20
	rows, total, err := m.svc.ListSubscriptions(c.Request.Context(), user.ID, tab, perPage, (page-1)*perPage)
	if err != nil {
		m.log.Error("list subscriptions failed", "error", err)
	}
	pages := int((total + perPage - 1) / perPage)
	if pages < 1 {
		pages = 1
	}
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.Title = "我的关注 - " + data.Site.Name
	data.Robots = "noindex,nofollow"
	data.Data = struct {
		Rows       []SubscriptionRow
		Total      int64
		Tab        string
		Page       int
		TotalPages int
		BasePath   string
	}{Rows: rows, Total: total, Tab: tab, Page: page, TotalPages: pages, BasePath: "/following?type=" + tab}
	m.renderer.Render(c, http.StatusOK, "following", data)
}

func (m *Module) toggleReaction(c *gin.Context) {
	var in struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
		Reaction   string `json:"reaction"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"body": "请求体格式错误"}))
		return
	}
	user := authn.FromContext(c.Request.Context())
	active, count, err := m.svc.ToggleReaction(c.Request.Context(), user.ID, in.TargetType, in.TargetID, in.Reaction)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"active": active, "count": count})
}

func (m *Module) toggleBookmark(c *gin.Context) {
	var in struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"body": "请求体格式错误"}))
		return
	}
	user := authn.FromContext(c.Request.Context())
	active, count, err := m.svc.ToggleBookmark(c.Request.Context(), user.ID, in.TargetType, in.TargetID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"active": active, "count": count})
}

func (m *Module) toggleSubscription(c *gin.Context) {
	var in struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"body": "请求体格式错误"}))
		return
	}
	user := authn.FromContext(c.Request.Context())
	following, err := m.svc.ToggleSubscription(c.Request.Context(), user.ID, in.TargetType, in.TargetID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"following": following})
}

func (m *Module) state(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	topicID := c.Query("topic_id")
	var postIDs []string
	if raw := strings.TrimSpace(c.Query("post_ids")); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				postIDs = append(postIDs, id)
			}
		}
	}
	liked, bookmarked, following, err := m.svc.TopicState(c.Request.Context(), user.ID, topicID, postIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	likedList := make([]string, 0, len(liked))
	for id := range liked {
		likedList = append(likedList, id)
	}
	httpx.OK(c, gin.H{"liked": likedList, "bookmarked": bookmarked, "following": following})
}
