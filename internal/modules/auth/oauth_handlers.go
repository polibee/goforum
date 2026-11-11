package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/authn"
	"goforum/internal/platform/httpx"
)

// oauthStart kicks off the authorize redirect and plants the state cookie.
func (m *Module) oauthStart(c *gin.Context) {
	provider := c.Param("provider")
	if _, ok := ProviderByKey(provider); !ok {
		m.oauthFail(c, provider, "不支持该登录方式")
		return
	}
	link := c.Query("link") == "1"
	if link && authn.FromContext(c.Request.Context()) == nil {
		c.Redirect(http.StatusFound, "/login?next="+c.Request.URL.RequestURI())
		return
	}
	cfg, err := m.svc.providerConfig(provider)
	if err != nil {
		m.oauthFail(c, provider, errMessage(err))
		return
	}
	next := safeNext(c.Query("next"))
	state, cookie, err := NewOAuthState(provider, next, link)
	if err != nil {
		m.oauthFail(c, provider, "无法发起第三方登录")
		return
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(OAuthRedirectCookieName(provider), cookie, 600, "/", "", c.Request.TLS != nil, true)
	c.Redirect(http.StatusFound, AuthCodeURL(cfg, state.Nonce))
}

// oauthCallback validates state, exchanges the code and signs the user in.
func (m *Module) oauthCallback(c *gin.Context) {
	provider := c.Param("provider")
	if _, ok := ProviderByKey(provider); !ok {
		m.oauthFail(c, provider, "不支持该登录方式")
		return
	}
	cookieName := OAuthRedirectCookieName(provider)
	rawState, err := c.Cookie(cookieName)
	c.SetCookie(cookieName, "", -1, "/", "", c.Request.TLS != nil, true)
	if err != nil || rawState == "" {
		m.oauthFail(c, provider, "登录状态已过期，请重新发起登录")
		return
	}
	state, err := ParseOAuthState(rawState)
	if err != nil || state.Provider != provider {
		m.oauthFail(c, provider, "登录状态校验失败，请重新发起登录")
		return
	}
	if providerErr := strings.TrimSpace(c.Query("error")); providerErr != "" {
		desc := strings.TrimSpace(c.Query("error_description"))
		if desc == "" {
			desc = providerErr
		}
		m.oauthFail(c, provider, "第三方授权未完成："+desc)
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		m.oauthFail(c, provider, "缺少授权码")
		return
	}

	profile, err := m.svc.FetchOAuthProfile(c.Request.Context(), provider, code)
	if err != nil {
		m.oauthFail(c, provider, errMessage(err))
		return
	}

	if state.Link {
		principal := authn.FromContext(c.Request.Context())
		if principal == nil {
			c.Redirect(http.StatusFound, "/login")
			return
		}
		if err := m.svc.LinkOAuthAccount(c.Request.Context(), principal.ID, provider, profile); err != nil {
			m.oauthFail(c, provider, errMessage(err))
			return
		}
		c.Redirect(http.StatusFound, "/settings/security?linked="+provider)
		return
	}

	user, _, err := m.svc.OAuthLogin(c.Request.Context(), provider, profile)
	if err != nil {
		m.oauthFail(c, provider, errMessage(err))
		return
	}
	if handled, err := m.finishLogin(c, toView(user), state.Next); err != nil {
		m.oauthFail(c, provider, "登录失败，请稍后重试")
		return
	} else if handled {
		return
	}
	if _, err := m.createSession(c, user.ID); err != nil {
		m.oauthFail(c, provider, "登录失败，请稍后重试")
		return
	}
	c.Redirect(http.StatusFound, safeNext(state.Next))
}

// oauthUnlink detaches a provider identity from the signed-in account.
func (m *Module) oauthUnlink(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	provider := c.Param("provider")
	if _, ok := ProviderByKey(provider); !ok {
		c.Redirect(http.StatusFound, "/settings/security")
		return
	}
	if err := m.svc.UnlinkOAuthAccount(c.Request.Context(), principal.ID, provider); err != nil {
		c.Redirect(http.StatusFound, "/settings/security?link_error="+errMessage(err))
		return
	}
	c.Redirect(http.StatusFound, "/settings/security?unlinked="+provider)
}

// oauthFail renders a friendly notice instead of a raw error page.
func (m *Module) oauthFail(c *gin.Context, provider, message string) {
	name := providerName(provider)
	m.notice(c, http.StatusBadRequest, name+" 登录失败", message, "/login", "返回登录", "")
}

func errMessage(err error) string {
	e := httpx.ErrorOf(err)
	if e.Message != "" {
		return e.Message
	}
	return "操作失败，请稍后重试"
}

// oauthSecurityData adds linked identity state to the security page payload.
type linkedProvider struct {
	Key      string
	Name     string
	Color    string
	Icon     string
	Linked   bool
	Username string
	Email    string
	Ready    bool
	Enabled  bool
}

func (m *Module) linkedProviders(c *gin.Context, userID string) []linkedProvider {
	linked, _ := m.svc.LinkedProviders(c.Request.Context(), userID)
	statuses := m.svc.OAuthProviders()
	out := make([]linkedProvider, 0, len(statuses))
	for _, st := range statuses {
		row := linkedProvider{
			Key: st.Key, Name: st.Name, Color: st.Color, Icon: st.Icon,
			Ready: st.Ready, Enabled: st.Enabled,
		}
		if identity, ok := linked[st.Key]; ok {
			row.Linked = true
			row.Username = identity.Username
			row.Email = identity.Email
		}
		out = append(out, row)
	}
	return out
}

// oauthNoticeQuery maps security-page query flags to a single banner.
func oauthNoticeQuery(c *gin.Context) (kind, message string) {
	if v := c.Query("linked"); v != "" {
		return "success", "已绑定 " + providerName(v) + " 账号"
	}
	if v := c.Query("unlinked"); v != "" {
		return "success", "已解除 " + providerName(v) + " 绑定"
	}
	if v := c.Query("link_error"); v != "" {
		return "danger", v
	}
	return "", ""
}
