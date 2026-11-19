package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"
	"gorm.io/gorm"

	"goforum/internal/platform/httpx"
	"goforum/internal/platform/id"
)

// Provider describes one supported external identity provider.
type Provider struct {
	Key       string
	Name      string
	Color     string
	Icon      string
	Scopes    []string
	AuthURL   string
	TokenURL  string
	UserURL   string
	EmailAPI  string
	Configure func(clientID, secret, redirect string, scopes []string) *oauth2.Config
}

// Providers is the catalog rendered on the login page and the admin form.
var Providers = []Provider{
	{
		Key:      "google",
		Name:     "Google",
		Color:    "#ea4335",
		Icon:     "google",
		Scopes:   []string{"openid", "email", "profile"},
		UserURL:  "https://openidconnect.googleapis.com/v1/userinfo",
		EmailAPI: "https://openidconnect.googleapis.com/v1/userinfo",
		Configure: func(clientID, secret, redirect string, scopes []string) *oauth2.Config {
			return &oauth2.Config{
				ClientID:     clientID,
				ClientSecret: secret,
				RedirectURL:  redirect,
				Scopes:       scopes,
				Endpoint:     google.Endpoint,
			}
		},
	},
	{
		Key:      "github",
		Name:     "GitHub",
		Color:    "#24292f",
		Icon:     "github",
		Scopes:   []string{"read:user", "user:email"},
		UserURL:  "https://api.github.com/user",
		EmailAPI: "https://api.github.com/user/emails",
		Configure: func(clientID, secret, redirect string, scopes []string) *oauth2.Config {
			return &oauth2.Config{
				ClientID:     clientID,
				ClientSecret: secret,
				RedirectURL:  redirect,
				Scopes:       scopes,
				Endpoint:     github.Endpoint,
			}
		},
	},
}

// ProviderByKey looks up a provider from the static catalog.
func ProviderByKey(key string) (Provider, bool) {
	for _, p := range Providers {
		if p.Key == key {
			return p, true
		}
	}
	return Provider{}, false
}

// ProviderStatus is what the login page and admin page need to know.
type ProviderStatus struct {
	Key      string
	Name     string
	Color    string
	Icon     string
	Enabled  bool
	Ready    bool
	ClientID string
	Scopes   string
	Redirect string
}

// OAuthSettingsKey builds the settings key for one provider field.
func OAuthSettingsKey(provider, field string) string {
	return "oauth." + provider + "." + field
}

func (s *Service) providerEnabled(provider string) bool {
	if s.settings == nil {
		return false
	}
	return s.settings.GetBool(OAuthSettingsKey(provider, "enabled"))
}

func (s *Service) providerConfig(provider string) (*oauth2.Config, error) {
	p, ok := ProviderByKey(provider)
	if !ok {
		return nil, httpx.NotFound("oauth.unknown_provider", "不支持该登录方式")
	}
	if s.settings == nil {
		return nil, httpx.NewError("oauth.disabled", 400, "该登录方式未启用")
	}
	if !s.providerEnabled(provider) {
		return nil, httpx.NewError("oauth.disabled", 400, "该登录方式未启用")
	}
	clientID := s.settings.GetString(OAuthSettingsKey(provider, "client_id"))
	secret := s.settings.GetString(OAuthSettingsKey(provider, "client_secret"))
	if clientID == "" || secret == "" {
		return nil, httpx.NewError("oauth.misconfigured", 503, "该登录方式尚未完成配置")
	}
	redirect := s.settings.GetString(OAuthSettingsKey(provider, "redirect_url"))
	if redirect == "" {
		redirect = s.link("/oauth/" + provider + "/callback")
	}
	scopes := p.Scopes
	if custom := s.settings.GetString(OAuthSettingsKey(provider, "scopes")); custom != "" {
		scopes = splitScopes(custom)
	}
	return p.Configure(clientID, secret, redirect, scopes), nil
}

// OAuthProviders returns the status of every supported provider.
func (s *Service) OAuthProviders() []ProviderStatus {
	out := make([]ProviderStatus, 0, len(Providers))
	for _, p := range Providers {
		st := ProviderStatus{
			Key: p.Key, Name: p.Name, Color: p.Color, Icon: p.Icon,
			Scopes:   strings.Join(p.Scopes, " "),
			Redirect: "/oauth/" + p.Key + "/callback",
		}
		if s.settings != nil {
			st.Enabled = s.settings.GetBool(OAuthSettingsKey(p.Key, "enabled"))
			st.ClientID = s.settings.GetString(OAuthSettingsKey(p.Key, "client_id"))
			if v := s.settings.GetString(OAuthSettingsKey(p.Key, "scopes")); v != "" {
				st.Scopes = v
			}
			if v := s.settings.GetString(OAuthSettingsKey(p.Key, "redirect_url")); v != "" {
				st.Redirect = v
			}
			st.Ready = st.Enabled &&
				s.settings.GetString(OAuthSettingsKey(p.Key, "client_secret")) != ""
		}
		out = append(out, st)
	}
	return out
}

