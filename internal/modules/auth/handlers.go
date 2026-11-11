package auth

import (
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/i18n"
	"goforum/internal/platform/session"
	"goforum/internal/platform/view"
)

type loginPageData struct {
	Login                string
	Error                string
	Action               string
	Next                 string
	Providers            []ProviderStatus
	HidePassword         bool
	PasswordResetEnabled bool
}

type registerPageData struct {
	Username    string
	Email       string
	InviteCode  string
	Error       string
	Providers   []ProviderStatus
	VerifyEmail bool
}

type noticeData struct {
	Title     string
	Message   string
	Link      string
	LinkLabel string
	DevURL    string
}

type forgotPageData struct {
	Email    string
	Error    string
	Disabled bool
}

type resetPageData struct {
	Token string
	Error string
}

type profilePageData struct {
	User  *contracts.UserView
	Bio   string
	Saved bool
	Error string
}

type securityPageData struct {
	Saved                    bool
	EmailSent                bool
	CodeSent                 bool
	Error                    string
	Email                    string
	EmailVerified            bool
	Sessions                 []session.Session
	CurrentSessionID         string
	LinkedProviders          []linkedProvider
	Notice                   string
	NoticeKind               string
	PasswordChangeEnabled    bool
	PasswordChangeVerify     bool
	EmailChangeEnabled       bool
	EmailChangeVerify        bool
	EmailVerificationEnabled bool
	PasswordMin              int
	ShowTOTP                 bool
	TOTPEnabled              bool
	TOTPPolicyRequired       bool
	TOTPRecoveryRemaining    int
}

type twoFactorPageData struct {
	Username string
	Error    string
}

type totpSetupPageData struct {
	Secret         string
	URI            string
	QRDataURI      template.URL
	Error          string
	RecoveryCodes  []string
	Enabled        bool
	PolicyRequired bool
}

func (m *Module) page(c *gin.Context, title string) view.Data {
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.SetPageTitle(title, m.settings.GetString("seo.title_template"))
	return data
}

func (m *Module) loginPage(c *gin.Context) {
	if authn.FromContext(c.Request.Context()) != nil {
		c.Redirect(http.StatusFound, "/")
		return
	}
	data := m.page(c, "登录")
	data.Data = m.loginData(c, "", "", c.Query("next"))
	m.renderer.Render(c, http.StatusOK, "login", data)
}

