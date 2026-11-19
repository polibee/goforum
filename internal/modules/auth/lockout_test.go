package auth

import (
	"context"
	"testing"
	"time"

	"goforum/internal/platform/cache"
	"goforum/internal/platform/httpx"
)

func TestPasswordLoginLocksAfterRepeatedFailures(t *testing.T) {
	svc, _ := testService(t)
	svc.SetCache(cache.NewMemory(5 * time.Minute))
	ctx := context.Background()
	if _, err := svc.CreateAdmin(ctx, "lockme", "lock@example.com", "correct-password-123"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxAuthFailures; i++ {
		if _, err := svc.Authenticate(ctx, "lockme", "wrong-password"); err == nil {
			t.Fatalf("attempt %d accepted", i)
		}
	}
	_, err := svc.Authenticate(ctx, "lockme", "correct-password-123")
	if err == nil {
		t.Fatal("correct password accepted while locked")
	}
	if httpx.ErrorOf(err).Status != 429 {
		t.Fatalf("status = %d, want 429", httpx.ErrorOf(err).Status)
	}
}

func TestTOTPChallengeLocksAfterRepeatedFailures(t *testing.T) {
	svc, _, _, _ := testServiceWithSettings(t)
	svc.SetCache(cache.NewMemory(5 * time.Minute))
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "totplock", "totplock@example.com", "totp-password-123")
	if err != nil {
		t.Fatal(err)
	}
	setup, err := svc.EnsureTOTPSetup(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := mustTOTPCode(t, setup.Secret, time.Now())
	if _, err := svc.EnableTOTP(ctx, user.ID, code); err != nil {
		t.Fatal(err)
	}
	raw, err := svc.CreateLoginChallenge(ctx, user.ID, "/")
	if err != nil {
		t.Fatal(err)
	}
	badCode := mustTOTPCode(t, setup.Secret, time.Now().Add(10*time.Minute))
	for i := 0; i < maxAuthFailures; i++ {
		if _, _, err := svc.VerifyLoginChallenge(ctx, raw, badCode); err == nil {
			t.Fatalf("attempt %d accepted", i)
		}
	}
	_, _, err = svc.VerifyLoginChallenge(ctx, raw, mustTOTPCode(t, setup.Secret, time.Now().Add(30*time.Second)))
	if err == nil || httpx.ErrorOf(err).Status != 429 {
		t.Fatalf("err = %v, want 429", err)
	}
}