// EnabledProviders lists providers usable right now, for the login page.
func (s *Service) EnabledProviders() []ProviderStatus {
	all := s.OAuthProviders()
	out := make([]ProviderStatus, 0, len(all))
	for _, p := range all {
		if p.Ready {
			out = append(out, p)
		}
	}
	return out
}

func splitScopes(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\n'
	})
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// OAuthState is the short-lived CSRF/redirect carrier stored in a signed cookie.
type OAuthState struct {
	Provider string `json:"p"`
	Nonce    string `json:"n"`
	Next     string `json:"x,omitempty"`
	Link     bool   `json:"l,omitempty"`
	// Verifier is the PKCE code_verifier. It stays in the signed cookie and is
	// never placed in the authorize URL.
	Verifier string `json:"v"`
}

// NewOAuthState builds a random nonce, PKCE verifier and cookie payload.
func NewOAuthState(provider, next string, link bool) (OAuthState, string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return OAuthState{}, "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(buf)
	verifier, err := randomOAuthToken(32)
	if err != nil {
		return OAuthState{}, "", err
	}
	state := OAuthState{Provider: provider, Nonce: nonce, Next: next, Link: link, Verifier: verifier}
	raw, err := json.Marshal(state)
	if err != nil {
		return OAuthState{}, "", err
	}
	return state, base64.RawURLEncoding.EncodeToString(raw), nil
}

func randomOAuthToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ParseOAuthState decodes a state payload. It must only be called after the
// payload signature has been verified.
func ParseOAuthState(encoded string) (OAuthState, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return OAuthState{}, err
	}
	var state OAuthState
	if err := json.Unmarshal(raw, &state); err != nil {
		return OAuthState{}, err
	}
	if state.Provider == "" || state.Nonce == "" || state.Verifier == "" {
		return OAuthState{}, errors.New("invalid state")
	}
	return state, nil
}

// SignOAuthState authenticates the payload with APP_KEY so the browser cannot
// rewrite Link/Next. The nonce is still checked against the provider response.
func SignOAuthState(payload, key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("oauth state signing key is not configured")
	}
	return payload + "." + base64.RawURLEncoding.EncodeToString(oauthStateMAC(payload, key)), nil
}

// VerifyOAuthState verifies the HMAC and returns the decoded state.
func VerifyOAuthState(encoded, key string) (OAuthState, error) {
	if strings.TrimSpace(key) == "" {
		return OAuthState{}, errors.New("oauth state signing key is not configured")
	}
	dot := strings.LastIndexByte(encoded, '.')
	if dot <= 0 || dot == len(encoded)-1 {
		return OAuthState{}, errors.New("invalid signed state")
	}
	payload, sig := encoded[:dot], encoded[dot+1:]
	provided, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return OAuthState{}, errors.New("invalid state signature")
	}
	if !hmac.Equal(provided, oauthStateMAC(payload, key)) {
		return OAuthState{}, errors.New("state signature mismatch")
	}
	return ParseOAuthState(payload)
}

func oauthStateMAC(payload, key string) []byte {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("goforum-oauth-state\x00"))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (s *Service) newOAuthState(provider, next string, link bool) (OAuthState, string, error) {
	state, payload, err := NewOAuthState(provider, next, link)
	if err != nil {
		return OAuthState{}, "", err
	}
	signed, err := SignOAuthState(payload, s.secretKey)
	if err != nil {
		return OAuthState{}, "", httpx.NewError("oauth.unavailable", 503, "第三方登录暂不可用，请稍后重试")
	}
	return state, signed, nil
}

func (s *Service) verifyOAuthState(encoded string) (OAuthState, error) {
	return VerifyOAuthState(encoded, s.secretKey)
}

// oauthStateMatches compares the provider callback state with the signed nonce.
func oauthStateMatches(providerState, nonce string) bool {
	return subtle.ConstantTimeCompare([]byte(providerState), []byte(nonce)) == 1
}

