package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/httpx"
)

func mustTOTPCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, at)
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	return code
}

func TestTOTPEnrollmentAndLogin(t *testing.T) {
	svc, db, _, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "totpadmin", "totp@example.com", "totp12345678")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}

	setup, err := svc.EnsureTOTPSetup(ctx, user.ID)
	if err != nil {
		t.Fatalf("ensure setup: %v", err)
	}
	if setup.Secret == "" || !strings.HasPrefix(setup.QRDataURI, "data:image/png;base64,") {
		t.Fatalf("setup payload incomplete: %+v", setup)
	}
	var stored User
	if err := db.First(&stored, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if !strings.HasPrefix(stored.TOTPSecret, "v1:") || strings.Contains(stored.TOTPSecret, setup.Secret) {
		t.Fatalf("totp secret is not encrypted: %q", stored.TOTPSecret)
	}

	code := mustTOTPCode(t, setup.Secret, time.Now())
	codes, err := svc.EnableTOTP(ctx, user.ID, code)
	if err != nil {
		t.Fatalf("enable totp: %v", err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("recovery codes = %d", len(codes))
	}
	got, err := svc.GetByID(ctx, user.ID)
	if err != nil || !got.TOTPEnabled {
		t.Fatalf("totp enabled = %v err=%v", got, err)
	}
	if n := svc.RecoveryCodesRemaining(ctx, user.ID); n != recoveryCodeCount {
		t.Fatalf("recovery remaining = %d", n)
	}

	nextCode := mustTOTPCode(t, setup.Secret, time.Now().Add(30*time.Second))
	raw, err := svc.CreateLoginChallenge(ctx, user.ID, "/settings/profile")
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	uid, next, err := svc.VerifyLoginChallenge(ctx, raw, nextCode)
	if err != nil || uid != user.ID || next != "/settings/profile" {
		t.Fatalf("verify challenge: uid=%q next=%q err=%v", uid, next, err)
	}
	// The same time step must not be replayable.
	raw, _ = svc.CreateLoginChallenge(ctx, user.ID, "/")
	if _, _, err := svc.VerifyLoginChallenge(ctx, raw, nextCode); err == nil {
		t.Fatal("replayed TOTP code accepted")
	}

	// Recovery codes are case-insensitive, accept a missing hyphen, and are
	// single use.
	recovery := strings.ToLower(strings.ReplaceAll(codes[0], "-", ""))
	raw, _ = svc.CreateLoginChallenge(ctx, user.ID, "/")
	if _, _, err := svc.VerifyLoginChallenge(ctx, raw, recovery); err != nil {
		t.Fatalf("recovery login: %v", err)
	}
	if n := svc.RecoveryCodesRemaining(ctx, user.ID); n != recoveryCodeCount-1 {
		t.Fatalf("recovery remaining after use = %d", n)
	}
	raw, _ = svc.CreateLoginChallenge(ctx, user.ID, "/")
	if _, _, err := svc.VerifyLoginChallenge(ctx, raw, recovery); err == nil {
		t.Fatal("reused recovery code accepted")
	}
}

func TestAdminTOTPPolicy(t *testing.T) {
	svc, _, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	admin, err := svc.CreateAdmin(ctx, "policyroot", "policy@example.com", "policy12345678")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	normal, err := svc.createUser(ctx, "policynormal", "normal@example.com", "normal12345678", "active", false, 0)
	if err != nil {
		t.Fatalf("create normal: %v", err)
	}
	normalView := toView(normal)

	if svc.AdminTOTPPolicy() != "superadmin" {
		t.Fatalf("default policy = %q", svc.AdminTOTPPolicy())
	}
	if !svc.AdminTOTPRequired(ctx, admin.ID, true) {
		t.Fatal("superadmin should require 2FA by default")
	}
	if svc.AdminTOTPRequired(ctx, normal.ID, false) {
		t.Fatal("normal user should not require 2FA by default")
	}
	if mode := svc.TwoFactorMode(ctx, admin); mode != "setup" {
		t.Fatalf("admin mode = %q", mode)
	}
	if mode := svc.TwoFactorMode(ctx, normalView); mode != "" {
		t.Fatalf("normal mode = %q", mode)
	}

	setSetting(t, store, "site.security.admin_2fa_policy", "off")
	if svc.AdminTOTPRequired(ctx, admin.ID, true) {
		t.Fatal("policy off still requires 2FA")
	}

	setSetting(t, store, "site.security.admin_2fa_policy", "admins")
	group, err := svc.CreateGroup(ctx, contracts.GroupInput{
		Name: "Backoffice", Slug: "backoffice", Permissions: []string{"admin.access"},
	})
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := svc.SetGroups(ctx, normal.ID, []string{group.ID}); err != nil {
		t.Fatalf("assign group: %v", err)
	}
	if !svc.AdminTOTPRequired(ctx, normal.ID, false) {
		t.Fatal("admin group member should require 2FA under admins policy")
	}
}

func TestDisableTOTP(t *testing.T) {
	svc, _, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "disabletotp", "disable@example.com", "disable12345678")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setup, err := svc.EnsureTOTPSetup(ctx, user.ID)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := svc.EnableTOTP(ctx, user.ID, mustTOTPCode(t, setup.Secret, time.Now())); err != nil {
		t.Fatalf("enable: %v", err)
	}
	nextCode := mustTOTPCode(t, setup.Secret, time.Now().Add(30*time.Second))

	if err := svc.DisableTOTP(ctx, user.ID, "wrong-password", nextCode); err == nil {
		t.Fatal("wrong password disabled 2FA")
	}
	err = svc.DisableTOTP(ctx, user.ID, "disable12345678", nextCode)
	if err == nil || httpx.ErrorOf(err).Code != "auth.totp_required" {
		t.Fatalf("required policy should block disable, got %v", err)
	}
	setSetting(t, store, "site.security.admin_2fa_policy", "off")
	if err := svc.DisableTOTP(ctx, user.ID, "disable12345678", nextCode); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, err := svc.GetByID(ctx, user.ID)
	if err != nil || got.TOTPEnabled {
		t.Fatalf("after disable = %+v err=%v", got, err)
	}
	if n := svc.RecoveryCodesRemaining(ctx, user.ID); n != 0 {
		t.Fatalf("recovery codes remain: %d", n)
	}
}

func TestTOTPSetupRequiresSecretKey(t *testing.T) {
	svc, db, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "nokey", "nokey@example.com", "nokey12345678")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	bare := New(db)
	bare.Configure(store, nil, "https://forum.test")
	if _, err := bare.EnsureTOTPSetup(ctx, user.ID); err == nil {
		t.Fatal("setup without APP_KEY should fail")
	}
}

func TestTwoFactorModeSkipsDevelopment(t *testing.T) {
	svc, _, _, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "devadmin", "devadmin@example.com", "devadmin123456")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	svc.SetEnv("development")
	if svc.AdminTOTPRequired(ctx, user.ID, true) {
		t.Fatal("development should not require TOTP")
	}
	if mode := svc.TwoFactorMode(ctx, user); mode != "" {
		t.Fatalf("development mode = %q", mode)
	}
	svc.SetEnv("production")
	if mode := svc.TwoFactorMode(ctx, user); mode != "setup" {
		t.Fatalf("production mode = %q", mode)
	}
}
