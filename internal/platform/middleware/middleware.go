package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"goforum/internal/platform/authn"
	"goforum/internal/platform/config"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/id"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
)

type Deps struct {
	Config   *config.Config
	Sessions *session.Store
	Users    authn.Loader
	Log      *slog.Logger
	Renderer *view.Renderer
	Settings *settings.Store
	Version  string
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return id.New()
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-ID")
		if rid == "" {
			rid = id.New()
		}
		c.Set("request_id", rid)
		c.Header("X-Request-ID", rid)
		c.Next()
	}
}

func Recover(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				d.Log.Error("panic recovered", "panic", r, "path", c.Request.URL.Path, "request_id", c.GetString("request_id"))
				if !c.Writer.Written() {
					if wantsJSON(c) {
						httpx.Fail(c, httpx.NewError("internal.error", http.StatusInternalServerError, "服务暂时不可用"))
						return
					}
					d.RenderError(c, http.StatusInternalServerError, "服务暂时不可用")
					return
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}

func AccessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		uid := ""
		if p := authn.FromContext(c.Request.Context()); p != nil {
			uid = p.ID
		}
		log.Info("http",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"request_id", c.GetString("request_id"),
			"user_id", uid,
			"ip", c.ClientIP(),
		)
	}
}

func SecurityHeaders(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		nonce := randomToken(18)
		c.Set("nonce", nonce)
		c.Header("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'nonce-"+nonce+"'; "+
				"style-src 'self' 'nonce-"+nonce+"'; style-src-attr 'unsafe-inline'; "+
				"img-src 'self' data: https:; font-src 'self'; connect-src 'self'; "+
				"frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if d.Config != nil && d.Config.Env == "production" {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

func StaticCache() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/static/") {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		}
		c.Next()
	}
}

func Session(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookieName := "gf_session"
		if d.Config != nil && d.Config.Security.SessionCookie != "" {
			cookieName = d.Config.Security.SessionCookie
		}
		token, err := c.Cookie(cookieName)
		if err == nil && token != "" {
			sess, err := d.Sessions.Get(c.Request.Context(), token)
			if err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					d.Log.Warn("session lookup failed", "error", err)
				}
				clearSessionCookie(c, d, cookieName)
			} else {
				c.Set("session", sess)
				principal, err := d.Users.Load(c.Request.Context(), sess.UserID)
				if err != nil {
					d.Log.Warn("user load failed", "user_id", sess.UserID, "error", err)
				} else {
					c.Set("user", principal)
					c.Request = c.Request.WithContext(authn.WithPrincipal(c.Request.Context(), principal))
				}
			}
		}
		c.Next()
	}
}

const csrfCookie = "gf_csrf"

// csrfExempt lists webhook endpoints authenticated by their own signatures.
var csrfExempt = map[string]bool{
	"/payments/nowpayments/notify":  true,
	"/payments/xcash/notify":        true,
	"/payments/creem/notify":        true,
	"/payments/lemonsqueezy/notify": true,
}

func CSRF(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if csrfExempt[c.Request.URL.Path] {
			c.Next()
			return
		}
		var token string
		if v, ok := c.Get("session"); ok {
			if sess, ok := v.(*session.Session); ok {
				token = sess.CSRF
			}
		}
		if token == "" {
			if v, err := c.Cookie(csrfCookie); err == nil {
				token = v
			}
			if token == "" {
				token = randomToken(32)
				c.SetSameSite(http.SameSiteLaxMode)
				c.SetCookie(csrfCookie, token, 30*24*3600, "/", "", isSecure(c, d.Config), true)
			}
		}
		c.Set("csrf", token)

		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}

		submitted := c.GetHeader("X-CSRF-Token")
		if submitted == "" {
			submitted = c.PostForm("_csrf")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(submitted)) != 1 {
			if wantsJSON(c) {
				httpx.Fail(c, httpx.NewError("csrf.invalid", http.StatusForbidden, "请求校验失败，请刷新后重试"))
				return
			}
			d.RenderError(c, http.StatusForbidden, "请求校验失败，请刷新后重试")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (d Deps) RenderError(c *gin.Context, status int, message string) {
	if d.Renderer == nil || d.Settings == nil {
		c.String(status, message)
		return
	}
	data := view.BaseData(c, d.Settings.SiteInfo(), d.Version)
	data.Title = fmt.Sprintf("%d - %s", status, data.Site.Name)
	data.Data = view.ErrorData{Code: status, Message: message, RequestID: c.GetString("request_id")}
	d.Renderer.Render(c, status, "error", data)
}

// RequireLoginOnly requires a valid session and an active account, but does
// not enforce the admin TOTP enrollment policy. It is used by the setup routes
// themselves so an admin can finish enrollment.
func RequireLoginOnly(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireLogin(c, d) {
			return
		}
		c.Next()
	}
}