func (m *Module) loginPost(c *gin.Context) {
	login := strings.TrimSpace(c.PostForm("login"))
	plain := c.PostForm("password")
	next := c.PostForm("next")
	if next == "" {
		next = c.Query("next")
	}

	if login == "" || plain == "" {
		m.renderLoginError(c, login, next, "请输入用户名与密码", http.StatusUnprocessableEntity)
		return
	}
	user, err := m.svc.Authenticate(c.Request.Context(), login, plain)
	if err != nil {
		m.renderLoginError(c, login, next, errorMessage(err), http.StatusForbidden)
		return
	}
	if handled, err := m.finishLogin(c, user, next); err != nil {
		m.renderLoginError(c, login, next, "登录失败，请稍后重试", http.StatusInternalServerError)
		return
	} else if handled {
		return
	}
	if _, err := m.createSession(c, user.ID); err != nil {
		m.renderLoginError(c, login, next, "登录失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	c.Redirect(http.StatusFound, safeNext(next))
}

// finishLogin applies the TOTP policy after the password step. It returns true
// when it has already redirected the response.
func (m *Module) finishLogin(c *gin.Context, user *contracts.UserView, next string) (bool, error) {
	switch m.svc.TwoFactorMode(c.Request.Context(), user) {
	case "verify":
		raw, err := m.svc.CreateLoginChallenge(c.Request.Context(), user.ID, safeNext(next))
		if err != nil {
			return true, err
		}
		session.SetTwoFactorCookie(c, m.cfg, raw, 5*time.Minute)
		c.Redirect(http.StatusFound, "/login/2fa")
		return true, nil
	case "setup":
		if _, err := m.createSession(c, user.ID); err != nil {
			return true, err
		}
		c.Redirect(http.StatusFound, "/settings/security/2fa/setup")
		return true, nil
	}
	return false, nil
}

func (m *Module) renderLoginError(c *gin.Context, login, next, message string, status int) {
	data := m.page(c, "登录")
	data.Data = m.loginData(c, login, message, next)
	m.renderer.Render(c, status, "login", data)
}

func (m *Module) login2FAPage(c *gin.Context) {
	if authn.FromContext(c.Request.Context()) != nil {
		c.Redirect(http.StatusFound, "/")
		return
	}
	raw, err := c.Cookie(session.TwoFactorCookie)
	if err != nil || raw == "" {
		m.notice(c, http.StatusBadRequest, "验证已过期", "请重新登录。", "/login", "返回登录", "")
		return
	}
	user, err := m.svc.LoginChallengeUser(c.Request.Context(), raw)
	if err != nil {
		session.ClearTwoFactorCookie(c, m.cfg)
		m.notice(c, http.StatusBadRequest, "验证已过期", "请重新登录。", "/login", "返回登录", "")
		return
	}
	data := m.page(c, "两步验证")
	data.Robots = "noindex,nofollow"
	data.Data = twoFactorPageData{Username: user.Username}
	m.renderer.Render(c, http.StatusOK, "login_2fa", data)
}

func (m *Module) login2FAPost(c *gin.Context) {
	raw, err := c.Cookie(session.TwoFactorCookie)
	if err != nil || raw == "" {
		m.notice(c, http.StatusBadRequest, "验证已过期", "请重新登录。", "/login", "返回登录", "")
		return
	}
	code := strings.TrimSpace(c.PostForm("code"))
	userID, next, err := m.svc.VerifyLoginChallenge(c.Request.Context(), raw, code)
	if err != nil {
		user, _ := m.svc.LoginChallengeUser(c.Request.Context(), raw)
		data := m.page(c, "两步验证")
		data.Robots = "noindex,nofollow"
		username := ""
		if user != nil {
			username = user.Username
		}
		data.Data = twoFactorPageData{Username: username, Error: errorMessage(err)}
		m.renderer.Render(c, http.StatusUnprocessableEntity, "login_2fa", data)
		return
	}
	session.ClearTwoFactorCookie(c, m.cfg)
	if _, err := m.createSession(c, userID); err != nil {
		m.notice(c, http.StatusInternalServerError, "登录失败", "请稍后重试。", "/login", "返回登录", "")
		return
	}
	c.Redirect(http.StatusFound, safeNext(next))
}

func (m *Module) logout(c *gin.Context) {
	if v, ok := c.Get("session"); ok {
		if sess, ok := v.(*session.Session); ok {
			_ = m.sessions.Delete(c.Request.Context(), sess.ID)
		}
	}
	session.ClearCookie(c, m.cfg)
	c.Redirect(http.StatusFound, "/")
}

func (m *Module) registerPage(c *gin.Context) {
	if authn.FromContext(c.Request.Context()) != nil {
		c.Redirect(http.StatusFound, "/")
		return
	}
	data := m.page(c, "注册")
	rd := registerPageData{InviteCode: c.Query("invite"), VerifyEmail: m.svc.RegistrationVerifyEnabled()}
	rd.Providers = m.svc.EnabledProviders()
	data.Data = rd
	m.renderer.Render(c, http.StatusOK, "register", data)
}

func (m *Module) registerPost(c *gin.Context) {
	username := strings.TrimSpace(c.PostForm("username"))
	email := strings.TrimSpace(c.PostForm("email"))
	plain := c.PostForm("password")
	confirm := c.PostForm("password_confirm")
	inviteCode := strings.TrimSpace(c.PostForm("invite_code"))
	if inviteCode == "" {
		inviteCode = strings.TrimSpace(c.Query("invite"))
	}

	data := m.page(c, "注册")
	if plain != confirm {
		data.Data = registerPageData{Username: username, Email: email, InviteCode: inviteCode, Error: "两次输入的密码不一致", VerifyEmail: m.svc.RegistrationVerifyEnabled()}
		m.renderer.Render(c, http.StatusUnprocessableEntity, "register", data)
		return
	}

	res, err := m.svc.Register(c.Request.Context(), username, email, plain, inviteCode)
	if err != nil {
		data.Data = registerPageData{Username: username, Email: email, InviteCode: inviteCode, Error: errorMessage(err), VerifyEmail: m.svc.RegistrationVerifyEnabled()}
		m.renderer.Render(c, http.StatusUnprocessableEntity, "register", data)
		return
	}

	if res.User.Status == "pending_email" {
		devURL := ""
		if m.cfg != nil && m.cfg.IsDevelopment() && res.VerifyToken != "" {
			devURL = m.svc.link("/verify-email?token=" + res.VerifyToken)
		}
		m.notice(c, http.StatusOK, "验证邮件已发送",
			"我们已向 "+email+" 发送验证邮件，请在 24 小时内完成验证。",
			"/", "返回首页", devURL)
		return
	}
	if res.User.Status == "pending_review" {
		m.notice(c, http.StatusOK, "等待管理员审核",
			"账号已创建，管理员审核通过后即可登录。", "/", "返回首页", "")
		return
	}

	if _, err := m.createSession(c, res.User.ID); err != nil {
		m.notice(c, http.StatusInternalServerError, "注册成功", "账号已创建，请前往登录。", "/login", "去登录", "")
		return
	}
	c.Redirect(http.StatusFound, "/")
}

func (m *Module) verifyEmail(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		m.notice(c, http.StatusBadRequest, "链接无效", "缺少验证令牌。", "/login", "去登录", "")
		return
	}
	if err := m.svc.VerifyEmail(c.Request.Context(), token); err != nil {
		m.notice(c, http.StatusBadRequest, "验证失败", errorMessage(err), "/login", "去登录", "")
		return
	}
	m.notice(c, http.StatusOK, "邮箱验证成功", "你的邮箱已验证，现在可以登录了。", "/login", "去登录", "")
}

func (m *Module) resendVerify(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	_ = m.svc.ResendVerification(c.Request.Context(), email)
	m.notice(c, http.StatusOK, "邮件已发送",
		"如果该邮箱存在且尚未验证，我们已重新发送验证邮件。", "/login", "去登录", "")
}

func (m *Module) forgotPage(c *gin.Context) {
	data := m.page(c, "找回密码")
	data.Data = forgotPageData{Disabled: !m.svc.PasswordResetEnabled()}
	m.renderer.Render(c, http.StatusOK, "forgot", data)
}

func (m *Module) forgotPost(c *gin.Context) {
	if !m.svc.PasswordResetEnabled() {
		m.notice(c, http.StatusForbidden, "密码找回已关闭",
			"站点已关闭自助找回密码，请联系管理员处理。", "/login", "返回登录", "")
		return
	}
	email := strings.TrimSpace(c.PostForm("email"))
	_ = m.svc.RequestPasswordReset(c.Request.Context(), email)
	m.notice(c, http.StatusOK, "邮件已发送",
		"如果该邮箱已注册，我们已发送重置密码链接，请在 1 小时内完成操作。", "/login", "去登录", "")
}

func (m *Module) resetPage(c *gin.Context) {
	if !m.svc.PasswordResetEnabled() {
		m.notice(c, http.StatusForbidden, "密码找回已关闭",
			"站点已关闭自助找回密码，请联系管理员处理。", "/login", "返回登录", "")
		return
	}
	data := m.page(c, "重置密码")
	data.Data = resetPageData{Token: c.Query("token")}
	m.renderer.Render(c, http.StatusOK, "reset", data)
}

func (m *Module) resetPost(c *gin.Context) {
	if !m.svc.PasswordResetEnabled() {
		m.notice(c, http.StatusForbidden, "密码找回已关闭",
			"站点已关闭自助找回密码，请联系管理员处理。", "/login", "返回登录", "")
		return
	}
	token := c.PostForm("token")
	plain := c.PostForm("password")
	confirm := c.PostForm("password_confirm")
	if plain != confirm {
		data := m.page(c, "重置密码")
		data.Data = resetPageData{Token: token, Error: "两次输入的密码不一致"}
		m.renderer.Render(c, http.StatusUnprocessableEntity, "reset", data)
		return
	}
	userID, err := m.svc.ResetPassword(c.Request.Context(), token, plain)
	if err != nil {
		data := m.page(c, "重置密码")
		data.Data = resetPageData{Token: token, Error: errorMessage(err)}
		m.renderer.Render(c, http.StatusBadRequest, "reset", data)
		return
	}
	_ = m.sessions.DeleteByUser(c.Request.Context(), userID)
	m.notice(c, http.StatusOK, "密码已重置", "所有设备已退出登录，请使用新密码登录。", "/login", "去登录", "")
}

func (m *Module) profilePage(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	user, err := m.svc.GetByID(c.Request.Context(), principal.ID)
	if err != nil {
		m.notice(c, http.StatusNotFound, "用户不存在", "无法加载当前账号。", "/", "返回首页", "")
		return
	}
	data := m.page(c, "个人资料")
	data.Data = profilePageData{User: user, Bio: user.Bio, Saved: c.Query("saved") == "1"}
	m.renderer.Render(c, http.StatusOK, "profile", data)
}

func (m *Module) profileSave(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	bio := c.PostForm("bio")
	signature := c.PostForm("signature")
	locale := c.PostForm("locale")
	if _, err := m.svc.UpdateProfile(c.Request.Context(), principal.ID, bio, signature, locale,
		c.PostForm("bio_enabled") == "1", c.PostForm("donate_enabled") != "", c.PostForm("donate_message"), c.PostForm("donate_url"), c.PostForm("cover_url"), c.PostForm("avatar_url")); err != nil {
		user, _ := m.svc.GetByID(c.Request.Context(), principal.ID)
		data := m.page(c, "个人资料")
		data.Data = profilePageData{User: user, Bio: bio, Error: errorMessage(err)}
		m.renderer.Render(c, http.StatusUnprocessableEntity, "profile", data)
		return
	}
	if locale == "" {
		c.SetCookie(i18n.CookieName, "", -1, "/", "", c.Request.TLS != nil, true)
	} else {
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie(i18n.CookieName, locale, 365*24*3600, "/", "", c.Request.TLS != nil, true)
	}
	c.Redirect(http.StatusFound, "/settings/profile?saved=1")
}

func (m *Module) securityData(c *gin.Context, saved, emailSent, codeSent bool) securityPageData {
	principal := authn.FromContext(c.Request.Context())
	currentID := ""
	if v, ok := c.Get("session"); ok {
		if sess, ok := v.(*session.Session); ok {
			currentID = sess.ID
		}
	}
	email := ""
	emailVerified := false
	totpEnabled := false
	if user, err := m.svc.GetByID(c.Request.Context(), principal.ID); err == nil {
		email = user.Email
		emailVerified = user.EmailVerifiedAt != nil
		totpEnabled = user.TOTPEnabled
	}
	passwordMin := int(m.settings.GetInt("site.registration.password_min"))
	if passwordMin <= 0 {
		passwordMin = 10
	}
	sessions, _ := m.sessions.ListByUser(c.Request.Context(), principal.ID)
	kind, notice := oauthNoticeQuery(c)
	return securityPageData{
		Saved: saved, EmailSent: emailSent, CodeSent: codeSent, Email: email, EmailVerified: emailVerified,
		Sessions: sessions, CurrentSessionID: currentID,
		LinkedProviders: m.linkedProviders(c, principal.ID),
		Notice:          notice, NoticeKind: kind,
		PasswordChangeEnabled:    m.svc.PasswordChangeEnabled(),
		PasswordChangeVerify:     m.svc.PasswordChangeVerifyEnabled(),
		EmailChangeEnabled:       m.svc.EmailChangeEnabled(),
		EmailChangeVerify:        m.svc.EmailChangeVerifyEnabled(),
		EmailVerificationEnabled: m.svc.RegistrationVerifyEnabled(),
		PasswordMin:              passwordMin,
		ShowTOTP:                 principal.Can("admin.access"),
		TOTPEnabled:              totpEnabled,
		TOTPPolicyRequired:       m.svc.AdminTOTPRequired(c.Request.Context(), principal.ID, principal.SuperAdmin),
		TOTPRecoveryRemaining:    m.svc.RecoveryCodesRemaining(c.Request.Context(), principal.ID),
	}
}

func (m *Module) securityPage(c *gin.Context) {
	data := m.page(c, "账号安全")
	data.Robots = "noindex,nofollow"
	data.Data = m.securityData(c, c.Query("saved") == "1", c.Query("email_sent") == "1", c.Query("code_sent") == "1")
	m.renderer.Render(c, http.StatusOK, "security", data)
}

func (m *Module) securityEmailSave(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	raw, err := m.svc.RequestEmailChange(c.Request.Context(), principal.ID, c.PostForm("current_password"), c.PostForm("new_email"))
	if err != nil {
		data := m.page(c, "账号安全")
		data.Robots = "noindex,nofollow"
		d := m.securityData(c, false, false, false)
		d.Error = errorMessage(err)
		data.Data = d
		m.renderer.Render(c, http.StatusUnprocessableEntity, "security", data)
		return
	}
	if raw == "" {
		c.Redirect(http.StatusFound, "/settings/security?saved=1")
		return
	}
	c.Redirect(http.StatusFound, "/settings/security?email_sent=1")
}

func (m *Module) confirmEmailChange(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	if err := m.svc.ConfirmEmailChange(c.Request.Context(), c.Query("token"), principal.ID); err != nil {
		m.notice(c, http.StatusBadRequest, "邮箱验证失败", errorMessage(err), "/settings/security", "返回账号安全", "")
		return
	}
	c.Redirect(http.StatusFound, "/settings/security?saved=1")
}

func (m *Module) securityRevokeSession(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	_ = m.sessions.Revoke(c.Request.Context(), principal.ID, c.Param("id"))
	c.Redirect(http.StatusFound, "/settings/security?saved=1")
}

func (m *Module) securityRevokeOthers(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	currentID := ""
	if v, ok := c.Get("session"); ok {
		if sess, ok := v.(*session.Session); ok {
			currentID = sess.ID
		}
	}
	_ = m.sessions.DeleteOthers(c.Request.Context(), principal.ID, currentID)
	c.Redirect(http.StatusFound, "/settings/security?saved=1")
}

func (m *Module) securitySave(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	current := c.PostForm("current_password")
	next := c.PostForm("new_password")
	if next != c.PostForm("confirm_password") {
		data := m.page(c, "账号安全")
		data.Robots = "noindex,nofollow"
		d := m.securityData(c, false, false, false)
		d.Error = "两次输入的新密码不一致"
		data.Data = d
		m.renderer.Render(c, http.StatusUnprocessableEntity, "security", data)
		return
	}
	if err := m.svc.ChangePassword(c.Request.Context(), principal.ID, current, next, c.PostForm("code")); err != nil {
		data := m.page(c, "账号安全")
		data.Robots = "noindex,nofollow"
		d := m.securityData(c, false, false, false)
		d.Error = errorMessage(err)
		data.Data = d
		m.renderer.Render(c, http.StatusUnprocessableEntity, "security", data)
		return
	}
	_ = m.sessions.DeleteByUser(c.Request.Context(), principal.ID)
	if _, token, err := m.sessions.Create(c.Request.Context(), principal.ID, c.ClientIP(), c.Request.UserAgent()); err == nil {
		session.SetCookie(c, m.cfg, token, m.sessions.TTL())
	}
	c.Redirect(http.StatusFound, "/settings/security?saved=1")
}

func (m *Module) securityTOTPSetupPage(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	if !principal.Can("admin.access") {
		m.notice(c, http.StatusForbidden, "没有权限", "只有管理员可以启用两步验证。", "/settings/security", "返回账号安全", "")
		return
	}
	user, err := m.svc.GetByID(c.Request.Context(), principal.ID)
	if err != nil {
		m.notice(c, http.StatusNotFound, "用户不存在", "无法加载当前账号。", "/", "返回首页", "")
		return
	}
	if user.TOTPEnabled {
		c.Redirect(http.StatusFound, "/settings/security")
		return
	}
	setup, err := m.svc.EnsureTOTPSetup(c.Request.Context(), principal.ID)
	if err != nil {
		m.notice(c, http.StatusInternalServerError, "无法启用两步验证", errorMessage(err), "/settings/security", "返回账号安全", "")
		return
	}
	data := m.page(c, "两步验证")
	data.Robots = "noindex,nofollow"
	data.Data = totpSetupPageData{
		Secret:         setup.Secret,
		URI:            setup.URI,
		QRDataURI:      template.URL(setup.QRDataURI),
		PolicyRequired: m.svc.AdminTOTPRequired(c.Request.Context(), principal.ID, principal.SuperAdmin),
	}
	m.renderer.Render(c, http.StatusOK, "security_2fa_setup", data)
}

func (m *Module) securityTOTPSetupPost(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	if !principal.Can("admin.access") {
		m.notice(c, http.StatusForbidden, "没有权限", "只有管理员可以启用两步验证。", "/settings/security", "返回账号安全", "")
		return
	}
	codes, err := m.svc.EnableTOTP(c.Request.Context(), principal.ID, c.PostForm("code"))
	if err != nil {
		setup, setupErr := m.svc.EnsureTOTPSetup(c.Request.Context(), principal.ID)
		data := m.page(c, "两步验证")
		data.Robots = "noindex,nofollow"
		d := totpSetupPageData{Error: errorMessage(err)}
		if setupErr == nil {
			d.Secret, d.URI, d.QRDataURI = setup.Secret, setup.URI, template.URL(setup.QRDataURI)
		}
		d.PolicyRequired = m.svc.AdminTOTPRequired(c.Request.Context(), principal.ID, principal.SuperAdmin)
		data.Data = d
		m.renderer.Render(c, http.StatusUnprocessableEntity, "security_2fa_setup", data)
		return
	}
	data := m.page(c, "两步验证")
	data.Robots = "noindex,nofollow"
	data.Data = totpSetupPageData{
		Enabled:        true,
		RecoveryCodes:  codes,
		PolicyRequired: m.svc.AdminTOTPRequired(c.Request.Context(), principal.ID, principal.SuperAdmin),
	}
	m.renderer.Render(c, http.StatusOK, "security_2fa_setup", data)
}

func (m *Module) securityTOTPDisable(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	err := m.svc.DisableTOTP(c.Request.Context(), principal.ID, c.PostForm("current_password"), c.PostForm("code"))
	if err != nil {
		data := m.page(c, "账号安全")
		data.Robots = "noindex,nofollow"
		d := m.securityData(c, false, false, false)
		d.Error = errorMessage(err)
		data.Data = d
		m.renderer.Render(c, http.StatusUnprocessableEntity, "security", data)
		return
	}
	c.Redirect(http.StatusFound, "/settings/security?saved=1")
}

// securityPasswordCode sends the short-lived email code required when the
// site enables additional verification for password changes.
func (m *Module) securityPasswordCode(c *gin.Context) {
	principal := authn.FromContext(c.Request.Context())
	if _, err := m.svc.RequestPasswordChangeCode(c.Request.Context(), principal.ID); err != nil {
		data := m.page(c, "账号安全")
		data.Robots = "noindex,nofollow"
		d := m.securityData(c, false, false, false)
		d.Error = errorMessage(err)
		data.Data = d
		m.renderer.Render(c, http.StatusUnprocessableEntity, "security", data)
		return
	}
	c.Redirect(http.StatusFound, "/settings/security?code_sent=1")
}

func (m *Module) createSession(c *gin.Context, userID string) (*session.Session, error) {
	sess, token, err := m.sessions.Create(c.Request.Context(), userID, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		return nil, err
	}
	session.SetCookie(c, m.cfg, token, m.sessions.TTL())
	return sess, nil
}

func (m *Module) notice(c *gin.Context, status int, title, message, link, linkLabel, devURL string) {
	data := m.page(c, title)
	data.Data = noticeData{Title: title, Message: message, Link: link, LinkLabel: linkLabel, DevURL: devURL}
	m.renderer.Render(c, status, "notice", data)
}

// safeNext only allows same-site paths. Browsers normalize a backslash to a
// slash, so "/\evil.com" would leave the site if only "//" were rejected.
func safeNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	if hasUnsafePathRune(next) {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	if strings.HasPrefix(next, "/login") || strings.HasPrefix(next, "/register") {
		return "/"
	}
	return next
}

func errorMessage(err error) string {
	e := httpx.ErrorOf(err)
	if len(e.Fields) > 0 {
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, e.Fields[k])
		}
		return strings.Join(parts, "；")
	}
	if e.Message != "" {
		return e.Message
	}
	return "操作失败，请稍后重试"
}

// loginData assembles the login page payload including enabled providers.
func (m *Module) loginData(c *gin.Context, login, message, next string) loginPageData {
	return loginPageData{
		Login:                login,
		Error:                message,
		Action:               "/login",
		Next:                 next,
		Providers:            m.svc.EnabledProviders(),
		HidePassword:         m.settings.GetBool("oauth.hide_password_form"),
		PasswordResetEnabled: m.svc.PasswordResetEnabled(),
	}
}

// hasUnsafePathRune reports whether a redirect target contains characters that
// browsers normalize into a scheme or host separator (backslash, CR, LF).
func hasUnsafePathRune(p string) bool {
	for _, r := range p {
		if r == 0x5c || r == '\r' || r == '\n' {
			return true
		}
	}
	return false
}