// OAuthProfile is the normalized provider payload used to create a local user.
type OAuthProfile struct {
	ProviderID string
	Email      string
	EmailOK    bool
	Username   string
	AvatarURL  string
}

// FetchOAuthProfile exchanges the code and loads the provider profile. The
// verifier is the PKCE code_verifier saved in the signed state cookie.
func (s *Service) FetchOAuthProfile(ctx context.Context, provider, code, verifier string) (*OAuthProfile, error) {
	cfg, err := s.providerConfig(provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(verifier) == "" {
		return nil, httpx.NewError("oauth.state_invalid", 400, "登录状态校验失败，请重新发起登录")
	}
	spec, _ := ProviderByKey(provider)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, httpx.NewError("oauth.exchange_failed", 502, "无法完成第三方登录授权")
	}
	client := cfg.Client(ctx, tok)
	switch provider {
	case "google":
		return fetchGoogleProfile(ctx, client, spec)
	case "github":
		return fetchGitHubProfile(ctx, client, spec)
	}
	return nil, httpx.NewError("oauth.unknown_provider", 400, "不支持该登录方式")
}

func fetchGoogleProfile(ctx context.Context, client *http.Client, spec Provider) (*OAuthProfile, error) {
	var body struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := getJSON(ctx, client, spec.UserURL, &body); err != nil {
		return nil, err
	}
	if body.Sub == "" {
		return nil, httpx.NewError("oauth.invalid_profile", 502, "第三方账号信息不完整")
	}
	return &OAuthProfile{
		ProviderID: body.Sub,
		Email:      strings.TrimSpace(body.Email),
		EmailOK:    body.EmailVerified,
		Username:   body.Name,
		AvatarURL:  body.Picture,
	}, nil
}

func fetchGitHubProfile(ctx context.Context, client *http.Client, spec Provider) (*OAuthProfile, error) {
	var body struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := getJSON(ctx, client, spec.UserURL, &body); err != nil {
		return nil, err
	}
	if body.ID == 0 {
		return nil, httpx.NewError("oauth.invalid_profile", 502, "第三方账号信息不完整")
	}
	// GitHub's /user "email" field is a user-editable public profile field and
	// is NOT verified. It must never be trusted for account linking: doing so
	// lets anyone set a victim's address as their public email and log in as
	// them. Only /user/emails entries with verified=true may set EmailOK.
	profile := &OAuthProfile{
		ProviderID: fmt.Sprintf("%d", body.ID),
		Email:      strings.TrimSpace(body.Email),
		EmailOK:    false,
		Username:   strings.TrimSpace(body.Name),
		AvatarURL:  body.AvatarURL,
	}
	if profile.Username == "" {
		profile.Username = body.Login
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, client, spec.EmailAPI, &emails); err == nil {
		// Prefer the verified primary address, then any verified one.
		for _, e := range emails {
			if e.Primary && e.Verified && e.Email != "" {
				profile.Email = e.Email
				profile.EmailOK = true
				break
			}
		}
		if !profile.EmailOK {
			for _, e := range emails {
				if e.Verified && e.Email != "" {
					profile.Email = e.Email
					profile.EmailOK = true
					break
				}
			}
		}
	}
	if !profile.EmailOK && profile.Email != "" {
		// Keep the address only for display; it cannot be used to link.
		profile.EmailOK = false
	}
	return profile, nil
}

func getJSON(ctx context.Context, client *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GoForum-OAuth")
	resp, err := client.Do(req)
	if err != nil {
		return httpx.NewError("oauth.profile_failed", 502, "无法获取第三方账号信息")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return httpx.NewError("oauth.profile_failed", 502, "无法读取第三方账号信息")
	}
	if resp.StatusCode != http.StatusOK {
		return httpx.NewError("oauth.profile_failed", 502,
			"第三方账号信息请求失败（HTTP "+fmt.Sprint(resp.StatusCode)+"）")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return httpx.NewError("oauth.profile_failed", 502, "第三方账号信息格式异常")
	}
	return nil
}

