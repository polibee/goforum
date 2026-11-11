package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
	_ "time/tzdata"

	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/id"
	"goforum/internal/platform/mail"
	"goforum/internal/platform/password"
	"goforum/internal/platform/settings"
)

type Service struct {
	db        *gorm.DB
	settings  *settings.Store
	mailer    mail.Mailer
	baseURL   string
	secretKey string
	devMode   bool
	activity  contracts.ActivityReader
	invites   contracts.InviteService
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

// Configure wires settings, mail and the public base URL after the platform is built.
func (s *Service) Configure(st *settings.Store, mailer mail.Mailer, baseURL string) {
	s.settings = st
	s.mailer = mailer
	s.baseURL = strings.TrimRight(baseURL, "/")
}

// SetEnv disables the admin 2FA enrollment requirement in development so local
// testing does not need an authenticator on every login. Explicitly enabled
// TOTP is also bypassed in development.
func (s *Service) SetEnv(env string) {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "development", "dev", "", "test":
		s.devMode = true
	default:
		s.devMode = false
	}
}

func (s *Service) SetActivity(a contracts.ActivityReader) { s.activity = a }
func (s *Service) SetInvites(i contracts.InviteService)   { s.invites = i }

// RegistrationVerifyEnabled reports whether new accounts must confirm their
// email before they can sign in.
func (s *Service) RegistrationVerifyEnabled() bool {
	return s.settingBool("site.registration.email_verification", true)
}

// PasswordResetEnabled reports whether the self-service password recovery
// flow is available.
func (s *Service) PasswordResetEnabled() bool {
	return s.settingBool("site.verification.password_reset", true)
}

// PasswordChangeEnabled reports whether signed-in users may change their own
// password.
func (s *Service) PasswordChangeEnabled() bool {
	return s.settingBool("site.verification.password_change", true)
}

// PasswordChangeVerifyEnabled reports whether changing a password also needs a
// code sent to the account email.
func (s *Service) PasswordChangeVerifyEnabled() bool {
	return s.settingBool("site.verification.password_change_verify", false)
}

// EmailChangeEnabled reports whether users may change their own email address.
func (s *Service) EmailChangeEnabled() bool {
	return s.settingBool("site.verification.email_change", true)
}

// EmailChangeVerifyEnabled reports whether a new email must be confirmed by a
// link before it replaces the current address.
func (s *Service) EmailChangeVerifyEnabled() bool {
	return s.settingBool("site.verification.email_change_verify", true)
}

// EmailDailyLimit is the per-account daily cap for account-triggered
// verification/security emails. Zero means unlimited.
func (s *Service) EmailDailyLimit() int {
	return int(s.emailLimit("site.verification.email_daily_limit", 10))
}

// EmailTotalLimit is the per-account lifetime cap for account-triggered
// verification/security emails. Zero means unlimited.
func (s *Service) EmailTotalLimit() int {
	return int(s.emailLimit("site.verification.email_total_limit", 50))
}

// emailLimit reads a quota setting without the positive-only fallback used by
// settingInt, because zero is a meaningful "unlimited" value here.
func (s *Service) emailLimit(key string, fallback int64) int64 {
	if s.settings == nil {
		return fallback
	}
	if _, ok := s.settings.Def(key); !ok {
		return fallback
	}
	return s.settings.GetInt(key)
}

// consumeEmailQuota checks the per-user daily and lifetime caps before an
// account-triggered email is sent, then records the attempt. Count failures
// fail open so a partially migrated database cannot break password recovery.
func (s *Service) consumeEmailQuota(ctx context.Context, userID, purpose string) error {
	daily := int64(s.EmailDailyLimit())
	total := int64(s.EmailTotalLimit())
	if daily <= 0 && total <= 0 {
		return nil
	}
	now := time.Now().UTC()
	if daily > 0 {
		var n int64
		if err := s.db.WithContext(ctx).Model(&EmailSendLog{}).
			Where("user_id = ? AND created_at >= ?", userID, s.emailDayStart(now)).
			Count(&n).Error; err != nil {
			return nil
		}
		if n >= daily {
			return httpx.NewError("auth.email_daily_limit", 429, "今日邮件发送次数已达上限，请明天再试")
		}
	}
	if total > 0 {
		var n int64
		if err := s.db.WithContext(ctx).Model(&EmailSendLog{}).
			Where("user_id = ?", userID).Count(&n).Error; err != nil {
			return nil
		}
		if n >= total {
			return httpx.NewError("auth.email_total_limit", 429, "该账号邮件发送次数已达上限，请联系管理员调整限额")
		}
	}
	_ = s.db.WithContext(ctx).Create(&EmailSendLog{
		ID:        id.New(),
		UserID:    userID,
		Purpose:   purpose,
		CreatedAt: now,
	}).Error
	return nil
}

// emailDayStart returns the UTC instant at which the site-local day began.
func (s *Service) emailDayStart(now time.Time) time.Time {
	zone := "Asia/Shanghai"
	if s.settings != nil {
		if v := s.settings.GetString("site.general.timezone"); v != "" {
			zone = v
		}
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
}

// DefaultPermissions are granted to accounts without an explicit group.
var DefaultPermissions = []string{
	"user.profile.edit",
	"topic.create",
	"topic.reply",
	"topic.edit_own",
	"upload.create",
	"reaction.create",
	"bookmark.create",
	"subscription.create",
	"message.send",
	"message.block",
	"notification.preferences",
	"flag.create",
	"bounty.create",
	"lottery.join",
	"invite.create",
}

var dummyHash = func() string {
	h, _ := password.Hash("goforum-timing-equalizer")
	return h
}()

type RegisterResult struct {
	User        *contracts.UserView
	VerifyToken string
}

func (s *Service) CreateAdmin(ctx context.Context, username, email, plainPassword string) (*contracts.UserView, error) {
	u, err := s.createUser(ctx, username, email, plainPassword, "active", true, 4)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	// The admin is usable either way, but a silent failure would leave an
	// account that cannot log in when verification is enforced.
	if err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).
		Update("email_verified_at", now).Error; err != nil {
		return nil, err
	}
	u.EmailVerifiedAt = &now
	return toView(u), nil
}

