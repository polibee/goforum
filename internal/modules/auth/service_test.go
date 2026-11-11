package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func testService(t *testing.T) (*Service, *gorm.DB) {
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
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db), db
}

func TestCreateAdminAndAuthenticate(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()

	user, err := svc.CreateAdmin(ctx, "Admin", "Admin@Example.com", "admin12345678")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if !user.SuperAdmin {
		t.Fatal("first admin must be super admin")
	}
	if user.Username != "Admin" {
		t.Fatalf("username = %q", user.Username)
	}

	if _, err := svc.Authenticate(ctx, "admin", "admin12345678"); err != nil {
		t.Fatalf("authenticate by username: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "admin@example.com", "admin12345678"); err != nil {
		t.Fatalf("authenticate by email: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "admin", "wrong-password"); err == nil {
		t.Fatal("wrong password accepted")
	}

	count, err := svc.Count(ctx)
	if err != nil || count != 1 {
		t.Fatalf("count = %d, err = %v", count, err)
	}
}

func TestUpdateProfileSignatureLimits(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "siguser", "sig@example.com", "signature12345")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.UpdateProfile(ctx, user.ID, "", strings.Repeat("好", 301), "", true, false, "", "", "", ""); err == nil {
		t.Fatal("overlong signature accepted")
	}
	if _, err := svc.UpdateProfile(ctx, user.ID, "", "1\n2\n3\n4\n5\n6\n7", "", true, false, "", "", "", ""); err == nil {
		t.Fatal("too many signature lines accepted")
	}
	got, err := svc.UpdateProfile(ctx, user.ID, "", "**hi**\n\n[link](/about)", "", true, false, "", "", "", "")
	if err != nil {
		t.Fatalf("valid signature: %v", err)
	}
	if got.Signature != "**hi**\n\n[link](/about)" {
		t.Fatalf("signature = %q", got.Signature)
	}
}

func TestUpdateProfileBioCard(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "biouser", "bio@example.com", "biocard12345")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := svc.UpdateProfile(ctx, user.ID, "## 关于我\n\n- Go", "", "", true, false, "", "", "", "")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !got.BioEnabled || got.Bio != "## 关于我\n\n- Go" {
		t.Fatalf("bio = %q enabled = %v", got.Bio, got.BioEnabled)
	}
	got, err = svc.UpdateProfile(ctx, user.ID, "## 关于我\n\n- Go", "", "", false, false, "", "", "", "")
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got.BioEnabled {
		t.Fatal("bio card should be disabled")
	}
}

func TestCreateAdminValidation(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()

	if _, err := svc.CreateAdmin(ctx, "ab", "not-an-email", "short"); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := svc.CreateAdmin(ctx, "admin", "admin@example.com", "admin12345678"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.CreateAdmin(ctx, "admin", "other@example.com", "admin12345678"); err == nil {
		t.Fatal("duplicate username accepted")
	}
}

func TestTimedStatusExpires(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	u, err := svc.CreateAdmin(ctx, "timeduser", "timed@example.com", "timed1234567")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	future := time.Now().UTC().Add(time.Hour)
	if err := svc.SetStatusUntil(ctx, u.ID, "suspended", &future); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	got, err := svc.GetByID(ctx, u.ID)
	if err != nil || got.Status != "suspended" || got.StatusUntil == nil {
		t.Fatalf("active suspension = %+v err=%v", got, err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := svc.SetStatusUntil(ctx, u.ID, "suspended", &past); err != nil {
		t.Fatalf("suspend past: %v", err)
	}
	got, err = svc.GetByID(ctx, u.ID)
	if err != nil || got.Status != "active" || got.StatusUntil != nil {
		t.Fatalf("expired status = %+v err=%v", got, err)
	}
}

func TestListFilteredUsers(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	if _, err := svc.CreateAdmin(ctx, "alice", "alice@example.com", "alice12345678"); err != nil {
		t.Fatalf("alice: %v", err)
	}
	if _, err := svc.CreateAdmin(ctx, "bob", "bob@example.com", "bob12345678"); err != nil {
		t.Fatalf("bob: %v", err)
	}
	rows, total, err := svc.ListFiltered(ctx, "ali", "", false, 20, 0)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Username != "alice" {
		t.Fatalf("filter = %+v total=%d err=%v", rows, total, err)
	}
	if err := svc.SetStatus(ctx, rows[0].ID, "banned"); err != nil {
		t.Fatalf("ban: %v", err)
	}
	rows, total, err = svc.ListFiltered(ctx, "", "banned", false, 20, 0)
	if err != nil || total != 1 || rows[0].Username != "alice" {
		t.Fatalf("status filter = %+v total=%d err=%v", rows, total, err)
	}
}

func TestChangePassword(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "pwuser", "pw@example.com", "oldpassword123")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.ChangePassword(ctx, user.ID, "wrong-password", "newpassword123", ""); err == nil {
		t.Fatal("wrong current password must fail")
	}
	if err := svc.ChangePassword(ctx, user.ID, "oldpassword123", "oldpassword123", ""); err == nil {
		t.Fatal("same password must fail")
	}
	if err := svc.ChangePassword(ctx, user.ID, "oldpassword123", "newpassword123", ""); err != nil {
		t.Fatalf("change: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "pw@example.com", "oldpassword123"); err == nil {
		t.Fatal("old password should no longer work")
	}
	if _, err := svc.Authenticate(ctx, "pw@example.com", "newpassword123"); err != nil {
		t.Fatalf("new password should work: %v", err)
	}
}

func TestEmailChangeVerification(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	user, err := svc.CreateAdmin(ctx, "mailuser", "old@example.com", "password12345")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.RequestEmailChange(ctx, user.ID, "wrong", "new@example.com"); err == nil {
		t.Fatal("wrong password must fail")
	}
	raw, err := svc.RequestEmailChange(ctx, user.ID, "password12345", "new@example.com")
	if err != nil || raw == "" {
		t.Fatalf("request: %v", err)
	}
	if err := svc.ConfirmEmailChange(ctx, raw, "someone-else"); err == nil {
		t.Fatal("other user must not confirm")
	}
	if err := svc.ConfirmEmailChange(ctx, raw, user.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	got, err := svc.GetByID(ctx, user.ID)
	if err != nil || got.Email != "new@example.com" || got.EmailVerifiedAt == nil {
		t.Fatalf("user = %+v err=%v", got, err)
	}
	if err := svc.ConfirmEmailChange(ctx, raw, user.ID); err == nil {
		t.Fatal("token must be single use")
	}
	if _, err := svc.Authenticate(ctx, "new@example.com", "password12345"); err != nil {
		t.Fatalf("login with new email: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "old@example.com", "password12345"); err == nil {
		t.Fatal("old email should no longer work")
	}
}