// OAuthLogin resolves a provider profile to a local user. It links an existing
// identity, otherwise links by verified email, otherwise creates a new account.
func (s *Service) OAuthLogin(ctx context.Context, provider string, profile *OAuthProfile) (*User, bool, error) {
	var identity OAuthIdentity
	err := s.db.WithContext(ctx).
		Where("provider = ? AND provider_user_id = ?", provider, profile.ProviderID).
		First(&identity).Error
	switch {
	case err == nil:
		var u User
		if err := s.db.WithContext(ctx).First(&u, "id = ?", identity.UserID).Error; err != nil {
			return nil, false, httpx.NewError("oauth.identity_orphan", 500, "关联账号不存在")
		}
		s.expireStatus(ctx, &u)
		if err := s.ensureOAuthUsable(&u); err != nil {
			return nil, false, err
		}
		_ = s.touchIdentity(ctx, &identity, profile)
		s.touchLastSeen(ctx, &u)
		return &u, false, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, false, err
	}

	// No stored identity: match an existing local account by verified email.
	// Linking to an existing account by email is opt-in; when disabled, a
	// provider login never touches an existing user.
	autoLink := false
	if s.settings != nil {
		autoLink = s.settings.GetBool("oauth.auto_link_email")
	}
	if autoLink && profile.Email != "" && profile.EmailOK {
		var u User
		err := s.db.WithContext(ctx).
			Where("email_lower = ?", strings.ToLower(profile.Email)).
			First(&u).Error
		if err == nil {
			s.expireStatus(ctx, &u)
			if err := s.ensureOAuthUsable(&u); err != nil {
				return nil, false, err
			}
			if err := s.linkIdentity(ctx, u.ID, provider, profile); err != nil {
				return nil, false, err
			}
			s.touchLastSeen(ctx, &u)
			return &u, false, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, err
		}
	}
	// Without auto-linking, refuse to create a duplicate account for an email
	// that already belongs to a local user. The user must sign in first and
	// bind the provider from the security page.
	if !autoLink && profile.Email != "" && profile.EmailOK {
		var count int64
		if err := s.db.WithContext(ctx).Model(&User{}).
			Where("email_lower = ?", strings.ToLower(profile.Email)).
			Count(&count).Error; err != nil {
			return nil, false, err
		}
		if count > 0 {
			return nil, false, httpx.NewError("oauth.email_registered", 409,
				"该邮箱已注册，请先登录后在安全设置中绑定此第三方账号")
		}
	}

	// Registration rules still apply to first-time OAuth users.
	mode := s.settingString("site.registration.mode", "open")
	if mode == "closed" {
		return nil, false, httpx.Forbidden("registration.closed", "站点已关闭注册")
	}
	if profile.Email == "" {
		return nil, false, httpx.NewError("oauth.email_required",
			422, "第三方账号未提供可用邮箱，无法创建本地账号")
	}
	status := "active"
	if mode == "approval" {
		status = "pending_review"
	}
	username := s.uniqueUsername(ctx, profile.Username, profile.Email, provider)
	u, err := s.createOAuthUser(ctx, username, profile, status)
	if err != nil {
		return nil, false, err
	}
	if err := s.linkIdentity(ctx, u.ID, provider, profile); err != nil {
		return nil, false, err
	}
	s.touchLastSeen(ctx, u)
	return u, true, nil
}

// LinkOAuthAccount attaches a provider identity to the signed-in user.
func (s *Service) LinkOAuthAccount(ctx context.Context, userID, provider string, profile *OAuthProfile) error {
	var existing OAuthIdentity
	err := s.db.WithContext(ctx).
		Where("provider = ? AND provider_user_id = ?", provider, profile.ProviderID).
		First(&existing).Error
	if err == nil {
		if existing.UserID == userID {
			return nil
		}
		return httpx.NewError("oauth.already_linked", 409,
			"该 "+providerName(provider)+" 账号已绑定其他用户")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.linkIdentity(ctx, userID, provider, profile)
}

// UnlinkOAuthAccount removes a provider identity, refusing to strand a
// passwordless account.
func (s *Service) UnlinkOAuthAccount(ctx context.Context, userID, provider string) error {
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", userID).Error; err != nil {
		return err
	}
	var count int64
	_ = s.db.WithContext(ctx).Model(&OAuthIdentity{}).
		Where("user_id = ?", userID).Count(&count).Error
	if count <= 1 && u.PasswordHash == "" {
		return httpx.NewError("oauth.last_login_method", 409,
			"这是唯一的登录方式，请先设置密码")
	}
	return s.db.WithContext(ctx).
		Where("user_id = ? AND provider = ?", userID, provider).
		Delete(&OAuthIdentity{}).Error
}

// LinkedProviders lists provider keys bound to a user.
func (s *Service) LinkedProviders(ctx context.Context, userID string) (map[string]OAuthIdentity, error) {
	var rows []OAuthIdentity
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]OAuthIdentity, len(rows))
	for _, r := range rows {
		out[r.Provider] = r
	}
	return out, nil
}

