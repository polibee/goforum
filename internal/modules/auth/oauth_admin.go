package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/audit"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/settings"
)

type loginMethodData struct {
	Providers    []loginProviderCard
	Saved        bool
	Error        string
	BaseURL      string
	HidePassword bool
	AutoLink     bool
}

type loginProviderCard struct {
	Key          string
	Name         string
	Color        string
	Icon         string
	Enabled      bool
	ClientID     string
	SecretSet    bool
	RedirectURL  string
	AutoRedirect string
	Scopes       string
	DefaultScope string
	Ready        bool
	DocsURL      string
	DocsLabel    string
}

func (m *Module) loginMethodsPage(c *gin.Context) {
	data := m.adminBase(c, "登录方式")
	data.Data = m.loginMethodData(c, c.Query("saved") == "1", "")
	m.renderer.Render(c, http.StatusOK, "admin_login_methods", data)
}

func (m *Module) loginMethodData(c *gin.Context, saved bool, message string) loginMethodData {
	base := m.publicBase()
	out := loginMethodData{
		Saved:        saved,
		Error:        message,
		BaseURL:      base,
		HidePassword: m.settings.GetBool("oauth.hide_password_form"),
		AutoLink:     m.settings.GetBool("oauth.auto_link_email"),
	}
	statuses := m.svc.OAuthProviders()
	for _, st := range statuses {
		spec, _ := ProviderByKey(st.Key)
		card := loginProviderCard{
			Key: st.Key, Name: st.Name, Color: st.Color, Icon: st.Icon,
			Enabled:      st.Enabled,
			ClientID:     st.ClientID,
			SecretSet:    m.settings.GetString(OAuthSettingsKey(st.Key, "client_secret")) != "",
			RedirectURL:  m.settings.GetString(OAuthSettingsKey(st.Key, "redirect_url")),
			AutoRedirect: base + "/oauth/" + st.Key + "/callback",
			Scopes:       m.settings.GetString(OAuthSettingsKey(st.Key, "scopes")),
			DefaultScope: strings.Join(spec.Scopes, " "),
			Ready:        st.Ready,
		}
		switch st.Key {
		case "google":
			card.DocsURL = "https://console.cloud.google.com/apis/credentials"
			card.DocsLabel = "Google Cloud Console"
		case "github":
			card.DocsURL = "https://github.com/settings/developers"
			card.DocsLabel = "GitHub Developer Settings"
		}
		out.Providers = append(out.Providers, card)
	}
	return out
}

// loginMethodsSave persists the OAuth provider settings from the dedicated page.
func (m *Module) loginMethodsSave(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		m.renderLoginMethodError(c, "表单解析失败")
		return
	}
	principal := authn.FromContext(c.Request.Context())
	actor := ""
	if principal != nil {
		actor = principal.ID
	}

	values := make(map[string]any)
	var problems []string
	for name, list := range c.Request.PostForm {
		if !strings.HasPrefix(name, "setting.") {
			continue
		}
		key := strings.TrimPrefix(name, "setting.")
		def, ok := m.settings.Def(key)
		if !ok || def.Namespace != "oauth" {
			continue
		}
		raw := ""
		if len(list) > 0 {
			raw = list[len(list)-1]
		}
		switch def.Type {
		case settings.TypeBool:
			values[key] = raw == "true" || raw == "1"
		case settings.TypeSecret:
			if raw == "" || raw == "••••••••" {
				continue
			}
			values[key] = strings.TrimSpace(raw)
		default:
			values[key] = strings.TrimSpace(raw)
		}
	}

	// Unchecked checkboxes are absent from the form, so set them explicitly.
	for _, key := range []string{
		"oauth.google.enabled", "oauth.github.enabled",
		"oauth.hide_password_form", "oauth.auto_link_email",
	} {
		if _, present := values[key]; !present {
			values[key] = false
		}
	}

	// Enabled providers must have a client id and secret.
	for _, provider := range []string{"google", "github"} {
		enabledKey := OAuthSettingsKey(provider, "enabled")
		if on, _ := values[enabledKey].(bool); !on {
			continue
		}
		clientID := strings.TrimSpace(asStringValue(values[OAuthSettingsKey(provider, "client_id")]))
		if clientID == "" {
			clientID = m.settings.GetString(OAuthSettingsKey(provider, "client_id"))
		}
		secret := ""
		if v, ok := values[OAuthSettingsKey(provider, "client_secret")]; ok {
			secret = asStringValue(v)
		} else {
			secret = m.settings.GetString(OAuthSettingsKey(provider, "client_secret"))
		}
		if clientID == "" || secret == "" {
			problems = append(problems, providerName(provider)+" 启用前需填写 Client ID 与 Client Secret")
		}
		redirect := strings.TrimSpace(asStringValue(values[OAuthSettingsKey(provider, "redirect_url")]))
		if redirect != "" && !RedirectAllowlist(redirect, m.publicBase()) {
			problems = append(problems, providerName(provider)+" 的回调地址必须与本站同域")
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		m.renderLoginMethodError(c, strings.Join(problems, "；"))
		return
	}
	if err := m.settings.SetMany(c.Request.Context(), values, actor); err != nil {
		var ve *settings.ValidationError
		if errors.As(err, &ve) {
			fields := make([]string, 0, len(ve.Fields))
			for k, v := range ve.Fields {
				fields = append(fields, k+": "+v)
			}
			sort.Strings(fields)
			m.renderLoginMethodError(c, strings.Join(fields, "；"))
			return
		}
		m.renderLoginMethodError(c, "保存失败，请稍后重试")
		return
	}

	auditValues := make(map[string]any, len(values))
	for k, v := range values {
		if def, ok := m.settings.Def(k); ok && (def.Secret || def.Type == settings.TypeSecret) {
			auditValues[k] = "***"
			continue
		}
		auditValues[k] = v
	}
	if m.audit != nil {
		_ = m.audit.Write(c.Request.Context(), audit.Entry{
			ActorID:    actor,
			Action:     "settings.oauth.update",
			TargetType: "settings",
			IP:         c.ClientIP(),
			RequestID:  c.GetString("request_id"),
			AfterJSON:  auditJSON(auditValues),
		})
	}
	c.Redirect(http.StatusFound, "/admin/auth?saved=1")
}

func (m *Module) renderLoginMethodError(c *gin.Context, message string) {
	data := m.adminBase(c, "登录方式")
	data.Data = m.loginMethodData(c, false, message)
	m.renderer.Render(c, http.StatusUnprocessableEntity, "admin_login_methods", data)
}

func (m *Module) publicBase() string {
	if m.svc != nil && m.svc.baseURL != "" {
		return m.svc.baseURL
	}
	if v := m.settings.GetString("site.general.url"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if m.cfg != nil {
		return strings.TrimRight(m.cfg.HTTP.BaseURL, "/")
	}
	return ""
}

func asStringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// adminAuthRoutes mounts the login-methods page; called from Routes.
func (m *Module) adminAuthRoutes(web *gin.RouterGroup, auth []gin.HandlerFunc) {
	perm := middleware.RequirePermission(m.middlewareDeps(), "settings.integration")
	web.GET("/admin/auth", append(auth, perm, m.loginMethodsPage)...)
	web.POST("/admin/auth/save", append(auth, perm, m.loginMethodsSave)...)
}

func auditJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// keep url import for redirect validation elsewhere in the package
var _ = url.Parse
var _ = context.Background
