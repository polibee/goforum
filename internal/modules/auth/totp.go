package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"net/url"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/password"
	"goforum/internal/platform/secretbox"
)

const (
	totpPurpose         = "login_2fa"
	totpChallengeTTL    = 5 * time.Minute
	recoveryCodeCount   = 10
	recoveryCodeEntropy = 10
)

var recoveryAlphabet = []byte("ABCDEFGHJKLMNPQRSTUVWXYZ23456789")

// SetSecretKey installs the site key used to encrypt TOTP secrets at rest.
func (s *Service) SetSecretKey(key string) {
	s.secretKey = strings.TrimSpace(key)
}

// AdminTOTPPolicy returns off, superadmin or admins.
func (s *Service) AdminTOTPPolicy() string {
	if s.settings == nil {
		return "superadmin"
	}
	policy := s.settings.GetString("site.security.admin_2fa_policy")
	switch policy {
	case "off", "superadmin", "admins":
		return policy
	default:
		return "superadmin"
	}
}

// AdminTOTPRequired reports whether this account must enroll before using the
// admin area.
func (s *Service) AdminTOTPRequired(ctx context.Context, userID string, superAdmin bool) bool {
	if s.devMode {
		return false
	}
	switch s.AdminTOTPPolicy() {
	case "off":
		return false
	case "admins":
		if superAdmin {
			return true
		}
		perms, err := s.PermissionsOf(ctx, userID)
		if err != nil {
			return false
		}
		_, ok := perms["admin.access"]
		return ok
	default:
		return superAdmin
	}
}

// TwoFactorMode decides what the login flow must do after the password step:
// "verify" for enrolled accounts, "setup" when policy requires enrollment, or
// "" for a normal login.
func (s *Service) TwoFactorMode(ctx context.Context, user *contracts.UserView) string {
	if s.devMode || user == nil {
		return ""
	}
	if user.TOTPEnabled {
		return "verify"
	}
	if s.AdminTOTPRequired(ctx, user.ID, user.SuperAdmin) {
		return "setup"
	}
	return ""
}

// CreateLoginChallenge stores a short-lived token that proves the password
// step passed. The cookie carries the raw token; the database stores only its
// hash.
func (s *Service) CreateLoginChallenge(ctx context.Context, userID, next string) (string, error) {
	return s.createTokenPayload(ctx, userID, totpPurpose, next, totpChallengeTTL)
}

// LoginChallengeUser validates a challenge token for the challenge page.
func (s *Service) LoginChallengeUser(ctx context.Context, raw string) (*contracts.UserView, error) {
	tok, err := s.findToken(ctx, raw, totpPurpose)
	if err != nil {
		return nil, err
	}
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", tok.UserID).Error; err != nil {
		return nil, err
	}
	if u.Status != "active" || !u.TOTPEnabled {
		return nil, httpx.NewError("auth.invalid_token", 400, "登录状态已失效，请重新登录")
	}
	return toView(&u), nil
}

// VerifyLoginChallenge consumes the challenge and returns the account and the
// original post-login destination.
func (s *Service) VerifyLoginChallenge(ctx context.Context, raw, code string) (string, string, error) {
	tok, err := s.findToken(ctx, raw, totpPurpose)
	if err != nil {
		return "", "", err
	}
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", tok.UserID).Error; err != nil {
		return "", "", err
	}
	if u.Status != "active" || !u.TOTPEnabled {
		return "", "", httpx.NewError("auth.invalid_token", 400, "登录状态已失效，请重新登录")
	}
	if err := s.verifyTOTPOrRecovery(ctx, &u, code); err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&EmailToken{}).Where("id = ?", tok.ID).
		Update("used_at", now).Error; err != nil {
		return "", "", err
	}
	return u.ID, tok.Payload, nil
}

// TOTPSetup describes one pending enrollment.
type TOTPSetup struct {
	Secret    string
	URI       string
	QRDataURI string
}

