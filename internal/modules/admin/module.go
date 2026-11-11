package admin

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/config"
	"goforum/internal/platform/events"
	"goforum/internal/platform/i18n"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
)

type Deps struct {
	Config    *config.Config
	DB        *gorm.DB
	Settings  *settings.Store
	Auth      contracts.Authenticator
	Sessions  *session.Store
	Perms     *permission.Registry
	Renderer  *view.Renderer
	Registry  adminui.Registry
	Audit     *audit.Service
	I18n      *i18n.Bundle
	Version   string
	StartedAt time.Time
	AdsCache  AdsCacheReporter
	Log       *slog.Logger
}

// AdsCacheReporter exposes the ad rotation cache state for the system page.
type AdsCacheReporter interface {
	CacheStats() (rows int, age time.Duration, warm bool)
}

type Module struct {
	deps Deps
}

func New(deps Deps) *Module { return &Module{deps: deps} }

func (m *Module) SetRegistry(r adminui.Registry) { m.deps.Registry = r }

func (m *Module) Name() string                    { return "admin" }
func (m *Module) DependsOn() []string             { return []string{"auth"} }
func (m *Module) Models() []any                   { return nil }
func (m *Module) Subscribe(events.Bus) error      { return nil }
func (m *Module) Settings() []settings.Definition { return nil }

func Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "admin.access", Label: "进入后台", Group: "后台"},
		{Key: "settings.read", Label: "查看站点设置", Group: "后台"},
		{Key: "settings.write", Label: "修改站点设置", Group: "后台"},
		{Key: "settings.site", Label: "站点与外观设置", Group: "后台"},
		{Key: "settings.registration", Label: "注册策略设置", Group: "后台"},
		{Key: "settings.seo", Label: "SEO 设置", Group: "后台"},
		{Key: "settings.integration", Label: "集成设置", Group: "后台"},
		{Key: "settings.mail", Label: "邮件设置", Group: "后台"},
		{Key: "settings.payment", Label: "支付设置", Group: "后台"},
		{Key: "admin.system", Label: "系统管理", Group: "后台"},
	}
}

func (m *Module) Permissions() []permission.Definition { return Permissions() }

func (m *Module) Admin() adminui.Contribution {
	return adminui.Contribution{
		Nav: []adminui.NavItem{
			{Group: "overview", Label: "仪表盘", URL: "/admin", Permission: "admin.access", Order: 0},
			{Group: "users", Label: "用户组", URL: "/admin/groups", Permission: "group.manage", Order: 20},
			{Group: "site", Label: "站点设置", URL: "/admin/settings", Permission: "settings.read", Order: 10},
			{Group: "system", Label: "审计日志", URL: "/admin/audit", Permission: "moderation.audit.view", Order: 20},
			{Group: "system", Label: "系统信息", URL: "/admin/system", Permission: "admin.system", Order: 5},
			{Group: "system", Label: "模块清单", URL: "/admin/modules", Permission: "admin.system", Order: 10},
		},
	}
}

func (m *Module) middlewareDeps() middleware.Deps {
	return middleware.Deps{
		Config:   m.deps.Config,
		Sessions: m.deps.Sessions,
		Users:    contracts.AsLoader(m.deps.Auth),
		Log:      m.deps.Log,
		Renderer: m.deps.Renderer,
		Settings: m.deps.Settings,
		Version:  m.deps.Version,
	}
}

func (m *Module) Routes(web, _ *gin.RouterGroup) {
	md := m.middlewareDeps()
	auth := []gin.HandlerFunc{
		middleware.RequireLogin(md),
		middleware.RequirePermission(md, "admin.access"),
	}

	web.GET("/admin/login", m.loginPage)
	web.POST("/admin/login", middleware.RateLimit(md, "admin_login", 5, 15*time.Minute), m.loginPost)
	web.POST("/admin/logout", m.logout)

	web.GET("/admin", append(auth, m.dashboard)...)
	web.GET("/admin/modules", append(auth, middleware.RequirePermission(md, "admin.system"), m.modulesPage)...)
	web.GET("/admin/system", append(auth, middleware.RequirePermission(md, "admin.system"), m.systemPage)...)
	web.GET("/admin/settings", append(auth, middleware.RequirePermission(md, "settings.read"), m.settingsPage)...)
	web.POST("/admin/settings", append(auth, middleware.RequirePermission(md, "settings.write"), m.settingsSave)...)
	web.GET("/admin/users", append(auth, middleware.RequirePermission(md, "admin.user.view"), m.usersPage)...)
	web.POST("/admin/users/bulk", append(auth, middleware.RequirePermission(md, "user.ban"), m.usersBulk)...)
	web.POST("/admin/users/:id/status", append(auth, middleware.RequirePermission(md, "user.ban"), m.userStatus)...)
	web.GET("/admin/users/:id", append(auth, middleware.RequirePermission(md, "admin.user.view"), m.userDetail)...)
	web.POST("/admin/users/:id/groups", append(auth, middleware.RequirePermission(md, "group.manage"), m.userGroupsSave)...)
	web.POST("/admin/users/:id/trust", append(auth, middleware.RequirePermission(md, "user.ban"), m.userTrustSave)...)

	web.GET("/admin/groups", append(auth, middleware.RequirePermission(md, "group.manage"), m.groupsPage)...)
	web.POST("/admin/groups", append(auth, middleware.RequirePermission(md, "group.manage"), m.groupCreate)...)
	web.POST("/admin/groups/:id", append(auth, middleware.RequirePermission(md, "group.manage"), m.groupUpdate)...)
	web.POST("/admin/groups/:id/delete", append(auth, middleware.RequirePermission(md, "group.manage"), m.groupDelete)...)

	web.GET("/admin/audit", append(auth, middleware.RequirePermission(md, "moderation.audit.view"), m.auditPage)...)
}
