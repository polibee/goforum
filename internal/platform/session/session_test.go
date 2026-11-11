package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSessionMetadataAndRevoke(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sessions.db")), &gorm.Config{})
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
	store := NewStore(db, time.Hour)
	ctx := context.Background()
	first, _, err := store.Create(ctx, "u1", "127.0.0.1", "Test Browser")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, _, err := store.Create(ctx, "u1", "10.0.0.2", "Other Browser")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rows, err := store.ListByUser(ctx, "u1")
	if err != nil || len(rows) != 2 {
		t.Fatalf("list = %d err=%v", len(rows), err)
	}
	if rows[0].IP == "" || rows[0].UserAgent == "" {
		t.Fatalf("metadata missing: %+v", rows[0])
	}
	if err := store.DeleteOthers(ctx, "u1", second.ID); err != nil {
		t.Fatalf("delete others: %v", err)
	}
	rows, _ = store.ListByUser(ctx, "u1")
	if len(rows) != 1 || rows[0].ID != second.ID {
		t.Fatalf("after delete others = %+v", rows)
	}
	if err := store.Revoke(ctx, "u1", second.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	rows, _ = store.ListByUser(ctx, "u1")
	if len(rows) != 0 {
		t.Fatalf("after revoke = %+v", rows)
	}
	_ = first
}
