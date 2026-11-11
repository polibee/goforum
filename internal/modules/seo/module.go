package seo

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
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
	settings *settings.Store
	renderer *view.Renderer
	auth     contracts.Authenticator
	sessions *session.Store
	cfg      *config.Config
	log      *slog.Logger
	version  string
	registry adminui.Registry
}

func New(db *gorm.DB) *Module { return &Module{db: db} }

func (m *Module) Configure(st *settings.Store, renderer *view.Renderer, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, version string) {
	m.settings = st
	m.renderer = renderer
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.version = version
}

func (m *Module) SetRegistry(r adminui.Registry) { m.registry = r }

func (m *Module) Name() string                         { return "seo" }
func (m *Module) DependsOn() []string                  { return []string{"auth", "community"} }
func (m *Module) Models() []any                        { return nil }
func (m *Module) Permissions() []permission.Definition { return nil }
func (m *Module) Settings() []settings.Definition      { return nil }
func (m *Module) Subscribe(events.Bus) error           { return nil }

func (m *Module) Admin() adminui.Contribution {
	return adminui.Contribution{
		Nav: []adminui.NavItem{
			{Group: "seo", Label: "SEO", URL: "/admin/seo", Permission: "settings.seo", Order: 20},
		},
	}
}

func (m *Module) Routes(web, api *gin.RouterGroup) {
	if m.settings == nil {
		return
	}
	web.GET("/robots.txt", m.robots)
	web.GET("/sitemap.xml", m.sitemapIndex)
	web.GET("/sitemap-topics.xml", m.sitemapTopics)
	web.GET("/sitemap-categories.xml", m.sitemapCategories)
	web.GET("/sitemap-tags.xml", m.sitemapTags)
	web.GET("/sitemap-users.xml", m.sitemapUsers)
	web.GET("/sitemap-pages.xml", m.sitemapPages)
	web.GET("/feed.xml", m.feedAll)
	web.GET("/c/:slug/feed.xml", m.feedCategory)
	web.GET("/tags/:slug/feed.xml", m.feedTag)
	web.GET("/u/:username/feed.xml", m.feedUser)

	md := middleware.Deps{
		Config: m.cfg, Sessions: m.sessions, Users: contracts.AsLoader(m.auth),
		Log: m.log, Renderer: m.renderer, Settings: m.settings, Version: m.version,
	}
	if m.renderer != nil {
		web.GET("/admin/seo", middleware.RequireLogin(md),
			middleware.RequirePermission(md, "settings.seo"), m.adminPage)
		web.POST("/admin/seo/robots", middleware.RequireLogin(md),
			middleware.RequirePermission(md, "settings.seo"), m.saveRobots)
	}
}

func (m *Module) baseURL(c *gin.Context) string {
	if u := strings.TrimRight(m.settings.GetString("site.general.url"), "/"); u != "" {
		return u
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

func (m *Module) sitemapEnabled() bool {
	return m.settings.GetBool("seo.sitemap_enabled")
}

type adminSEOData struct {
	BaseURL       string
	SitemapOn     bool
	TopicCount    int64
	CategoryCount int64
	TagCount      int64
	UserCount     int64
	Robots        string
	RobotsCustom  string
	RobotsExtra   string
	Saved         bool
	Error         string
}

func (m *Module) adminPage(c *gin.Context) {
	ctx := c.Request.Context()
	var topics, categories, tags, users int64
	_ = m.db.WithContext(ctx).Table("topics AS t").Joins("JOIN categories c ON c.id = t.category_id").
		Where("t.deleted_at IS NULL AND t.status IN ? AND t.visibility = ? AND c.visibility = ?",
			[]string{"open", "closed", "archived"}, "public", "public").Count(&topics).Error
	_ = m.db.WithContext(ctx).Table("categories").Where("visibility = ?", "public").Count(&categories).Error
	_ = m.db.WithContext(ctx).Table("tags").Where("topic_count > 0").Count(&tags).Error
	_ = m.db.WithContext(ctx).Table("users").Where("status = ?", "active").Count(&users).Error

	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.SetPageTitle("SEO 诊断", m.settings.GetString("seo.title_template"))
	if p := data.User; p != nil && m.registry != nil {
		data.AdminNav = m.registry.NavFor(p)
		data.AdminGroups = adminui.BuildGroups(data.AdminNav)
	}
	data.Data = adminSEOData{
		BaseURL:       m.baseURL(c),
		SitemapOn:     m.sitemapEnabled(),
		TopicCount:    topics,
		CategoryCount: categories,
		TagCount:      tags,
		UserCount:     users,
		Robots:        m.robotsText(c),
		RobotsCustom:  m.settings.GetString("seo.robots_custom"),
		RobotsExtra:   m.settings.GetString("seo.robots_extra"),
		Saved:         c.Query("saved") == "1",
		Error:         c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "admin_seo", data)
}

func (m *Module) saveRobots(c *gin.Context) {
	actor := ""
	if p := authn.FromContext(c.Request.Context()); p != nil {
		actor = p.ID
	}
	custom := strings.TrimSpace(c.PostForm("robots_custom"))
	extra := strings.TrimSpace(c.PostForm("robots_extra"))
	if len([]rune(custom)) > 20000 || len([]rune(extra)) > 2000 {
		c.Redirect(http.StatusFound, "/admin/seo?error="+url.QueryEscape("robots.txt 内容超出长度限制"))
		return
	}
	if err := m.settings.Set(c.Request.Context(), "seo.robots_custom", custom, actor); err != nil {
		c.Redirect(http.StatusFound, "/admin/seo?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	if err := m.settings.Set(c.Request.Context(), "seo.robots_extra", extra, actor); err != nil {
		c.Redirect(http.StatusFound, "/admin/seo?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/seo?saved=1")
}