func (s *Service) Register(ctx context.Context, username, email, plainPassword, inviteCode string) (*RegisterResult, error) {
	mode := s.settingString("site.registration.mode", "open")
	switch mode {
	case "closed":
		return nil, httpx.Forbidden("registration.closed", "站点已关闭注册")
	case "invite", "approval", "open":
	default:
		return nil, httpx.Forbidden("registration.closed", "站点已关闭注册")
	}
	inviteCode = strings.TrimSpace(inviteCode)
	if inviteCode != "" || mode == "invite" {
		if s.invites == nil {
			return nil, httpx.NewError("registration.invite_unavailable", 503, "邀请服务暂不可用")
		}
		if err := s.invites.ValidateInvite(ctx, inviteCode, email); err != nil {
			return nil, err
		}
	}

	verify := s.RegistrationVerifyEnabled()
	status := "active"
	if mode == "approval" {
		status = "pending_review"
	} else if verify {
		status = "pending_email"
	}

	u, err := s.createUser(ctx, username, email, plainPassword, status, false, 0)
	if err != nil {
		return nil, err
	}

	if inviteCode != "" {
		if err := s.invites.ConsumeInvite(ctx, inviteCode, u.ID, email); err != nil {
			return nil, err
		}
	}

	res := &RegisterResult{User: toView(u)}
	if verify {
		if err := s.consumeEmailQuota(ctx, u.ID, "register_verify"); err != nil {
			return nil, err
		}
		raw, err := s.createToken(ctx, u.ID, "verify", 24*time.Hour)
		if err != nil {
			return nil, err
		}
		res.VerifyToken = raw
		s.sendVerifyEmail(ctx, u, raw)
	}
	return res, nil
}

func (s *Service) VerifyEmail(ctx context.Context, rawToken string) error {
	tok, err := s.findToken(ctx, rawToken, "verify")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&EmailToken{}).Where("id = ?", tok.ID).Update("used_at", now).Error; err != nil {
			return err
		}
		updates := map[string]any{"email_verified_at": now}
		var u User
		if err := tx.First(&u, "id = ?", tok.UserID).Error; err != nil {
			return err
		}
		if u.Status == "pending_email" {
			updates["status"] = "active"
		}
		return tx.Model(&User{}).Where("id = ?", u.ID).Updates(updates).Error
	})
}

func (s *Service) ResendVerification(ctx context.Context, email string) error {
	if !s.RegistrationVerifyEnabled() {
		return nil
	}
	var u User
	err := s.db.WithContext(ctx).Where("email_lower = ?", strings.ToLower(strings.TrimSpace(email))).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil // do not enumerate accounts
	}
	if err != nil {
		return err
	}
	if u.EmailVerifiedAt != nil {
		return nil
	}
	if err := s.consumeEmailQuota(ctx, u.ID, "resend_verify"); err != nil {
		return err
	}
	raw, err := s.createToken(ctx, u.ID, "verify", 24*time.Hour)
	if err != nil {
		return err
	}
	s.sendVerifyEmail(ctx, &u, raw)
	return nil
}

func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	if !s.PasswordResetEnabled() {
		return httpx.Forbidden("auth.password_reset_disabled", "密码找回功能已关闭")
	}
	var u User
	err := s.db.WithContext(ctx).Where("email_lower = ?", strings.ToLower(strings.TrimSpace(email))).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil // do not enumerate accounts
	}
	if err != nil {
		return err
	}
	if err := s.consumeEmailQuota(ctx, u.ID, "password_reset"); err != nil {
		return err
	}
	raw, err := s.createToken(ctx, u.ID, "reset", time.Hour)
	if err != nil {
		return err
	}
	link := s.link("/reset-password?token=" + raw)
	s.sendMail(ctx, u.Email,
		"重置密码 - "+s.siteName(),
		"你好 "+u.Username+"：\n\n请在 1 小时内打开以下链接重置密码：\n"+link+"\n\n如果这不是你本人的操作，请忽略本邮件。")
	return nil
}

func (s *Service) ResetPassword(ctx context.Context, rawToken, newPassword string) (string, error) {
	if !s.PasswordResetEnabled() {
		return "", httpx.Forbidden("auth.password_reset_disabled", "密码找回功能已关闭")
	}
	if min := s.settingInt("site.registration.password_min", 10); len(newPassword) < int(min) {
		return "", httpx.Invalid(map[string]string{"password": "密码至少 " + itoa(int(min)) + " 个字符"})
	}
	tok, err := s.findToken(ctx, rawToken, "reset")
	if err != nil {
		return "", err
	}
	hash, err := password.Hash(newPassword)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("id = ?", tok.UserID).Update("password_hash", hash).Error; err != nil {
			return err
		}
		if err := tx.Model(&EmailToken{}).Where("user_id = ? AND purpose = ? AND used_at IS NULL", tok.UserID, "reset").
			Update("used_at", now).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return tok.UserID, nil
}

