package auth

import (
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/modules/auth/contracts"
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
	svc      *Service
	renderer *view.Renderer
	sessions *session.Store
	settings *settings.Store
	cfg      *config.Config
	version  string
	audit    audit.Writer
	registry adminui.Registry
}

func NewModule(svc *Service) *Module { return &Module{svc: svc} }

// SetAudit installs the audit writer used by admin-side setting changes.
func (m *Module) SetAudit(w audit.Writer) { m.audit = w }

// SetRegistry lets admin pages render the shared sidebar navigation.
func (m *Module) SetRegistry(r adminui.Registry) { m.registry = r }

// adminBase attaches the admin nav groups to a page payload.
func (m *Module) adminBase(c *gin.Context, title string) view.Data {
	data := m.page(c, title)
	if p := data.User; p != nil && m.registry != nil {
		data.AdminNav = m.registry.NavFor(p)
		data.AdminGroups = adminui.BuildGroups(data.AdminNav)
	}
	return data
}

func (m *Module) Configure(r *view.Renderer, sessions *session.Store, st *settings.Store, cfg *config.Config, version string) {
	m.renderer = r
	m.sessions = sessions
	m.settings = st
	m.cfg = cfg
	m.version = version
}

func (m *Module) Name() string               { return "auth" }
func (m *Module) DependsOn() []string        { return nil }
func (m *Module) Models() []any              { return Models() }
func (m *Module) Subscribe(events.Bus) error { return nil }
func (m *Module) Settings() []settings.Definition {
	defs := []settings.Definition{
		{Namespace: "oauth", Key: "google.enabled", Group: "integrations", Label: "启用 Google 登录", Type: settings.TypeBool, Default: false, Permission: "settings.integration", Hidden: true},
		{Namespace: "oauth", Key: "google.client_id", Group: "integrations", Label: "Google Client ID", Type: settings.TypeString, Default: "", Permission: "settings.integration", Max: 300, Hidden: true},
		{Namespace: "oauth", Key: "google.client_secret", Group: "integrations", Label: "Google Client Secret", Type: settings.TypeSecret, Default: "", Permission: "settings.integration", Hidden: true},
		{Namespace: "oauth", Key: "google.redirect_url", Group: "integrations", Label: "Google 回调地址（留空自动生成）", Type: settings.TypeURL, Default: "", Permission: "settings.integration", Hidden: true},
		{Namespace: "oauth", Key: "google.scopes", Group: "integrations", Label: "Google Scopes（空格分隔，留空用默认）", Type: settings.TypeString, Default: "", Permission: "settings.integration", Max: 300, Hidden: true},

		{Namespace: "oauth", Key: "github.enabled", Group: "integrations", Label: "启用 GitHub 登录", Type: settings.TypeBool, Default: false, Permission: "settings.integration", Hidden: true},
		{Namespace: "oauth", Key: "github.client_id", Group: "integrations", Label: "GitHub Client ID", Type: settings.TypeString, Default: "", Permission: "settings.integration", Max: 300, Hidden: true},
		{Namespace: "oauth", Key: "github.client_secret", Group: "integrations", Label: "GitHub Client Secret", Type: settings.TypeSecret, Default: "", Permission: "settings.integration", Hidden: true},
		{Namespace: "oauth", Key: "github.redirect_url", Group: "integrations", Label: "GitHub 回调地址（留空自动生成）", Type: settings.TypeURL, Default: "", Permission: "settings.integration", Hidden: true},
		{Namespace: "oauth", Key: "github.scopes", Group: "integrations", Label: "GitHub Scopes（空格分隔，留空用默认）", Type: settings.TypeString, Default: "", Permission: "settings.integration", Max: 300, Hidden: true},

		{Namespace: "oauth", Key: "auto_link_email", Group: "integrations", Label: "邮箱一致时自动绑定已有账号", Type: settings.TypeBool, Default: true, Permission: "settings.integration", Hidden: true},
		{Namespace: "oauth", Key: "hide_password_form", Group: "integrations", Label: "仅启用第三方登录（隐藏密码表单）", Type: settings.TypeBool, Default: false, Permission: "settings.integration", Hidden: true},
	}
	return append(defs, m.trustSettings()...)
}

func (m *Module) trustSettings() []settings.Definition {
	return []settings.Definition{
		{Namespace: "auth", Key: "trust.tl1_days", Group: "trust", Label: "TL1 最少活跃天数", Type: settings.TypeInt, Default: 1, Min: 0, Max: 3650, Permission: "settings.write"},
		{Namespace: "auth", Key: "trust.tl1_posts", Group: "trust", Label: "TL1 最少发帖数", Type: settings.TypeInt, Default: 1, Min: 0, Max: 10000, Permission: "settings.write"},
		{Namespace: "auth", Key: "trust.tl2_days", Group: "trust", Label: "TL2 最少活跃天数", Type: settings.TypeInt, Default: 15, Min: 0, Max: 3650, Permission: "settings.write"},
		{Namespace: "auth", Key: "trust.tl2_posts", Group: "trust", Label: "TL2 最少发帖数", Type: settings.TypeInt, Default: 10, Min: 0, Max: 100000, Permission: "settings.write"},
		{Namespace: "auth", Key: "trust.tl2_likes", Group: "trust", Label: "TL2 最少获赞数", Type: settings.TypeInt, Default: 1, Min: 0, Max: 100000, Permission: "settings.write"},
		{Namespace: "auth", Key: "trust.tl3_days", Group: "trust", Label: "TL3 最少活跃天数", Type: settings.TypeInt, Default: 50, Min: 0, Max: 3650, Permission: "settings.write"},
		{Namespace: "auth", Key: "trust.tl3_posts", Group: "trust", Label: "TL3 最少发帖数", Type: settings.TypeInt, Default: 50, Min: 0, Max: 100000, Permission: "settings.write"},
		{Namespace: "auth", Key: "trust.tl3_likes", Group: "trust", Label: "TL3 最少获赞数", Type: settings.TypeInt, Default: 20, Min: 0, Max: 100000, Permission: "settings.write"},
	}
}

