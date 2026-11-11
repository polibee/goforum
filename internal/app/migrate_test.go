package app

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/modules/billing"
	"goforum/internal/modules/engagement"
)

func TestMigrateLegacySubscriptions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(append(engagement.Models(), billing.Models()...)...); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE subscriptions (
		id text primary key, user_id text, target_type text, target_id text, level text, created_at datetime,
		product_id text, status text, starts_at datetime, ends_at datetime, canceled_at datetime, source_order_id text
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO subscriptions (id, user_id, target_type, target_id, level, created_at)
		VALUES ('f1', 'u1', 'category', 'c1', 'normal', datetime('now'))`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO subscriptions
		(id, user_id, product_id, status, starts_at, ends_at, source_order_id, created_at)
		VALUES ('b1', 'u1', 'p1', 'active', datetime('now'), datetime('now', '+30 days'), 'o1', datetime('now'))`).Error; err != nil {
		t.Fatal(err)
	}

	log := slog.Default()
	if err := migrateLegacySubscriptions(db, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if db.Migrator().HasTable("subscriptions") {
		t.Fatal("legacy table still present")
	}
	var follows, billings int64
	db.Table("follow_subscriptions").Where("id = ?", "f1").Count(&follows)
	db.Table("billing_subscriptions").Where("id = ?", "b1").Count(&billings)
	if follows != 1 || billings != 1 {
		t.Fatalf("follows=%d billings=%d", follows, billings)
	}
	if err := migrateLegacySubscriptions(db, log); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
