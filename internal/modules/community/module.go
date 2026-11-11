package community

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/modules/auth/contracts"
	cmod "goforum/internal/modules/community/contracts"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/config"
	"goforum/internal/platform/events"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
)

type Module struct {
	svc        *Service
	renderer   *view.Renderer
	settings   *settings.Store
	auth       contracts.Authenticator
	sessions   *session.Store
	cfg        *config.Config
	log        *slog.Logger
	version    string
	registry   adminui.Registry
	engagement cmod.EngagementReader
	badges     cmod.BadgeReader
	audit      audit.Writer
}

func New(svc *Service) *Module { return &Module{svc: svc} }

func (m *Module) SetEngagement(e cmod.EngagementReader) {
	m.engagement = e
	m.svc.SetEngagement(e)
}
func (m *Module) SetBadges(b cmod.BadgeReader) { m.badges = b }
func (m *Module) SetAudit(w audit.Writer)      { m.audit = w }

func (m *Module) Configure(r *view.Renderer, st *settings.Store, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, version string) {
	m.renderer = r
	m.settings = st
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.version = version
}

func (m *Module) SetRegistry(r adminui.Registry) { m.registry = r }
func (m *Module) SetTips(t cmod.TipService)      { m.svc.SetTips(t) }

func (m *Module) Name() string                   { return "community" }
func (m *Module) DependsOn() []string            { return []string{"auth"} }
func (m *Module) Models() []any                  { return Models() }
func (m *Module) Subscribe(bus events.Bus) error { return m.svc.Subscribe(bus) }

func (m *Module) Admin() adminui.Contribution {
	return adminui.Contribution{
		Nav: []adminui.NavItem{
			{Group: "content", Label: "主题", URL: "/admin/topics", Permission: "admin.content.view", Order: 10},
			{Group: "content", Label: "回复", URL: "/admin/posts", Permission: "admin.content.view", Order: 15},
			{Group: "content", Label: "板块", URL: "/admin/categories", Permission: "category.manage", Order: 20},
			{Group: "content", Label: "标签", URL: "/admin/tags", Permission: "tag.manage", Order: 30},
		},
	}
}

func (m *Module) Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "topic.create", Label: "发布主题", Group: "内容"},
		{Key: "topic.reply", Label: "回复主题", Group: "内容"},
		{Key: "topic.edit_own", Label: "编辑自己的内容", Group: "内容"},
		{Key: "topic.moderate", Label: "管理主题", Group: "内容"},
		{Key: "topic.pin", Label: "置顶主题", Group: "内容"},
		{Key: "topic.feature", Label: "设置精华", Group: "内容"},
		{Key: "category.manage", Label: "管理板块", Group: "内容"},
		{Key: "tag.manage", Label: "管理标签", Group: "内容"},
		{Key: "admin.content.view", Label: "查看内容管理", Group: "后台"},
		{Key: "admin.content.manage", Label: "管理内容（删除、置顶、编辑）", Group: "后台"},
	}
}

func (m *Module) Settings() []settings.Definition {
	return []settings.Definition{
		{Namespace: "community", Key: "topic.title_min", Group: "content", Label: "标题最短长度", Type: settings.TypeInt, Default: 5, Min: 1, Max: 50, Permission: "settings.write"},
		{Namespace: "community", Key: "topic.title_max", Group: "content", Label: "标题最长长度", Type: settings.TypeInt, Default: 120, Min: 20, Max: 255, Permission: "settings.write"},
		{Namespace: "community", Key: "post.max_length", Group: "content", Label: "回复最大长度", Type: settings.TypeInt, Default: 50000, Min: 1000, Max: 200000, Permission: "settings.write"},
		{Namespace: "community", Key: "list.topics_per_page", Group: "content", Label: "每页主题数", Type: settings.TypeInt, Default: 30, Min: 10, Max: 100, Permission: "settings.write"},
		{Namespace: "community", Key: "post.edit_window_minutes", Group: "content", Label: "编辑时限（分钟）", Type: settings.TypeInt, Default: 30, Min: 0, Max: 10080, Permission: "settings.write"},
	}
}

func (m *Module) md() middleware.Deps {
	return middleware.Deps{
		Config:   m.cfg,
		Sessions: m.sessions,
		Users:    contracts.AsLoader(m.auth),
		Log:      m.log,
		Renderer: m.renderer,
		Settings: m.settings,
		Version:  m.version,
	}
}