func (s *Service) expireStatus(ctx context.Context, u *User) {
	if u == nil || (u.Status != "suspended" && u.Status != "banned") || u.StatusUntil == nil {
		return
	}
	if time.Now().UTC().Before(*u.StatusUntil) {
		return
	}
	_ = s.db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).
		Updates(map[string]any{"status": "active", "status_until": nil}).Error
	u.Status = "active"
	u.StatusUntil = nil
}

// RequestEmailChange validates the current password and emails a confirmation
// link to the new address. It returns the raw token for tests and callers that
// need to surface it in development.
func (s *Service) RequestEmailChange(ctx context.Context, userID, currentPassword, newEmail string) (string, error) {
	if !s.EmailChangeEnabled() {
		return "", httpx.Forbidden("auth.email_change_disabled", "站点已关闭自助修改邮箱")
	}
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", userID).Error; err != nil {
		return "", httpx.NotFound("user.not_found", "用户不存在")
	}
	if !password.Verify(u.PasswordHash, currentPassword) {
		return "", httpx.Invalid(map[string]string{"current_password": "当前密码不正确"})
	}
	newEmail = strings.ToLower(strings.TrimSpace(newEmail))
	if !strings.Contains(newEmail, "@") || len(newEmail) > 255 {
		return "", httpx.Invalid(map[string]string{"new_email": "请输入有效的邮箱地址"})
	}
	var existing int64
	if err := s.db.WithContext(ctx).Model(&User{}).
		Where("email_lower = ? AND id <> ?", newEmail, userID).Count(&existing).Error; err != nil {
		return "", err
	}
	if existing > 0 {
		return "", httpx.Invalid(map[string]string{"new_email": "该邮箱已被使用"})
	}
	if err := s.consumeEmailQuota(ctx, userID, "email_change"); err != nil {
		return "", err
	}
	if !s.EmailChangeVerifyEnabled() {
		now := time.Now().UTC()
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&User{}).Where("id = ?", userID).
				Updates(map[string]any{"email": newEmail, "email_lower": newEmail, "email_verified_at": now}).Error; err != nil {
				return err
			}
			// Pending confirmation links for an older address must not be able
			// to overwrite the address that was just applied.
			return tx.Model(&EmailToken{}).
				Where("user_id = ? AND purpose = ? AND used_at IS NULL", userID, "email_change").
				Update("used_at", now).Error
		}); err != nil {
			return "", err
		}
		if u.Email != "" && !strings.EqualFold(u.Email, newEmail) {
			s.sendMail(ctx, u.Email,
				"邮箱已变更 - "+s.siteName(),
				`你好 `+u.Username+`：

你的账号邮箱已变更为 `+newEmail+`。
如果这不是你本人的操作，请立即重置密码并联系管理员。`)
		}
		return "", nil
	}
	raw, err := s.createTokenPayload(ctx, userID, "email_change", newEmail, time.Hour)
	if err != nil {
		return "", err
	}
	link := s.link("/settings/email/confirm?token=" + raw)
	s.sendMail(ctx, newEmail,
		"确认新邮箱 - "+s.siteName(),
		`你好 `+u.Username+`：

请在 1 小时内打开以下链接确认新的邮箱地址：
`+link+`

如果这不是你本人的操作，请忽略本邮件。`)
	if u.Email != "" && !strings.EqualFold(u.Email, newEmail) {
		s.sendMail(ctx, u.Email,
			"邮箱变更申请 - "+s.siteName(),
			`你好 `+u.Username+`：

你的账号申请将邮箱变更为 `+newEmail+`。
如果这不是你本人的操作，请立即修改密码并联系管理员。`)
	}
	return raw, nil
}

// ConfirmEmailChange applies a pending email change for the logged-in owner.
func (s *Service) ConfirmEmailChange(ctx context.Context, rawToken, userID string) error {
	if !s.EmailChangeEnabled() {
		return httpx.Forbidden("auth.email_change_disabled", "站点已关闭自助修改邮箱")
	}
	tok, err := s.findToken(ctx, rawToken, "email_change")
	if err != nil {
		return err
	}
	if tok.UserID != userID {
		return httpx.Forbidden("permission.denied", "没有权限确认该邮箱")
	}
	newEmail := strings.ToLower(strings.TrimSpace(tok.Payload))
	if !strings.Contains(newEmail, "@") || len(newEmail) > 255 {
		return httpx.Invalid(map[string]string{"new_email": "邮箱地址无效"})
	}
	var existing int64
	if err := s.db.WithContext(ctx).Model(&User{}).
		Where("email_lower = ? AND id <> ?", newEmail, userID).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return httpx.Invalid(map[string]string{"new_email": "该邮箱已被使用"})
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("id = ?", userID).Updates(map[string]any{
			"email": newEmail, "email_lower": newEmail, "email_verified_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&EmailToken{}).Where("id = ?", tok.ID).Update("used_at", now).Error
	})
}

