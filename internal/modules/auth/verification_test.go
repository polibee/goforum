package auth

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/platform/httpx"
	"goforum/internal/platform/mail"
	"goforum/internal/platform/settings"
)

func testServiceWithSettings(t *testing.T) (*Service, *gorm.DB, *settings.Store, *captureMailer) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "auth.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(append(Models(), settings.Models()...)...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := settings.NewRegistry()
	if err := reg.Register(settings.PlatformDefinitions()...); err != nil {
		t.Fatalf("settings defs: %v", err)
	}
	store, err := settings.NewStore(db, reg)
	if err != nil {
		t.Fatalf("settings store: %v", err)
	}
	mailer := &captureMailer{}
	svc := New(db)
	svc.Configure(store, mailer, "https://forum.test")
	svc.SetSecretKey("test-secret-key")
	return svc, db, store, mailer
}

func setSetting(t *testing.T, store *settings.Store, key string, value any) {
	t.Helper()
	if err := store.Set(context.Background(), key, value, "test"); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func TestRegistrationVerificationDisabled(t *testing.T) {
	svc, _, store, mailer := testServiceWithSettings(t)
	ctx := context.Background()

	// An account created while verification was required stays pending.
	pending, err := svc.Register(ctx, "pendinguser", "pending@example.com", "pending123456", "")
	if err != nil {
		t.Fatalf("register pending: %v", err)
	}
	if pending.User.Status != "pending_email" {
		t.Fatalf("status = %q", pending.User.Status)
	}

	setSetting(t, store, "site.registration.email_verification", false)
	if _, err := svc.Authenticate(ctx, "pending@example.com", "pending123456"); err != nil {
		t.Fatalf("legacy pending account should log in after verification is disabled: %v", err)
	}
	got, err := svc.GetByID(ctx, pending.User.ID)
	if err != nil || got.Status != "active" {
		t.Fatalf("pending account status = %+v err=%v", got, err)
	}

	mailer.last = mail.Message{}
	res, err := svc.Register(ctx, "directuser", "direct@example.com", "direct123456", "")
	if err != nil {
		t.Fatalf("register direct: %v", err)
	}
	if res.User.Status != "active" || res.VerifyToken != "" {
		t.Fatalf("direct registration = %+v", res)
	}
	if mailer.last.Text != "" {
		t.Fatalf("verification mail should not be sent: %q", mailer.last.Text)
	}
	if _, err := svc.Authenticate(ctx, "direct@example.com", "direct123456"); err != nil {
		t.Fatalf("direct login: %v", err)
	}
}

func TestPasswordResetToggle(t *testing.T) {
	svc, _, store, mailer := testServiceWithSettings(t)
	ctx := context.Background()
	if _, err := svc.CreateAdmin(ctx, "resetuser", "reset@example.com", "reset12345678"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.RequestPasswordReset(ctx, "reset@example.com"); err != nil {
		t.Fatalf("request reset: %v", err)
	}
	match := regexp.MustCompile(`/reset-password\?token=([A-Za-z0-9_-]+)`).FindStringSubmatch(mailer.last.Text)
	if len(match) != 2 {
		t.Fatalf("reset link missing: %q", mailer.last.Text)
	}
	token := match[1]

	setSetting(t, store, "site.verification.password_reset", false)
	if err := svc.RequestPasswordReset(ctx, "reset@example.com"); err == nil {
		t.Fatal("reset request should be rejected when disabled")
	} else if httpx.ErrorOf(err).Code != "auth.password_reset_disabled" {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := svc.ResetPassword(ctx, token, "changed123456"); err == nil {
		t.Fatal("reset must be rejected when disabled")
	}

	setSetting(t, store, "site.verification.password_reset", true)
	if _, err := svc.ResetPassword(ctx, token, "changed123456"); err != nil {
		t.Fatalf("reset after re-enable: %v", err)
	}
}

func TestPasswordChangeToggle(t *testing.T) {
	svc, _, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "changeuser", "change@example.com", "change12345678")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setSetting(t, store, "site.verification.password_change", false)
	if err := svc.ChangePassword(ctx, user.ID, "change12345678", "changed12345678", ""); err == nil {
		t.Fatal("password change should be rejected when disabled")
	} else if httpx.ErrorOf(err).Code != "auth.password_change_disabled" {
		t.Fatalf("unexpected error: %v", err)
	}

	setSetting(t, store, "site.verification.password_change", true)
	if err := svc.ChangePassword(ctx, user.ID, "change12345678", "changed12345678", ""); err != nil {
		t.Fatalf("change after re-enable: %v", err)
	}
}

func TestPasswordChangeEmailCode(t *testing.T) {
	svc, _, store, mailer := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "codeuser", "code@example.com", "code12345678")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setSetting(t, store, "site.verification.password_change_verify", true)

	if err := svc.ChangePassword(ctx, user.ID, "code12345678", "changed12345678", ""); err == nil {
		t.Fatal("password change without code must fail")
	}
	code, err := svc.RequestPasswordChangeCode(ctx, user.ID)
	if err != nil {
		t.Fatalf("request code: %v", err)
	}
	if len(code) != 6 || !allDigits(code) {
		t.Fatalf("code = %q", code)
	}
	if !strings.Contains(mailer.last.Text, code) {
		t.Fatalf("code missing from mail: %q", mailer.last.Text)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	if err := svc.ChangePassword(ctx, user.ID, "code12345678", "changed12345678", wrong); err == nil {
		t.Fatal("wrong code must fail")
	}
	if err := svc.ChangePassword(ctx, user.ID, "code12345678", "changed12345678", code); err != nil {
		t.Fatalf("change with code: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "code@example.com", "changed12345678"); err != nil {
		t.Fatalf("new password login: %v", err)
	}
	if err := svc.ChangePassword(ctx, user.ID, "changed12345678", "another12345678", code); err == nil {
		t.Fatal("code must be single use")
	}
}

func TestEmailChangeVerificationToggle(t *testing.T) {
	svc, _, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "mailchange", "old-mail@example.com", "mailchange12345")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	setSetting(t, store, "site.verification.email_change", false)
	if _, err := svc.RequestEmailChange(ctx, user.ID, "mailchange12345", "blocked@example.com"); err == nil {
		t.Fatal("email change should be rejected when disabled")
	}

	setSetting(t, store, "site.verification.email_change", true)
	setSetting(t, store, "site.verification.email_change_verify", false)
	raw, err := svc.RequestEmailChange(ctx, user.ID, "mailchange12345", "new-mail@example.com")
	if err != nil {
		t.Fatalf("direct email change: %v", err)
	}
	if raw != "" {
		t.Fatalf("direct change should not return a token: %q", raw)
	}
	got, err := svc.GetByID(ctx, user.ID)
	if err != nil || got.Email != "new-mail@example.com" || got.EmailVerifiedAt == nil {
		t.Fatalf("user after direct change = %+v err=%v", got, err)
	}
	if _, err := svc.Authenticate(ctx, "new-mail@example.com", "mailchange12345"); err != nil {
		t.Fatalf("login with new email: %v", err)
	}
}