// EnsureTOTPSetup returns an existing pending secret or creates a new one. The
// secret is stored encrypted and stays disabled until a valid code is entered.
func (s *Service) EnsureTOTPSetup(ctx context.Context, userID string) (*TOTPSetup, error) {
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", userID).Error; err != nil {
		return nil, httpx.NotFound("user.not_found", "用户不存在")
	}
	if u.TOTPEnabled {
		return nil, httpx.NewError("auth.totp_already_enabled", 409, "两步验证已经启用")
	}
	secret := ""
	if u.TOTPSecret != "" {
		if decrypted, err := s.decryptTOTP(u.TOTPSecret); err == nil {
			secret = decrypted
		}
	}
	if secret == "" {
		raw := make([]byte, 20)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
		encrypted, err := s.encryptTOTP(secret)
		if err != nil {
			return nil, err
		}
		if err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).
			Updates(map[string]any{
				"totp_secret":        encrypted,
				"totp_enabled":       false,
				"totp_recovery_json": "",
				"totp_last_step":     0,
			}).Error; err != nil {
			return nil, err
		}
	}
	uri := buildTOTPURI(s.siteName(), u.Username, secret)
	key, err := otp.NewKeyFromURL(uri)
	if err != nil {
		return nil, err
	}
	img, err := key.Image(256, 256)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return &TOTPSetup{
		Secret:    secret,
		URI:       uri,
		QRDataURI: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
	}, nil
}

// EnableTOTP verifies the first code, enables TOTP and returns one-time
// recovery codes. The plaintext codes are shown once and only hashes are
// stored.
func (s *Service) EnableTOTP(ctx context.Context, userID, code string) ([]string, error) {
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", userID).Error; err != nil {
		return nil, httpx.NotFound("user.not_found", "用户不存在")
	}
	if u.TOTPEnabled {
		return nil, httpx.NewError("auth.totp_already_enabled", 409, "两步验证已经启用")
	}
	secret, err := s.decryptTOTP(u.TOTPSecret)
	if err != nil {
		return nil, httpx.Invalid(map[string]string{"code": "两步验证密钥无效，请重新生成"})
	}
	code = strings.TrimSpace(code)
	step, ok := validateTOTPCode(secret, code)
	if !ok {
		return nil, httpx.Invalid(map[string]string{"code": "验证码不正确，请检查身份验证器时间"})
	}
	if nowStep := time.Now().Unix() / 30; step < nowStep {
		step = nowStep
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(hashes)
	if err != nil {
		return nil, err
	}
	res := s.db.WithContext(ctx).Model(&User{}).
		Where("id = ? AND totp_enabled = ?", u.ID, false).
		Updates(map[string]any{
			"totp_enabled":       true,
			"totp_last_step":     step,
			"totp_recovery_json": string(encoded),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, httpx.NewError("auth.totp_already_enabled", 409, "两步验证已经启用")
	}
	return codes, nil
}

// DisableTOTP turns off 2FA after verifying the password and a current code or
// recovery code. Sites that require admin 2FA refuse the operation.
func (s *Service) DisableTOTP(ctx context.Context, userID, plainPassword, code string) error {
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", userID).Error; err != nil {
		return httpx.NotFound("user.not_found", "用户不存在")
	}
	if !u.TOTPEnabled {
		return nil
	}
	if !password.Verify(u.PasswordHash, plainPassword) {
		return httpx.Invalid(map[string]string{"current_password": "当前密码不正确"})
	}
	if s.AdminTOTPRequired(ctx, u.ID, u.IsSuperAdmin) {
		return httpx.Forbidden("auth.totp_required", "当前站点要求管理员启用两步验证，无法关闭")
	}
	if err := s.verifyTOTPOrRecovery(ctx, &u, code); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).
		Updates(map[string]any{
			"totp_enabled":       false,
			"totp_secret":        "",
			"totp_recovery_json": "",
			"totp_last_step":     0,
		}).Error
}

// RecoveryCodesRemaining counts unused one-time codes.
func (s *Service) RecoveryCodesRemaining(ctx context.Context, userID string) int {
	var u User
	if err := s.db.WithContext(ctx).Select("totp_recovery_json").
		First(&u, "id = ?", userID).Error; err != nil {
		return 0
	}
	var hashes []string
	if err := json.Unmarshal([]byte(u.TOTPRecoveryJSON), &hashes); err != nil {
		return 0
	}
	return len(hashes)
}