func RequireLogin(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireLogin(c, d) {
			return
		}
		if !requireTOTPSetup(c, d) {
			return
		}
		c.Next()
	}
}

func RequirePermission(d Deps, permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireLogin(c, d) {
			return
		}
		p := authn.FromContext(c.Request.Context())
		if !p.Can(permission) {
			if wantsJSON(c) {
				httpx.Fail(c, httpx.Forbidden("permission.denied", "没有权限执行该操作"))
				return
			}
			d.RenderError(c, http.StatusForbidden, "没有权限访问该页面")
			c.Abort()
			return
		}
		if !requireTOTPSetup(c, d) {
			return
		}
		c.Next()
	}
}

// requireLogin centralizes the session/status checks shared by the login and
// permission middlewares.
func requireLogin(c *gin.Context, d Deps) bool {
	p := authn.FromContext(c.Request.Context())
	if p == nil {
		if wantsJSON(c) {
			httpx.Fail(c, httpx.Unauthorized("请先登录"))
			return false
		}
		next := url.QueryEscape(c.Request.URL.RequestURI())
		c.Redirect(http.StatusFound, "/admin/login?next="+next)
		c.Abort()
		return false
	}
	if p.Status != "" && p.Status != "active" {
		cookieName := "gf_session"
		if d.Config != nil && d.Config.Security.SessionCookie != "" {
			cookieName = d.Config.Security.SessionCookie
		}
		clearSessionCookie(c, d, cookieName)
		if wantsJSON(c) {
			httpx.Fail(c, httpx.Forbidden("user.restricted", "账号已被限制，请联系管理员"))
			return false
		}
		c.Redirect(http.StatusFound, "/login")
		c.Abort()
		return false
	}
	return true
}

// requireTOTPSetup blocks admin routes for accounts the site policy requires
// to enroll in TOTP but who have not done so yet.
func requireTOTPSetup(c *gin.Context, d Deps) bool {
	if isDevelopmentEnv(d.Config) {
		return true
	}
	p := authn.FromContext(c.Request.Context())
	if p == nil || p.TOTPEnabled || d.Settings == nil {
		return true
	}
	required := false
	switch d.Settings.GetString("site.security.admin_2fa_policy") {
	case "admins":
		required = p.Can("admin.access")
	case "superadmin":
		required = p.SuperAdmin
	}
	if !required {
		return true
	}
	if wantsJSON(c) {
		httpx.Fail(c, httpx.NewError("auth.totp_setup_required", http.StatusForbidden, "请先启用两步验证"))
		c.Abort()
		return false
	}
	c.Redirect(http.StatusFound, "/settings/security/2fa/setup")
	c.Abort()
	return false
}

func clearSessionCookie(c *gin.Context, d Deps, name string) {
	secure := false
	if d.Config != nil {
		secure = isSecure(c, d.Config)
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, "", -1, "/", "", secure, true)
}

func isSecure(c *gin.Context, cfg *config.Config) bool {
	if c.Request.TLS != nil {
		return true
	}
	if cfg == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(cfg.HTTP.BaseURL), "https://")
}

// isDevelopmentEnv mirrors config's development/test detection so local work
// does not require TOTP enrollment on every account.
func isDevelopmentEnv(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Env)) {
	case "development", "dev", "", "test":
		return true
	default:
		return false
	}
}

func wantsJSON(c *gin.Context) bool {
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		return true
	}
	return strings.Contains(c.GetHeader("Accept"), "application/json")
}