// RequestPasswordChangeCode emails a short-lived code that must accompany a
// password change when the site enables that extra verification step. The raw
// code is returned for tests and development helpers; callers must not expose
// it in production responses.
func (s *Service) RequestPasswordChangeCode(ctx context.Context, userID string) (string, error) {
	if !s.PasswordChangeEnabled() {
		return "", httpx.Forbidden("auth.password_change_disabled", "站点已关闭自助修改密码")
	}
	if !s.PasswordChangeVerifyEnabled() {
		return "", httpx.Invalid(map[string]string{"code": "站点未开启修改密码验证"})
	}
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", userID).Error; err != nil {
		return "", httpx.NotFound("user.not_found", "用户不存在")
	}
	if strings.TrimSpace(u.Email) == "" {
		return "", httpx.Invalid(map[string]string{"email": "账号未绑定邮箱，无法发送验证码"})
	}
	if err := s.consumeEmailQuota(ctx, userID, "password_change"); err != nil {
		return "", err
	}
	code, err := randomCode(6)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	// Invalidate earlier codes so only the newest message can be used.
	if err := s.db.WithContext(ctx).Model(&EmailToken{}).
		Where("user_id = ? AND purpose = ? AND used_at IS NULL", userID, "password_change").
		Update("used_at", now).Error; err != nil {
		return "", err
	}
	tok := &EmailToken{
		ID:        id.New(),
		UserID:    userID,
		Purpose:   "password_change",
		TokenHash: tokenHash(code),
		ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := s.db.WithContext(ctx).Create(tok).Error; err != nil {
		return "", err
	}
	s.sendMail(ctx, u.Email,
		"修改密码验证码 - "+s.siteName(),
		"你好 "+u.Username+"：\n\n你正在修改密码，验证码："+code+"\n\n验证码 10 分钟内有效。如果这不是你本人的操作，请忽略本邮件。")
	return code, nil
}

// ChangePassword verifies the current password before setting a new one. When
// the site enables additional verification, code must be the latest code sent
// to the account email.
func (s *Service) ChangePassword(ctx context.Context, userID, current, next, code string) error {
	if !s.PasswordChangeEnabled() {
		return httpx.Forbidden("auth.password_change_disabled", "站点已关闭自助修改密码")
	}
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", userID).Error; err != nil {
		return httpx.NotFound("user.not_found", "用户不存在")
	}
	if !password.Verify(u.PasswordHash, current) {
		return httpx.Invalid(map[string]string{"current_password": "当前密码不正确"})
	}
	if min := s.settingInt("site.registration.password_min", 10); len(next) < int(min) {
		return httpx.Invalid(map[string]string{"new_password": "密码至少 " + itoa(int(min)) + " 个字符"})
	}
	if current == next {
		return httpx.Invalid(map[string]string{"new_password": "新密码不能与当前密码相同"})
	}
	needsCode := s.PasswordChangeVerifyEnabled()
	code = strings.TrimSpace(code)
	if needsCode {
		if len(code) != 6 || !allDigits(code) {
			return httpx.Invalid(map[string]string{"code": "请输入 6 位邮箱验证码"})
		}
		if _, err := s.findUserToken(ctx, userID, "password_change", code); err != nil {
			return err
		}
	}
	hash, err := password.Hash(next)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("id = ?", userID).Update("password_hash", hash).Error; err != nil {
			return err
		}
		if needsCode {
			return tx.Model(&EmailToken{}).
				Where("user_id = ? AND purpose = ? AND used_at IS NULL", userID, "password_change").
				Update("used_at", now).Error
		}
		return nil
	})
}

func (s *Service) Authenticate(ctx context.Context, login, plainPassword string) (*contracts.UserView, error) {
	login = strings.TrimSpace(strings.ToLower(login))
	var u User
	err := s.db.WithContext(ctx).
		Where("username_lower = ? OR email_lower = ?", login, login).
		First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		password.Verify(dummyHash, plainPassword) // equalize timing
		return nil, httpx.NewError("auth.invalid_credentials", 401, "账号或密码错误")
	}
	if err != nil {
		return nil, err
	}
	if !password.Verify(u.PasswordHash, plainPassword) {
		return nil, httpx.NewError("auth.invalid_credentials", 401, "账号或密码错误")
	}
	s.expireStatus(ctx, &u)

	requireVerify := s.RegistrationVerifyEnabled()
	switch u.Status {
	case "active":
		if requireVerify && u.EmailVerifiedAt == nil {
			return nil, httpx.NewError("auth.email_unverified", 403, "请先验证邮箱后再登录")
		}
	case "pending_email":
		if requireVerify {
			return nil, httpx.NewError("auth.email_unverified", 403, "请先验证邮箱后再登录")
		}
		// Verification was disabled after this account registered; it no
		// longer makes sense to keep the account in the pending state.
		if err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).
			Update("status", "active").Error; err == nil {
			u.Status = "active"
		}
	case "pending_review":
		return nil, httpx.NewError("auth.pending_review", 403, "账号正在等待管理员审核")
	default:
		return nil, httpx.Forbidden("user.banned", "账号已被限制，请联系管理员")
	}

	now := time.Now().UTC()
	_ = s.db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).Update("last_seen_at", now).Error
	u.LastSeenAt = &now
	return toView(&u), nil
}

func (s *Service) GetByID(ctx context.Context, id string) (*contracts.UserView, error) {
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", id).Error; err != nil {
		return nil, err
	}
	s.expireStatus(ctx, &u)
	return toView(&u), nil
}

func (s *Service) GetByUsername(ctx context.Context, username string) (*contracts.UserView, error) {
	var u User
	err := s.db.WithContext(ctx).Where("username_lower = ?", strings.ToLower(strings.TrimSpace(username))).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, httpx.NotFound("user.not_found", "用户不存在")
	}
	if err != nil {
		return nil, err
	}
	s.expireStatus(ctx, &u)
	return toView(&u), nil
}