func (s *Service) verifyTOTPOrRecovery(ctx context.Context, u *User, code string) error {
	code = strings.TrimSpace(code)
	if len(code) == 6 && allDigits(code) {
		secret, err := s.decryptTOTP(u.TOTPSecret)
		if err != nil {
			return httpx.Invalid(map[string]string{"code": "验证码不正确或已过期"})
		}
		step, ok := validateTOTPCode(secret, code)
		if !ok {
			return httpx.Invalid(map[string]string{"code": "验证码不正确或已过期"})
		}
		res := s.db.WithContext(ctx).Model(&User{}).
			Where("id = ? AND totp_last_step < ?", u.ID, step).
			Update("totp_last_step", step)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return httpx.Invalid(map[string]string{"code": "验证码已使用，请等待下一次刷新"})
		}
		return nil
	}
	return s.consumeRecoveryCode(ctx, u, code)
}

func (s *Service) consumeRecoveryCode(ctx context.Context, u *User, code string) error {
	normalized := normalizeRecoveryCode(code)
	if normalized == "" {
		return httpx.Invalid(map[string]string{"code": "请输入验证码或恢复码"})
	}
	var hashes []string
	if err := json.Unmarshal([]byte(u.TOTPRecoveryJSON), &hashes); err != nil {
		return httpx.Invalid(map[string]string{"code": "恢复码不可用，请使用身份验证器"})
	}
	want := sha256Hex(normalized)
	idx := -1
	for i, h := range hashes {
		if h == want {
			idx = i
			break
		}
	}
	if idx < 0 {
		return httpx.Invalid(map[string]string{"code": "验证码或恢复码不正确"})
	}
	remaining := append(append([]string{}, hashes[:idx]...), hashes[idx+1:]...)
	encoded, err := json.Marshal(remaining)
	if err != nil {
		return err
	}
	old, _ := json.Marshal(hashes)
	res := s.db.WithContext(ctx).Model(&User{}).
		Where("id = ? AND totp_recovery_json = ?", u.ID, string(old)).
		Update("totp_recovery_json", string(encoded))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return httpx.Invalid(map[string]string{"code": "恢复码已被使用"})
	}
	return nil
}

func newRecoveryCodes() ([]string, []string, error) {
	codes := make([]string, 0, recoveryCodeCount)
	hashes := make([]string, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		raw := make([]byte, recoveryCodeEntropy)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		for j := range raw {
			raw[j] = recoveryAlphabet[int(raw[j])%len(recoveryAlphabet)]
		}
		code := string(raw[:5]) + "-" + string(raw[5:])
		codes = append(codes, code)
		hashes = append(hashes, sha256Hex(normalizeRecoveryCode(code)))
	}
	return codes, hashes, nil
}

func normalizeRecoveryCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	code = strings.ReplaceAll(code, " ", "")
	return code
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// validateTOTPCode accepts the current 30-second step plus one step of clock
// skew, and returns the step that matched so replay protection can advance
// even when the code came from the neighbouring window.
func validateTOTPCode(secret, code string) (int64, bool) {
	if len(code) != 6 || !allDigits(code) {
		return 0, false
	}
	now := time.Now()
	for _, offset := range []int{0, -1, 1} {
		at := now.Add(time.Duration(offset*30) * time.Second)
		ok, err := totp.ValidateCustom(code, secret, at, totp.ValidateOpts{
			Period:    30,
			Skew:      0,
			Digits:    otp.DigitsSix,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err == nil && ok {
			return at.Unix() / 30, true
		}
	}
	return 0, false
}

func buildTOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func (s *Service) encryptTOTP(secret string) (string, error) {
	if s.secretKey == "" {
		return "", errors.New("站点未配置 APP_KEY，无法启用两步验证")
	}
	return secretbox.Encrypt(s.secretKey, []byte(secret))
}

func (s *Service) decryptTOTP(encoded string) (string, error) {
	if encoded == "" {
		return "", errors.New("missing totp secret")
	}
	if !secretbox.IsEncrypted(encoded) {
		// Rows created before encryption was introduced remain readable.
		return encoded, nil
	}
	if s.secretKey == "" {
		return "", secretbox.ErrKeyMissing
	}
	plain, err := secretbox.Decrypt(s.secretKey, encoded)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
