package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
)

type fakeActivity struct{ posts int64 }

func (f fakeActivity) UserActivity(context.Context, string) (int64, int64, int64, error) {
	return 0, f.posts, 0, nil
}

func (f fakeActivity) UserActivityBatch(_ context.Context, userIDs []string) (map[string]contracts.ActivityCounts, error) {
	out := make(map[string]contracts.ActivityCounts, len(userIDs))
	for _, id := range userIDs {
		out[id] = contracts.ActivityCounts{Posts: f.posts}
	}
	return out, nil
}

func setupGroups(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "groups.db")), &gorm.Config{})
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
	svc := New(db)
	ctx := context.Background()
	if err := svc.EnsureDefaultGroup(ctx); err != nil {
		t.Fatalf("default group: %v", err)
	}
	return svc, db
}

func TestGroupsAndPermissions(t *testing.T) {
	svc, _ := setupGroups(t)
	ctx := context.Background()

	user, err := svc.createUser(ctx, "bob", "bob@example.com", "bob12345678", "active", false, 0)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	perms, err := svc.PermissionsOf(ctx, user.ID)
	if err != nil {
		t.Fatalf("permissions: %v", err)
	}
	if _, ok := perms["topic.create"]; !ok {
		t.Fatal("default permissions missing topic.create")
	}

	group, err := svc.CreateGroup(ctx, contracts.GroupInput{
		Name: "Moderators", Slug: "moderators",
		Permissions: []string{"topic.moderate", "admin.access"},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := svc.SetGroups(ctx, user.ID, []string{group.ID}); err != nil {
		t.Fatalf("set groups: %v", err)
	}
	perms, _ = svc.PermissionsOf(ctx, user.ID)
	if _, ok := perms["topic.moderate"]; !ok {
		t.Fatal("group permission missing")
	}
	if _, ok := perms["topic.create"]; ok {
		t.Fatal("default permission should not apply when explicit groups exist")
	}

	groups, err := svc.GroupsOf(ctx, user.ID)
	if err != nil || len(groups) != 1 || groups[0].Name != "Moderators" {
		t.Fatalf("groups = %+v, err=%v", groups, err)
	}

	all, _ := svc.ListGroups(ctx)
	if len(all) < 2 {
		t.Fatalf("expected default + moderator groups, got %d", len(all))
	}
}

func TestTrustLevelRecompute(t *testing.T) {
	svc, db := setupGroups(t)
	ctx := context.Background()

	user, err := svc.createUser(ctx, "carol", "carol@example.com", "carol12345678", "active", false, 0)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := db.Model(&User{}).Where("id = ?", user.ID).Update("created_at", old).Error; err != nil {
		t.Fatalf("age user: %v", err)
	}
	svc.SetActivity(fakeActivity{posts: 1})
	changed, err := svc.RecomputeTrustLevels(ctx)
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if changed == 0 {
		t.Fatal("expected a trust-level change")
	}
	view, _ := svc.GetByID(ctx, user.ID)
	if view.TrustLevel != 1 {
		t.Fatalf("trust level = %d", view.TrustLevel)
	}
	if err := svc.SetTrustLevel(ctx, user.ID, 4); err != nil {
		t.Fatalf("set trust: %v", err)
	}
	view, _ = svc.GetByID(ctx, user.ID)
	if view.TrustLevel != 4 {
		t.Fatalf("manual trust level = %d", view.TrustLevel)
	}
	if err := svc.SetTrustLevel(ctx, user.ID, 9); err == nil {
		t.Fatal("invalid trust level accepted")
	}
}

func TestEnsureModeratorGroup(t *testing.T) {
	svc, _ := setupGroups(t)
	ctx := context.Background()
	if err := svc.EnsureModeratorGroup(ctx); err != nil {
		t.Fatalf("ensure moderator: %v", err)
	}
	groups, err := svc.ListGroups(ctx)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	var found *contracts.GroupView
	for i := range groups {
		if groups[i].Slug == "moderators" {
			found = &groups[i]
		}
	}
	if found == nil {
		t.Fatal("moderator group missing")
	}
	want := map[string]bool{"admin.access": false, "moderation.queue.view": false, "moderation.action": false}
	for _, p := range found.Permissions {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, ok := range want {
		if !ok {
			t.Fatalf("moderator missing %s", p)
		}
	}
}