func (s *Service) GetSummaries(ctx context.Context, ids []string) (map[string]contracts.UserSummary, error) {
	out := make(map[string]contracts.UserSummary, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []User
	if err := s.db.WithContext(ctx).Select("id, username, bio, signature, avatar_url, donate_enabled, donate_message, donate_url, trust_level, created_at").
		Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, u := range rows {
		out[u.ID] = contracts.UserSummary{
			ID:            u.ID,
			Username:      u.Username,
			Bio:           u.Bio,
			Signature:     u.Signature,
			AvatarURL:     u.AvatarURL,
			DonateEnabled: u.DonateEnabled,
			DonateMessage: u.DonateMessage,
			DonateURL:     u.DonateURL,
			TrustLevel:    u.TrustLevel,
			CreatedAt:     u.CreatedAt,
		}
	}
	return out, nil
}

func (s *Service) FindByUsernames(ctx context.Context, usernames []string) (map[string]contracts.UserSummary, error) {
	out := make(map[string]contracts.UserSummary, len(usernames))
	if len(usernames) == 0 {
		return out, nil
	}
	lower := make([]string, 0, len(usernames))
	for _, name := range usernames {
		lower = append(lower, strings.ToLower(strings.TrimSpace(name)))
	}
	var rows []User
	if err := s.db.WithContext(ctx).Select("id, username, bio, signature, avatar_url, donate_enabled, donate_message, donate_url, trust_level, created_at").
		Where("username_lower IN ?", lower).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, u := range rows {
		out[strings.ToLower(u.Username)] = contracts.UserSummary{
			ID:            u.ID,
			Username:      u.Username,
			Bio:           u.Bio,
			Signature:     u.Signature,
			AvatarURL:     u.AvatarURL,
			DonateEnabled: u.DonateEnabled,
			DonateMessage: u.DonateMessage,
			DonateURL:     u.DonateURL,
			TrustLevel:    u.TrustLevel,
			CreatedAt:     u.CreatedAt,
		}
	}
	return out, nil
}

func (s *Service) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&User{}).Count(&n).Error
	return n, err
}

func (s *Service) CountPending(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&User{}).Where("status = ?", "pending_review").Count(&n).Error
	return n, err
}

func (s *Service) List(ctx context.Context, limit, offset int) ([]contracts.UserView, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	var rows []User
	if err := s.db.WithContext(ctx).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]contracts.UserView, 0, len(rows))
	for i := range rows {
		out = append(out, *toView(&rows[i]))
	}
	return out, nil
}

func (s *Service) SetStatus(ctx context.Context, userID, status string) error {
	return s.SetStatusUntil(ctx, userID, status, nil)
}

func (s *Service) SetStatusUntil(ctx context.Context, userID, status string, until *time.Time) error {
	switch status {
	case "active", "suspended", "banned", "rejected", "pending_review":
	default:
		return httpx.Invalid(map[string]string{"status": "无效的用户状态"})
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return httpx.NotFound("user.not_found", "用户不存在")
	}
	return s.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).
		Updates(map[string]any{"status": status, "status_until": until}).Error
}

func (s *Service) UpdateProfile(ctx context.Context, userID, bio, signature, locale string, bioEnabled, donateEnabled bool, donateMessage, donateURL, coverURL, avatarURL string) (*contracts.UserView, error) {
	bio = strings.TrimSpace(bio)
	if len([]rune(bio)) > 1000 {
		return nil, httpx.Invalid(map[string]string{"bio": "简介最多 1000 个字符"})
	}
	signature = strings.TrimSpace(signature)
	if len([]rune(signature)) > 300 {
		return nil, httpx.Invalid(map[string]string{"signature": "签名最多 300 个字符"})
	}
	if strings.Count(signature, "\n")+1 > 6 {
		return nil, httpx.Invalid(map[string]string{"signature": "签名最多 6 行"})
	}
	locale = strings.TrimSpace(locale)
	if locale != "" && locale != "zh-CN" && locale != "en-US" {
		return nil, httpx.Invalid(map[string]string{"locale": "不支持的语言"})
	}
	donateMessage = strings.TrimSpace(donateMessage)
	if len([]rune(donateMessage)) > 200 {
		return nil, httpx.Invalid(map[string]string{"donate_message": "捐赠说明最多 200 个字符"})
	}
	donateURL = strings.TrimSpace(donateURL)
	if donateURL != "" && !strings.HasPrefix(donateURL, "http://") && !strings.HasPrefix(donateURL, "https://") {
		return nil, httpx.Invalid(map[string]string{"donate_url": "捐赠链接必须是 http/https 地址"})
	}
	coverURL = strings.TrimSpace(coverURL)
	if len([]rune(coverURL)) > 500 || (coverURL != "" && !strings.HasPrefix(coverURL, "http://") && !strings.HasPrefix(coverURL, "https://") && !strings.HasPrefix(coverURL, "/")) {
		return nil, httpx.Invalid(map[string]string{"cover_url": "封面地址必须是 http/https 或站内路径"})
	}
	avatarURL = strings.TrimSpace(avatarURL)
	if len([]rune(avatarURL)) > 500 || (avatarURL != "" && !strings.HasPrefix(avatarURL, "http://") && !strings.HasPrefix(avatarURL, "https://") && !strings.HasPrefix(avatarURL, "/")) {
		return nil, httpx.Invalid(map[string]string{"avatar_url": "头像地址必须是 http/https 或站内路径"})
	}
	err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).
		Updates(map[string]any{"bio": bio, "bio_enabled": bioEnabled, "cover_url": coverURL, "avatar_url": avatarURL, "signature": signature, "locale": locale,
			"donate_enabled": donateEnabled, "donate_message": donateMessage, "donate_url": donateURL}).Error
	if err != nil {
		return nil, err
	}
	return s.GetByID(ctx, userID)
}

