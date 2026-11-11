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

type captureMailer struct {
	last mail.Message
}

func (m *captureMailer) Send(_ context.Context, msg mail.Message) error {
	m.last = msg
	return nil
}

func TestRegisterVerifyAndReset(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "auth.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatalf("migrate auth: %v", err)
	}
	if err := db.AutoMigrate(settings.Models()...); err != nil {
		t.Fatalf("migrate settings: %v", err)
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

	ctx := context.Background()
	res, err := svc.Register(ctx, "alice", "alice@example.com", "alice12345678", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if res.User.Status != "pending_email" {
		t.Fatalf("status = %q", res.User.Status)
	}
	if res.VerifyToken == "" {
		t.Fatal("verify token missing")
	}
	if !strings.Contains(mailer.last.Text, "/verify-email?token=") {
		t.Fatalf("verification mail missing link: %q", mailer.last.Text)
	}

	if _, err := svc.Authenticate(ctx, "alice", "alice12345678"); err == nil {
		t.Fatal("unverified user logged in")
	} else if httpx.ErrorOf(err).Code != "auth.email_unverified" {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := svc.VerifyEmail(ctx, res.VerifyToken); err != nil {
		t.Fatalf("verify: %v", err)
	}
	u, err := svc.GetByID(ctx, res.User.ID)
	if err != nil || u.Status != "active" || u.EmailVerifiedAt == nil {
		t.Fatalf("after verify: %+v, err=%v", u, err)
	}
	if _, err := svc.Authenticate(ctx, "alice@example.com", "alice12345678"); err != nil {
		t.Fatalf("login after verify: %v", err)
	}

	if err := svc.RequestPasswordReset(ctx, "alice@example.com"); err != nil {
		t.Fatalf("request reset: %v", err)
	}
	re := regexp.MustCompile(`/reset-password\?token=([A-Za-z0-9_-]+)`)
	match := re.FindStringSubmatch(mailer.last.Text)
	if len(match) != 2 {
		t.Fatalf("reset link missing: %q", mailer.last.Text)
	}
	userID, err := svc.ResetPassword(ctx, match[1], "newpassword123")
	if err != nil || userID != res.User.ID {
		t.Fatalf("reset: user=%q err=%v", userID, err)
	}
	if _, err := svc.Authenticate(ctx, "alice", "newpassword123"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "alice", "alice12345678"); err == nil {
		t.Fatal("old password still works")
	}
}

func TestRegisterValidation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "authv.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	_ = db.AutoMigrate(Models()...)
	_ = db.AutoMigrate(settings.Models()...)

	reg := settings.NewRegistry()
	_ = reg.Register(settings.PlatformDefinitions()...)
	store, _ := settings.NewStore(db, reg)
	svc := New(db)
	svc.Configure(store, &captureMailer{}, "https://forum.test")

	ctx := context.Background()
	if _, err := svc.Register(ctx, "ab", "bad", "short", ""); err == nil {
		t.Fatal("invalid registration accepted")
	}
	if _, err := svc.Register(ctx, "alice", "alice@example.com", "alice12345678", ""); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := svc.Register(ctx, "alice", "other@example.com", "alice12345678", ""); err == nil {
		t.Fatal("duplicate username accepted")
	}
}