func (m *Module) Admin() adminui.Contribution {
	return adminui.Contribution{
		Nav: []adminui.NavItem{
			{Group: "users", Label: "用户", URL: "/admin/users", Permission: "admin.user.view", Order: 10},
			{Group: "users", Label: "登录方式", URL: "/admin/auth", Permission: "settings.integration", Order: 30},
		},
	}
}

func (m *Module) Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "user.register", Label: "注册", Group: "用户"},
		{Key: "user.profile.edit", Label: "编辑自己的资料", Group: "用户"},
		{Key: "user.ban", Label: "封禁用户", Group: "用户"},
		{Key: "user.suspend", Label: "禁言用户", Group: "用户"},
		{Key: "group.manage", Label: "管理用户组与权限", Group: "用户"},
		{Key: "admin.user.view", Label: "查看用户列表", Group: "后台"},
	}
}

func (m *Module) middlewareDeps() middleware.Deps {
	return middleware.Deps{
		Config:   m.cfg,
		Sessions: m.sessions,
		Users:    contracts.AsLoader(m.svc),
		Log:      nil,
		Renderer: m.renderer,
		Settings: m.settings,
		Version:  m.version,
	}
}

func (m *Module) Routes(web, _ *gin.RouterGroup) {
	if m.renderer == nil {
		return
	}
	md := m.middlewareDeps()
	login := middleware.RequireLogin(md)

	m.adminAuthRoutes(web, []gin.HandlerFunc{
		middleware.RequireLogin(md),
		middleware.RequirePermission(md, "admin.access"),
	})

	web.GET("/login", m.loginPage)
	web.GET("/oauth/:provider", middleware.RateLimit(md, "oauth_start", 30, 10*time.Minute), m.oauthStart)
	web.GET("/oauth/:provider/callback", middleware.RateLimit(md, "oauth_cb", 30, 10*time.Minute), m.oauthCallback)
	web.POST("/login", middleware.RateLimit(md, "login", 10, 5*time.Minute), m.loginPost)
	web.GET("/login/2fa", m.login2FAPage)
	web.POST("/login/2fa", middleware.RateLimit(md, "login_2fa", 10, 5*time.Minute), m.login2FAPost)
	web.POST("/logout", m.logout)

	web.GET("/register", m.registerPage)
	web.POST("/register", middleware.RateLimit(md, "register", 5, time.Hour), m.registerPost)

	web.GET("/verify-email", m.verifyEmail)
	web.POST("/verify-email/resend", middleware.RateLimit(md, "verify_resend", 3, 15*time.Minute), m.resendVerify)

	web.GET("/forgot-password", m.forgotPage)
	web.POST("/forgot-password", middleware.RateLimit(md, "forgot", 5, 15*time.Minute), m.forgotPost)

	web.GET("/reset-password", m.resetPage)
	web.POST("/reset-password", middleware.RateLimit(md, "reset", 10, 15*time.Minute), m.resetPost)

	web.GET("/settings/profile", login, m.profilePage)
	web.POST("/settings/profile", login, middleware.RateLimit(md, "profile_save", 30, 10*time.Minute), m.profileSave)
	web.GET("/settings/security", login, m.securityPage)
	web.POST("/settings/security", login, middleware.RateLimit(md, "security_save", 10, 10*time.Minute), m.securitySave)
	web.POST("/settings/security/password/code", login, middleware.RateLimit(md, "password_code", 5, 10*time.Minute), m.securityPasswordCode)
	// TOTP enrollment must stay reachable while the admin gate is redirecting
	// the user here, so these use the login-only middleware.
	loginOnly := middleware.RequireLoginOnly(md)
	web.GET("/settings/security/2fa/setup", loginOnly, m.securityTOTPSetupPage)
	web.POST("/settings/security/2fa/setup", loginOnly, middleware.RateLimit(md, "totp_setup", 10, 10*time.Minute), m.securityTOTPSetupPost)
	web.POST("/settings/security/2fa/disable", loginOnly, middleware.RateLimit(md, "totp_disable", 5, 10*time.Minute), m.securityTOTPDisable)
	web.GET("/settings/security/email", login, m.securityPage)
	web.POST("/settings/security/email", login, middleware.RateLimit(md, "email_change", 5, 10*time.Minute), m.securityEmailSave)
	web.GET("/settings/email/confirm", login, m.confirmEmailChange)
	web.POST("/settings/security/oauth/:provider/unlink", login, m.oauthUnlink)
	web.POST("/settings/security/sessions/:id/revoke", login, m.securityRevokeSession)
	web.POST("/settings/security/sessions/revoke-others", login, m.securityRevokeOthers)
}