func (s *Service) createUser(ctx context.Context, username, email, plainPassword, status string, super bool, trust int) (*User, error) {
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	fields := map[string]string{}
	if n := len([]rune(username)); n < 3 || n > 50 {
		fields["username"] = "用户名长度需在 3 到 50 个字符之间"
	}
	if !strings.Contains(email, "@") || len(email) > 255 {
		fields["email"] = "请输入有效邮箱"
	}
	if min := s.settingInt("site.registration.password_min", 10); len(plainPassword) < int(min) {
		fields["password"] = "密码至少 " + itoa(int(min)) + " 个字符"
	}
	if len(fields) > 0 {
		return nil, httpx.Invalid(fields)
	}

	var existing int64
	if err := s.db.WithContext(ctx).
		Model(&User{}).
		Where("username_lower = ? OR email_lower = ?", strings.ToLower(username), strings.ToLower(email)).
		Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		fields["username"] = "用户名或邮箱已被占用"
		return nil, httpx.Invalid(fields)
	}

	hash, err := password.Hash(plainPassword)
	if err != nil {
		return nil, err
	}
	u := &User{
		Username:     username,
		Email:        email,
		PasswordHash: hash,
		IsSuperAdmin: super,
		Status:       status,
		TrustLevel:   trust,
	}
	if err := s.db.WithContext(ctx).Create(u).Error; err != nil {
		return nil, err
	}
	_ = s.assignDefaultGroup(ctx, u.ID)
	return u, nil
}

func (s *Service) EnsureDefaultGroup(ctx context.Context) error {
	var g Group
	err := s.db.WithContext(ctx).Where("is_default = ?", true).First(&g).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.WithContext(ctx).Create(&Group{
			Name: "成员", Slug: "members", IsDefault: true, Priority: 0,
			PermissionsJSON: encodePermissions(DefaultPermissions),
		}).Error
	}
	if err != nil {
		return err
	}
	var existing []string
	_ = json.Unmarshal([]byte(g.PermissionsJSON), &existing)
	merged := dedupeStrings(append(existing, DefaultPermissions...))
	if len(merged) != len(existing) {
		return s.db.WithContext(ctx).Model(&Group{}).Where("id = ?", g.ID).
			Update("permissions_json", encodePermissions(merged)).Error
	}
	return nil
}

// EnsureModeratorGroup creates a ready-to-use moderator group. It is created
// once so admins can assign users and later customize its permissions.
func (s *Service) EnsureModeratorGroup(ctx context.Context) error {
	var n int64
	if err := s.db.WithContext(ctx).Model(&Group{}).Where("slug = ?", "moderators").Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		// Add newly introduced content permissions to the default moderator
		// preset once, preserving any custom removals afterwards.
		if !s.settingBool("auth.moderators.pin_feature_granted", false) {
			var g Group
			if err := s.db.WithContext(ctx).Where("slug = ?", "moderators").First(&g).Error; err == nil {
				var existing []string
				_ = json.Unmarshal([]byte(g.PermissionsJSON), &existing)
				merged := dedupeStrings(append(existing, "topic.pin", "topic.feature"))
				if err := s.db.WithContext(ctx).Model(&Group{}).Where("id = ?", g.ID).
					Update("permissions_json", encodePermissions(merged)).Error; err != nil {
					return err
				}
			}
			if s.settings != nil {
				_ = s.settings.Set(ctx, "auth.moderators.pin_feature_granted", true, "system")
			}
		}
		return nil
	}
	perms := []string{
		"admin.access", "admin.content.view", "topic.moderate", "topic.pin", "topic.feature",
		"flag.review", "moderation.queue.view", "moderation.action",
	}
	return s.db.WithContext(ctx).Create(&Group{
		Name: "版主", Slug: "moderators", Color: "#0ea5e9", Priority: 50,
		PermissionsJSON: encodePermissions(perms),
	}).Error
}

func (s *Service) assignDefaultGroup(ctx context.Context, userID string) error {
	var g Group
	if err := s.db.WithContext(ctx).Where("is_default = ?", true).First(&g).Error; err != nil {
		return nil // no default group configured yet
	}
	ug := UserGroup{UserID: userID, GroupID: g.ID}
	return s.db.WithContext(ctx).Where("user_id = ? AND group_id = ?", userID, g.ID).
		FirstOrCreate(&ug).Error
}

func (s *Service) ListGroups(ctx context.Context) ([]contracts.GroupView, error) {
	var rows []Group
	if err := s.db.WithContext(ctx).Order("priority DESC, name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]contracts.GroupView, 0, len(rows))
	for _, g := range rows {
		out = append(out, groupView(g))
	}
	return out, nil
}

func (s *Service) GroupsOf(ctx context.Context, userID string) ([]contracts.GroupView, error) {
	var rows []Group
	err := s.db.WithContext(ctx).Table("groups AS g").
		Select("g.*").
		Joins("JOIN user_groups ug ON ug.group_id = g.id").
		Where("ug.user_id = ?", userID).
		Where("ug.expires_at IS NULL OR ug.expires_at > ?", time.Now().UTC()).
		Order("g.priority DESC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]contracts.GroupView, 0, len(rows))
	for _, g := range rows {
		out = append(out, groupView(g))
	}
	return out, nil
}

func (s *Service) CreateGroup(ctx context.Context, in contracts.GroupInput) (*contracts.GroupView, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, httpx.Invalid(map[string]string{"name": "名称不能为空"})
	}
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		slug = groupSlug(name)
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&Group{}).Where("slug = ?", slug).Count(&n).Error; err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, httpx.Invalid(map[string]string{"slug": "slug 已存在"})
	}
	g := &Group{
		Name: name, Slug: slug, Color: in.Color, Priority: in.Priority,
		PermissionsJSON: encodePermissions(in.Permissions),
	}
	if err := s.db.WithContext(ctx).Create(g).Error; err != nil {
		return nil, err
	}
	v := groupView(*g)
	return &v, nil
}

