package engagement

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	cmod "goforum/internal/modules/community/contracts"
)

type fakeContent struct{}

func (fakeContent) TopicMeta(_ context.Context, id string) (*cmod.TopicMeta, error) {
	return &cmod.TopicMeta{ID: id, Number: 1, AuthorID: "author", Title: "Topic"}, nil
}

func (fakeContent) PostMeta(_ context.Context, id string) (*cmod.PostMeta, error) {
	return &cmod.PostMeta{ID: id, TopicID: "topic1", TopicNumber: 1, TopicTitle: "Topic", AuthorID: "author", PostNumber: 1}, nil
}

func (fakeContent) TopicMetaBatch(_ context.Context, ids []string) (map[string]cmod.TopicMeta, error) {
	out := make(map[string]cmod.TopicMeta, len(ids))
	for _, id := range ids {
		out[id] = cmod.TopicMeta{ID: id, Number: 1, AuthorID: "author", Title: "Topic"}
	}
	return out, nil
}

func (fakeContent) PostMetaBatch(_ context.Context, ids []string) (map[string]cmod.PostMeta, error) {
	out := make(map[string]cmod.PostMeta, len(ids))
	for _, id := range ids {
		out[id] = cmod.PostMeta{ID: id, TopicID: "topic1", TopicNumber: 1, TopicTitle: "Topic", AuthorID: "author", PostNumber: 1}
	}
	return out, nil
}

func testService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "engagement.db")), &gorm.Config{})
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
	// Minimal joined tables used by ListSubscriptions; the real app migrates
	// them from the auth/community modules.
	for _, ddl := range []string{
		"CREATE TABLE users (id text primary key, username text, avatar_url text, trust_level integer)",
		"CREATE TABLE topics (id text primary key, title text, number integer, reply_count integer, deleted_at datetime)",
		"CREATE TABLE categories (id text primary key, name text, slug text)",
		"CREATE TABLE tags (id text primary key, name text, slug text)",
		"INSERT INTO users (id, username, trust_level) VALUES ('u4', 'user-u4', 1)",
		"INSERT INTO topics (id, title, number, reply_count) VALUES ('t1', 'Topic', 1, 0)",
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	svc := NewService(db)
	svc.Configure(fakeContent{}, nil)
	return svc
}

func TestReactionToggleIdempotent(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()

	active, count, err := svc.ToggleReaction(ctx, "u1", "post", "p1", "like")
	if err != nil || !active || count != 1 {
		t.Fatalf("first like: active=%v count=%d err=%v", active, count, err)
	}
	active, count, err = svc.ToggleReaction(ctx, "u1", "post", "p1", "like")
	if err != nil || active || count != 0 {
		t.Fatalf("unlike: active=%v count=%d err=%v", active, count, err)
	}
	if _, _, err := svc.ToggleReaction(ctx, "u1", "unknown", "x", "like"); err != nil {
		t.Fatalf("unknown target should not fail hard: %v", err)
	}
}

func TestBookmarkAndSubscriptionState(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()

	active, count, err := svc.ToggleBookmark(ctx, "u1", "topic", "t1")
	if err != nil || !active || count != 1 {
		t.Fatalf("bookmark: active=%v count=%d err=%v", active, count, err)
	}
	if _, _, err := svc.ToggleReaction(ctx, "u1", "post", "p1", "like"); err != nil {
		t.Fatalf("like: %v", err)
	}
	if _, err := svc.ToggleSubscription(ctx, "u1", "topic", "t1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	liked, bookmarked, following, err := svc.TopicState(ctx, "u1", "t1", []string{"p1", "p2"})
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !liked["p1"] || liked["p2"] {
		t.Fatalf("liked = %v", liked)
	}
	if !bookmarked || !following {
		t.Fatalf("bookmarked=%v following=%v", bookmarked, following)
	}
	followers, err := svc.TopicFollowers(ctx, "t1", 10)
	if err != nil || len(followers) != 1 || followers[0] != "u1" {
		t.Fatalf("topic followers = %v err=%v", followers, err)
	}
	subs, total, err := svc.ListSubscriptions(ctx, "u1", "topic", 10, 0)
	if err != nil || total != 1 || len(subs) != 1 || subs[0].URL != "/t/1" {
		t.Fatalf("topic subscriptions = %+v total=%d err=%v", subs, total, err)
	}

	rows, total, err := svc.ListBookmarks(ctx, "u1", 20, 0)
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("bookmarks = %d/%d err=%v", len(rows), total, err)
	}
	if rows[0].Title != "Topic" || rows[0].URL != "/t/1" {
		t.Fatalf("row = %+v", rows[0])
	}
}

func TestFollowStatsAndLists(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	for _, pair := range [][2]string{{"u1", "u2"}, {"u3", "u2"}, {"u2", "u4"}} {
		if _, err := svc.ToggleSubscription(ctx, pair[0], "user", pair[1]); err != nil {
			t.Fatalf("follow: %v", err)
		}
	}
	followers, following, err := svc.FollowStats(ctx, "u2")
	if err != nil || followers != 2 || following != 1 {
		t.Fatalf("stats = %d/%d err=%v", followers, following, err)
	}
	ids, total, err := svc.ListFollowers(ctx, "u2", 10, 0)
	if err != nil || total != 2 || len(ids) != 2 {
		t.Fatalf("followers = %v/%d err=%v", ids, total, err)
	}
	ids, total, err = svc.ListFollowing(ctx, "u2", 10, 0)
	if err != nil || total != 1 || len(ids) != 1 || ids[0] != "u4" {
		t.Fatalf("following = %v/%d err=%v", ids, total, err)
	}
	subs, total, err := svc.ListSubscriptions(ctx, "u2", "user", 10, 0)
	if err != nil || total != 1 || len(subs) != 1 || subs[0].URL != "/u/user-u4" {
		t.Fatalf("user subscriptions = %+v total=%d err=%v", subs, total, err)
	}
}
