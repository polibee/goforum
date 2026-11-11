package auth

import (
	"context"
	"testing"
	"time"

	"goforum/internal/platform/httpx"
	"goforum/internal/platform/mail"
)

func quotaCount(t *testing.T, svc *Service, userID string) int64 {
	t.Helper()
	var n int64
	if err := svc.db.Model(&EmailSendLog{}).Where("user_id = ?", userID).Count(&n).Error; err != nil {
		t.Fatalf("count quota logs: %v", err)
	}
	return n
}

func TestEmailDailyQuota(t *testing.T) {
	svc, _, store, mailer := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "quota", "quota@example.com", "quota12345678")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setSetting(t, store, "site.verification.email_daily_limit", 2)
	setSetting(t, store, "site.verification.email_total_limit", 0)

	for i := 0; i < 2; i++ {
		if err := svc.RequestPasswordReset(ctx, "quota@example.com"); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	mailer.last = mail.Message{}
	err = svc.RequestPasswordReset(ctx, "quota@example.com")
	if err == nil {
		t.Fatal("third request should be blocked by daily quota")
	}
	if httpx.ErrorOf(err).Code != "auth.email_daily_limit" {
		t.Fatalf("unexpected error: %v", err)
	}
	if mailer.last.Text != "" {
		t.Fatalf("blocked request must not send mail: %q", mailer.last.Text)
	}
	if got := quotaCount(t, svc, user.ID); got != 2 {
		t.Fatalf("quota logs = %d, want 2", got)
	}
}

func TestEmailLifetimeQuota(t *testing.T) {
	svc, _, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "lifetime", "lifetime@example.com", "lifetime123456")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setSetting(t, store, "site.verification.email_daily_limit", 0)
	setSetting(t, store, "site.verification.email_total_limit", 2)

	for i := 0; i < 2; i++ {
		if err := svc.RequestPasswordReset(ctx, "lifetime@example.com"); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	err = svc.RequestPasswordReset(ctx, "lifetime@example.com")
	if err == nil {
		t.Fatal("third request should be blocked by lifetime quota")
	}
	if httpx.ErrorOf(err).Code != "auth.email_total_limit" {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := quotaCount(t, svc, user.ID); got != 2 {
		t.Fatalf("quota logs = %d, want 2", got)
	}
}

func TestEmailDailyQuotaIgnoresPreviousDays(t *testing.T) {
	svc, _, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "dayroll", "dayroll@example.com", "dayroll123456")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setSetting(t, store, "site.verification.email_daily_limit", 1)
	setSetting(t, store, "site.verification.email_total_limit", 0)

	if err := svc.db.Create(&EmailSendLog{
		UserID: user.ID, Purpose: "password_reset", CreatedAt: time.Now().UTC().Add(-24 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed old log: %v", err)
	}
	if err := svc.RequestPasswordReset(ctx, "dayroll@example.com"); err != nil {
		t.Fatalf("request on a new day should pass: %v", err)
	}
	if err := svc.RequestPasswordReset(ctx, "dayroll@example.com"); err == nil {
		t.Fatal("second request on the same day should be blocked")
	}
}

func TestEmailQuotaCoverRegistrationAndResend(t *testing.T) {
	svc, _, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	setSetting(t, store, "site.verification.email_total_limit", 1)
	setSetting(t, store, "site.verification.email_daily_limit", 0)

	res, err := svc.Register(ctx, "regquota", "regquota@example.com", "regquota123456", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if res.User.Status != "pending_email" {
		t.Fatalf("status = %q", res.User.Status)
	}
	if got := quotaCount(t, svc, res.User.ID); got != 1 {
		t.Fatalf("registration logs = %d, want 1", got)
	}
	err = svc.ResendVerification(ctx, "regquota@example.com")
	if err == nil || httpx.ErrorOf(err).Code != "auth.email_total_limit" {
		t.Fatalf("resend should hit lifetime quota, got %v", err)
	}
}

func TestEmailQuotaCoversPasswordChangeCode(t *testing.T) {
	svc, _, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "codequota", "codequota@example.com", "codequota123456")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setSetting(t, store, "site.verification.password_change_verify", true)
	setSetting(t, store, "site.verification.email_daily_limit", 1)
	setSetting(t, store, "site.verification.email_total_limit", 0)

	if _, err := svc.RequestPasswordChangeCode(ctx, user.ID); err != nil {
		t.Fatalf("first code: %v", err)
	}
	if _, err := svc.RequestPasswordChangeCode(ctx, user.ID); err == nil {
		t.Fatal("second code on the same day should be blocked")
	}
}

func TestEmailQuotaCoversDirectEmailChange(t *testing.T) {
	svc, _, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "mailquota", "mailquota@example.com", "mailquota123456")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setSetting(t, store, "site.verification.email_change_verify", false)
	setSetting(t, store, "site.verification.email_daily_limit", 1)
	setSetting(t, store, "site.verification.email_total_limit", 0)

	if _, err := svc.RequestEmailChange(ctx, user.ID, "mailquota123456", "first@example.com"); err != nil {
		t.Fatalf("first change: %v", err)
	}
	_, err = svc.RequestEmailChange(ctx, user.ID, "mailquota123456", "second@example.com")
	if err == nil || httpx.ErrorOf(err).Code != "auth.email_daily_limit" {
		t.Fatalf("second change should hit daily quota, got %v", err)
	}
}

func TestZeroEmailQuotaMeansUnlimited(t *testing.T) {
	svc, _, store, _ := testServiceWithSettings(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "unlimited", "unlimited@example.com", "unlimited123456")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setSetting(t, store, "site.verification.email_daily_limit", 0)
	setSetting(t, store, "site.verification.email_total_limit", 0)

	for i := 0; i < 12; i++ {
		if err := svc.RequestPasswordReset(ctx, "unlimited@example.com"); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	if got := quotaCount(t, svc, user.ID); got != 0 {
		t.Fatalf("unlimited quota should not write logs, got %d", got)
	}
}