func (s *Service) UpdateGroup(ctx context.Context, in contracts.GroupInput) error {
	var g Group
	if err := s.db.WithContext(ctx).First(&g, "id = ?", in.ID).Error; err != nil {
		return httpx.NotFound("group.not_found", "用户组不存在")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return httpx.Invalid(map[string]string{"name": "名称不能为空"})
	}
	return s.db.WithContext(ctx).Model(&Group{}).Where("id = ?", g.ID).Updates(map[string]any{
		"name": name, "color": in.Color, "priority": in.Priority,
		"permissions_json": encodePermissions(in.Permissions),
	}).Error
}

func (s *Service) DeleteGroup(ctx context.Context, id string) error {
	var g Group
	if err := s.db.WithContext(ctx).First(&g, "id = ?", id).Error; err != nil {
		return httpx.NotFound("group.not_found", "用户组不存在")
	}
	if g.IsDefault {
		return httpx.Invalid(map[string]string{"group": "默认用户组不能删除"})
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&UserGroup{}, "group_id = ?", id).Error; err != nil {
			return err
		}
		return tx.Delete(&Group{}, "id = ?", id).Error
	})
}

func (s *Service) SetGroups(ctx context.Context, userID string, groupIDs []string) error {
	var user User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		return httpx.NotFound("user.not_found", "用户不存在")
	}
	ids := dedupeStrings(groupIDs)
	var valid []string
	if len(ids) > 0 {
		_ = s.db.WithContext(ctx).Model(&Group{}).Where("id IN ?", ids).Pluck("id", &valid).Error
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&UserGroup{}, "user_id = ?", userID).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, gid := range valid {
			if err := tx.Create(&UserGroup{UserID: userID, GroupID: gid, CreatedAt: now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) ListFiltered(ctx context.Context, query, status string, includeEmail bool, limit, offset int) ([]contracts.UserView, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	base := s.db.WithContext(ctx).Model(&User{})
	if q := strings.TrimSpace(strings.ToLower(query)); q != "" {
		like := "%" + q + "%"
		if includeEmail {
			base = base.Where("username_lower LIKE ? OR email_lower LIKE ? OR (bio_enabled = ? AND LOWER(bio) LIKE ?)", like, like, true, like)
		} else {
			base = base.Where("username_lower LIKE ? OR (bio_enabled = ? AND LOWER(bio) LIKE ?)", like, true, like)
		}
	}
	if status != "" {
		base = base.Where("status = ?", status)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []User
	if err := base.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]contracts.UserView, 0, len(rows))
	for i := range rows {
		out = append(out, *toView(&rows[i]))
	}
	return out, total, nil
}

func (s *Service) PermissionsOf(ctx context.Context, userID string) (map[string]struct{}, error) {
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	out := map[string]struct{}{}
	if u.IsSuperAdmin {
		return out, nil
	}
	groups, err := s.GroupsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		for _, p := range DefaultPermissions {
			out[p] = struct{}{}
		}
		return out, nil
	}
	for _, g := range groups {
		for _, p := range g.Permissions {
			out[p] = struct{}{}
		}
	}
	return out, nil
}

func (s *Service) RecomputeTrustLevels(ctx context.Context) (int, error) {
	if s.activity == nil {
		return 0, nil
	}
	var users []User
	if err := s.db.WithContext(ctx).Where("status = ? AND trust_level < 4", "active").Find(&users).Error; err != nil {
		return 0, err
	}
	if len(users) == 0 {
		return 0, nil
	}
	// Fetch every user's counts in a handful of grouped queries instead of
	// three per user; the hourly job previously issued 3N statements.
	ids := make([]string, 0, len(users))
	for i := range users {
		ids = append(ids, users[i].ID)
	}
	counts, err := s.activity.UserActivityBatch(ctx, ids)
	if err != nil {
		return 0, err
	}
	changed := 0
	for i := range users {
		activity := counts[users[i].ID]
		posts, likes := activity.Posts, activity.Likes
		days := int64(time.Since(users[i].CreatedAt).Hours() / 24)
		level := 0
		switch {
		case days >= s.settingInt("auth.trust.tl3_days", 50) &&
			posts >= s.settingInt("auth.trust.tl3_posts", 50) &&
			likes >= s.settingInt("auth.trust.tl3_likes", 20):
			level = 3
		case days >= s.settingInt("auth.trust.tl2_days", 15) &&
			posts >= s.settingInt("auth.trust.tl2_posts", 10) &&
			likes >= s.settingInt("auth.trust.tl2_likes", 1):
			level = 2
		case days >= s.settingInt("auth.trust.tl1_days", 1) &&
			posts >= s.settingInt("auth.trust.tl1_posts", 1):
			level = 1
		}
		if level != users[i].TrustLevel {
			if err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", users[i].ID).
				Update("trust_level", level).Error; err == nil {
				changed++
			}
		}
	}
	return changed, nil
}

