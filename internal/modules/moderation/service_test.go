package moderation

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
	return &cmod.TopicMeta{ID: id, Title: "Topic " + id, AuthorID: "author"}, nil
}

func (fakeContent) PostMeta(_ context.Context, id string) (*cmod.PostMeta, error) {
	return &cmod.PostMeta{ID: id, TopicID: "topic1", TopicTitle: "Topic one", AuthorID: "author", PostNumber: 1}, nil
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
		out[id] = cmod.PostMeta{ID: id, TopicID: "topic1", TopicNumber: 1, TopicTitle: "Topic one", AuthorID: "author", PostNumber: 1}
	}
	return out, nil
}

type fakeModerator struct {
	hiddenTopics []string
	hiddenPosts  []string
	deleted      []string
}

func (f *fakeModerator) HideTopic(_ context.Context, id string) error {
	f.hiddenTopics = append(f.hiddenTopics, id)
	return nil
}
func (f *fakeModerator) HidePost(_ context.Context, id string) error {
	f.hiddenPosts = append(f.hiddenPosts, id)
	return nil
}
func (f *fakeModerator) DeleteTopic(_ context.Context, id string) error {
	f.deleted = append(f.deleted, "topic:"+id)
	return nil
}
func (f *fakeModerator) DeletePost(_ context.Context, id string) error {
	f.deleted = append(f.deleted, "post:"+id)
	return nil
}
func (f *fakeModerator) RestoreTopic(context.Context, string) error { return nil }
func (f *fakeModerator) RestorePost(context.Context, string) error  { return nil }
func (f *fakeModerator) LockTopic(context.Context, string, bool) error {
	return nil
}
func (f *fakeModerator) MoveTopic(context.Context, string, string) error { return nil }

func setup(t *testing.T) (*Service, *fakeModerator) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "moderation.db")), &gorm.Config{})
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
	mod := &fakeModerator{}
	svc := NewService(db)
	svc.Configure(nil, fakeContent{}, mod, nil, nil, nil, nil)
	return svc, mod
}

func TestBannedWords(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()

	if err := svc.CreateWord(ctx, "badword", "contains", "block", ""); err != nil {
		t.Fatalf("create block word: %v", err)
	}
	if err := svc.CreateWord(ctx, "reviewme", "contains", "review", ""); err != nil {
		t.Fatalf("create review word: %v", err)
	}
	action, matched, err := svc.Check(ctx, "this has BadWord inside")
	if err != nil || action != "block" || matched != "badword" {
		t.Fatalf("check = %q/%q/%v", action, matched, err)
	}
	action, _, _ = svc.Check(ctx, "please reviewme now")
	if action != "review" {
		t.Fatalf("review action = %q", action)
	}
	action, _, _ = svc.Check(ctx, "clean text")
	if action != "allow" {
		t.Fatalf("allow action = %q", action)
	}
	if err := svc.CreateWord(ctx, "(", "regex", "block", ""); err == nil {
		t.Fatal("invalid regex accepted")
	}
}

func TestFlagQueueClaimDecide(t *testing.T) {
	svc, mod := setup(t)
	ctx := context.Background()

	if err := svc.Flag(ctx, "reporter1", "post", "p1", "spam", "looks like spam"); err != nil {
		t.Fatalf("flag: %v", err)
	}
	rows, total, err := svc.ListQueue(ctx, "", 1, 20)
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("queue = %d/%d err=%v", len(rows), total, err)
	}
	item := rows[0]
	if item.TargetTitle != "Topic one" || item.FlagCount != 1 {
		t.Fatalf("row = %+v", item)
	}
	if err := svc.Claim(ctx, item.Item.ID, "mod1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := svc.Claim(ctx, item.Item.ID, "mod2"); err == nil {
		t.Fatal("double claim accepted")
	}
	if err := svc.Decide(ctx, item.Item.ID, "mod1", "hide", "", nil); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if len(mod.hiddenPosts) != 1 || mod.hiddenPosts[0] != "p1" {
		t.Fatalf("hidden posts = %v", mod.hiddenPosts)
	}
}

func TestAutoHideAfterThreshold(t *testing.T) {
	svc, mod := setup(t)
	ctx := context.Background()

	for _, reporter := range []string{"r1", "r2", "r3"} {
		if err := svc.Flag(ctx, reporter, "post", "p2", "abuse", ""); err != nil {
			t.Fatalf("flag %s: %v", reporter, err)
		}
	}
	if len(mod.hiddenPosts) != 1 || mod.hiddenPosts[0] != "p2" {
		t.Fatalf("auto hide = %v", mod.hiddenPosts)
	}
}