func (m *Module) Routes(web, api *gin.RouterGroup) {
	if m.renderer == nil {
		return
	}
	md := m.md()
	login := middleware.RequireLogin(md)
	canCreate := middleware.RequirePermission(md, "topic.create")
	canReply := middleware.RequirePermission(md, "topic.reply")

	web.GET("/", m.home)
	web.GET("/search", middleware.RateLimit(md, "search", 60, time.Minute), m.searchPage)
	web.GET("/c/:slug", m.categoryPage)
	web.GET("/tags/:slug", m.tagPage)
	web.GET("/t/:ident", m.topicPage)
	web.GET("/t/:ident/edit", login, m.editTopicPage)
	web.POST("/t/:ident/edit", login, middleware.RateLimit(md, "topic_edit", 20, 10*time.Minute), m.editTopicSave)
	web.GET("/u/:username", m.userPage)

	web.GET("/new", login, canCreate, m.newTopicPage)
	web.POST("/topics", login, canCreate, middleware.RateLimit(md, "topic_create", 20, time.Hour), m.createTopic)
	web.POST("/t/:ident/posts", login, canReply, middleware.RateLimit(md, "post_reply", 60, 10*time.Minute), m.replyPost)
	web.POST("/t/:ident/read", login, m.markRead)
	web.POST("/t/:ident/moderate", login, m.topicModerate)
	web.POST("/t/:ident/accept", login, m.acceptAnswer)
	web.POST("/t/:ident/poll/vote", login, m.votePoll)
	web.POST("/t/:ident/tip", login, m.tipTopic)

	web.GET("/posts/:id/edit", login, m.editPostPage)
	web.POST("/posts/:id/edit", login, middleware.RateLimit(md, "post_edit", 30, 10*time.Minute), m.editPostSave)
	// Legacy /p/:id/edit links redirect to the canonical post path.
	web.GET("/p/:id/edit", m.legacyPostEditRedirect)

	api.GET("/categories", m.apiCategories)
	api.GET("/topics", m.apiTopics)
	api.GET("/users/search", login, m.apiUserSearch)
	api.GET("/users/:username/card", m.apiUserCard)
	api.POST("/topics", login, canCreate, middleware.RateLimit(md, "api_topic_create", 20, time.Hour), m.apiCreateTopic)
	api.GET("/topics/:id", m.apiTopic)
	api.POST("/topics/:id/posts", login, canReply, middleware.RateLimit(md, "api_post_reply", 60, 10*time.Minute), m.apiReply)
	api.PATCH("/posts/:id", login, m.apiEditPost)
	api.POST("/markdown/preview", login, m.apiPreview)
	api.POST("/read-states", login, m.apiReadState)

	adminContent := middleware.RequirePermission(md, "admin.content.view")
	adminContentWrite := middleware.RequirePermission(md, "admin.content.manage")
	adminCategory := middleware.RequirePermission(md, "category.manage")
	adminTag := middleware.RequirePermission(md, "tag.manage")
	adminSystem := middleware.RequirePermission(md, "admin.system")

	web.GET("/admin/topics", login, adminContent, m.adminTopics)
	web.POST("/admin/topics/bulk", login, adminContentWrite, m.adminTopicsBulk)
	web.POST("/admin/topics/:id/action", login, adminContentWrite, m.adminTopicAction)
	web.GET("/admin/topics/:id/edit", login, adminContent, m.adminTopicEditPage)
	web.POST("/admin/topics/:id/edit", login, adminContentWrite, m.adminTopicEditSave)
	web.POST("/admin/search/reindex", login, adminSystem, m.reindexSearch)
	web.GET("/admin/posts", login, adminContent, m.adminPosts)
	web.POST("/admin/posts/bulk", login, adminContentWrite, m.adminPostsBulk)
	web.POST("/admin/posts/:id/action", login, adminContentWrite, m.adminPostAction)
	web.GET("/admin/categories", login, adminCategory, m.adminCategories)
	web.POST("/admin/categories", login, adminCategory, m.adminCategoryCreate)
	web.POST("/admin/categories/:id", login, adminCategory, m.adminCategoryUpdate)
	web.POST("/admin/categories/:id/delete", login, adminCategory, m.adminCategoryDelete)
	web.GET("/admin/tags", login, adminTag, m.adminTags)
	web.POST("/admin/tags", login, adminTag, m.adminTagCreate)
	web.POST("/admin/tags/:id/delete", login, adminTag, m.adminTagDelete)
}