func (s *Service) SetTrustLevel(ctx context.Context, userID string, level int) error {
	if level < 0 || level > 4 {
		return httpx.Invalid(map[string]string{"trust_level": "信任等级需在 0–4 之间"})
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return httpx.NotFound("user.not_found", "用户不存在")
	}
	return s.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).Update("trust_level", level).Error
}

func groupView(g Group) contracts.GroupView {
	var perms []string
	_ = json.Unmarshal([]byte(g.PermissionsJSON), &perms)
	if perms == nil {
		perms = []string{}
	}
	return contracts.GroupView{
		ID: g.ID, Name: g.Name, Slug: g.Slug, Color: g.Color,
		Priority: g.Priority, IsDefault: g.IsDefault, Permissions: perms,
	}
}

func encodePermissions(perms []string) string {
	b, _ := json.Marshal(dedupeStrings(perms))
	return string(b)
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func groupSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == ' ' || r == '-' || r == '_':
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "group"
	}
	return s
}

func (s *Service) createToken(ctx context.Context, userID, purpose string, ttl time.Duration) (string, error) {
	raw, err := randomToken()
	if err != nil {
		return "", err
	}
	tok := &EmailToken{
		ID:        id.New(),
		UserID:    userID,
		Purpose:   purpose,
		TokenHash: tokenHash(raw),
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	if err := s.db.WithContext(ctx).Create(tok).Error; err != nil {
		return "", err
	}
	return raw, nil
}

func (s *Service) createTokenPayload(ctx context.Context, userID, purpose, payload string, ttl time.Duration) (string, error) {
	raw, err := randomToken()
	if err != nil {
		return "", err
	}
	tok := &EmailToken{
		ID:        id.New(),
		UserID:    userID,
		Purpose:   purpose,
		Payload:   payload,
		TokenHash: tokenHash(raw),
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	if err := s.db.WithContext(ctx).Create(tok).Error; err != nil {
		return "", err
	}
	return raw, nil
}

func (s *Service) findToken(ctx context.Context, raw, purpose string) (*EmailToken, error) {
	var tok EmailToken
	err := s.db.WithContext(ctx).
		Where("token_hash = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?", tokenHash(raw), purpose, time.Now().UTC()).
		First(&tok).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, httpx.NewError("auth.invalid_token", 400, "链接无效或已过期，请重新申请")
	}
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

// findUserToken looks up a token that belongs to the given user. Email codes
// must be bound to the account being changed, not just to any live token.
func (s *Service) findUserToken(ctx context.Context, userID, purpose, raw string) (*EmailToken, error) {
	var tok EmailToken
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND token_hash = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?",
			userID, tokenHash(raw), purpose, time.Now().UTC()).
		First(&tok).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, httpx.Invalid(map[string]string{"code": "验证码无效或已过期"})
	}
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

func randomCode(digits int) (string, error) {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", err
	}
	code := n.String()
	for len(code) < digits {
		code = "0" + code
	}
	return code, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func (s *Service) sendVerifyEmail(ctx context.Context, u *User, raw string) {
	link := s.link("/verify-email?token=" + raw)
	s.sendMail(ctx, u.Email,
		"验证你的邮箱 - "+s.siteName(),
		"你好 "+u.Username+"：\n\n请在 24 小时内打开以下链接完成邮箱验证：\n"+link+"\n\n如果这不是你本人的操作，请忽略本邮件。")
}

func (s *Service) sendMail(ctx context.Context, to, subject, text string) {
	if s.mailer == nil {
		return
	}
	_ = s.mailer.Send(ctx, mail.Message{To: []string{to}, Subject: subject, Text: text})
}

func (s *Service) link(path string) string {
	if s.baseURL == "" {
		return path
	}
	return s.baseURL + path
}

func (s *Service) siteName() string { return s.settingString("site.general.name", "GoForum") }

func (s *Service) settingString(key, fallback string) string {
	if s.settings == nil {
		return fallback
	}
	if v := s.settings.GetString(key); v != "" {
		return v
	}
	return fallback
}

func (s *Service) settingBool(key string, fallback bool) bool {
	if s.settings == nil {
		return fallback
	}
	return s.settings.GetBool(key)
}

func (s *Service) settingInt(key string, fallback int) int64 {
	if s.settings == nil {
		return int64(fallback)
	}
	if v := s.settings.GetInt(key); v > 0 {
		return v
	}
	return int64(fallback)
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func toView(u *User) *contracts.UserView {
	return &contracts.UserView{
		ID:              u.ID,
		Username:        u.Username,
		Email:           u.Email,
		Bio:             u.Bio,
		BioEnabled:      u.BioEnabled,
		AvatarURL:       u.AvatarURL,
		CoverURL:        u.CoverURL,
		Signature:       u.Signature,
		DonateEnabled:   u.DonateEnabled,
		DonateMessage:   u.DonateMessage,
		DonateURL:       u.DonateURL,
		SuperAdmin:      u.IsSuperAdmin,
		Status:          u.Status,
		StatusUntil:     u.StatusUntil,
		TrustLevel:      u.TrustLevel,
		Locale:          u.Locale,
		EmailVerifiedAt: u.EmailVerifiedAt,
		TOTPEnabled:     u.TOTPEnabled,
		CreatedAt:       u.CreatedAt,
		LastSeenAt:      u.LastSeenAt,
	}
}
