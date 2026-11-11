package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/markdown"
)

type fakeUsers struct{}

func (fakeUsers) GetSummaries(_ context.Context, ids []string) (map[string]contracts.UserSummary, error) {
	out := make(map[string]contracts.UserSummary, len(ids))
	for _, id := range ids {
		out[id] = contracts.UserSummary{ID: id, Username: "user-" + id}
	}
	return out, nil
}

func (fakeUsers) FindByUsernames(_ context.Context, names []string) (map[string]contracts.UserSummary, error) {
	out := make(map[string]contracts.UserSummary, len(names))
	for _, name := range names {
		out[strings.ToLower(name)] = contracts.UserSummary{ID: "id-" + strings.ToLower(name), Username: name}
	}
	return out, nil
}

func testService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "messaging.db")), &gorm.Config{})
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
	svc := NewService(db)
	svc.Configure(fakeUsers{}, markdown.New(), nil)
	return svc
}

func TestStartSendAndRead(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()

	conv, err := svc.StartDirect(ctx, "id-alice", "bob")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	again, err := svc.StartDirect(ctx, "id-alice", "bob")
	if err != nil || again.ID != conv.ID {
		t.Fatalf("start should reuse conversation: %v / %v", again, err)
	}

	if _, err := svc.Send(ctx, conv.ID, "id-alice", "hello **bob**"); err != nil {
		t.Fatalf("send: %v", err)
	}

	rows, total, err := svc.List(ctx, "id-bob", 20, 0, "", "", false)
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("list = %d/%d err=%v", len(rows), total, err)
	}
	if rows[0].Unread != 1 {
		t.Fatalf("unread = %d", rows[0].Unread)
	}
	if rows[0].OtherName != "user-id-alice" {
		t.Fatalf("other = %q", rows[0].OtherName)
	}

	view, err := svc.Get(ctx, conv.ID, "id-bob")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(view.Messages) != 1 || !strings.Contains(view.Messages[0].CookedHTML, "<strong>bob</strong>") {
		t.Fatalf("messages = %+v", view.Messages)
	}

	rows, _, _ = svc.List(ctx, "id-bob", 20, 0, "", "", false)
	if rows[0].Unread != 0 {
		t.Fatalf("unread after read = %d", rows[0].Unread)
	}

	if _, err := svc.Get(ctx, conv.ID, "id-carol"); err == nil {
		t.Fatal("non-participant could read conversation")
	}
}

func TestSendRejectsEmpty(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	conv, _ := svc.StartDirect(ctx, "id-alice", "bob")
	if _, err := svc.Send(ctx, conv.ID, "id-alice", "   "); err == nil {
		t.Fatal("empty message accepted")
	}
}

func TestBlockPreventsSend(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	conv, err := svc.StartDirect(ctx, "id-alice", "bob")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	blocked, err := svc.ToggleBlock(ctx, "id-bob", "id-alice")
	if err != nil || !blocked {
		t.Fatalf("block = %v/%v", blocked, err)
	}
	if _, err := svc.Send(ctx, conv.ID, "id-alice", "hello"); err == nil {
		t.Fatal("blocked send accepted")
	}
	if _, err := svc.ToggleBlock(ctx, "id-bob", "id-alice"); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	if _, err := svc.Send(ctx, conv.ID, "id-alice", "hello"); err != nil {
		t.Fatalf("send after unblock: %v", err)
	}
}