func (s *Service) ensureOAuthUsable(u *User) error {
	switch u.Status {
	case "active", "pending_email":
		return nil
	case "pending_review":
		return httpx.NewError("auth.pending_review", 403, "账号正在等待管理员审核")
	default:
		return httpx.Forbidden("user.banned", "账号已被限制，请联系管理员")
	}
}

func (s *Service) linkIdentity(ctx context.Context, userID, provider string, profile *OAuthProfile) error {
	row := OAuthIdentity{
		UserID:         userID,
		Provider:       provider,
		ProviderUserID: profile.ProviderID,
		Email:          profile.Email,
		Username:       profile.Username,
		AvatarURL:      profile.AvatarURL,
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

func (s *Service) touchIdentity(ctx context.Context, identity *OAuthIdentity, profile *OAuthProfile) error {
	updates := map[string]any{}
	if profile.Email != "" && profile.Email != identity.Email {
		updates["email"] = profile.Email
	}
	if profile.Username != "" && profile.Username != identity.Username {
		updates["username"] = profile.Username
	}
	if profile.AvatarURL != "" && profile.AvatarURL != identity.AvatarURL {
		updates["avatar_url"] = profile.AvatarURL
	}
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = time.Now().UTC()
	return s.db.WithContext(ctx).Model(identity).Updates(updates).Error
}

func (s *Service) touchLastSeen(ctx context.Context, u *User) {
	now := time.Now().UTC()
	_ = s.db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).Update("last_seen_at", now).Error
	u.LastSeenAt = &now
}

// createOAuthUser creates an account without a password. Email is trusted
// because the provider already verified it.
func (s *Service) createOAuthUser(ctx context.Context, username string, profile *OAuthProfile, status string) (*User, error) {
	now := time.Now().UTC()
	u := &User{
		Username:        username,
		Email:           profile.Email,
		Status:          status,
		EmailVerifiedAt: &now,
		AvatarURL:       profile.AvatarURL,
	}
	if err := s.db.WithContext(ctx).Create(u).Error; err != nil {
		return nil, err
	}
	_ = s.assignDefaultGroup(ctx, u.ID)
	return u, nil
}

var usernameSanitizer = regexp.MustCompile(`[^0-9A-Za-z_\p{Han}\-]+`)

// uniqueUsername derives a valid, unused username from the provider profile.
func (s *Service) uniqueUsername(ctx context.Context, wanted, email, provider string) string {
	candidate := strings.TrimSpace(wanted)
	if candidate == "" && email != "" {
		candidate = strings.SplitN(email, "@", 2)[0]
	}
	if candidate == "" {
		candidate = provider + "_user"
	}
	candidate = strings.Trim(usernameSanitizer.ReplaceAllString(candidate, "_"), "_")
	runes := []rune(candidate)
	if len(runes) > 40 {
		candidate = string(runes[:40])
	}
	if len([]rune(candidate)) < 3 {
		candidate = candidate + "_" + id.New()[:6]
	}
	base := candidate
	for i := 1; i < 100; i++ {
		var n int64
		_ = s.db.WithContext(ctx).Model(&User{}).
			Where("username_lower = ?", strings.ToLower(candidate)).Count(&n).Error
		if n == 0 {
			return candidate
		}
		candidate = base + fmt.Sprintf("_%d", i)
	}
	return base + "_" + id.New()[:6]
}

func providerName(key string) string {
	if p, ok := ProviderByKey(key); ok {
		return p.Name
	}
	return key
}

// OAuthRedirectCookieName is the state cookie for one provider flow.
func OAuthRedirectCookieName(provider string) string {
	sum := sha256.Sum256([]byte("oauth-state:" + provider))
	return "gf_oauth_" + hex.EncodeToString(sum[:6])
}

// AuthCodeURL builds the provider authorize URL for a signed OAuth state.
func AuthCodeURL(cfg *oauth2.Config, state OAuthState) string {
	return cfg.AuthCodeURL(state.Nonce,
		oauth2.AccessTypeOnline,
		oauth2.S256ChallengeOption(state.Verifier),
	)
}

// RedirectAllowlist checks that a configured redirect URL stays on this host.
func RedirectAllowlist(raw, baseURL string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if baseURL == "" {
		return true
	}
	b, err := url.Parse(baseURL)
	if err != nil {
		return true
	}
	return strings.EqualFold(u.Host, b.Host)
}
