package app

import (
	"log/slog"

	"gorm.io/gorm"
)

// migrateLegacySubscriptions splits the pre-M7 shared "subscriptions" table
// into engagement follow subscriptions and billing subscriptions, then drops
// the legacy table. It is safe to run repeatedly.
func migrateLegacySubscriptions(gdb *gorm.DB, log *slog.Logger) error {
	if !gdb.Migrator().HasTable("subscriptions") {
		return nil
	}
	hasFollow := gdb.Migrator().HasColumn("subscriptions", "target_type")
	hasBilling := gdb.Migrator().HasColumn("subscriptions", "product_id")
	if !hasFollow && !hasBilling {
		return nil
	}
	tx := gdb.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	if hasFollow {
		if err := tx.Exec(`INSERT INTO follow_subscriptions (id, user_id, target_type, target_id, level, created_at)
			SELECT s.id, s.user_id, s.target_type, s.target_id, s.level, s.created_at
			FROM subscriptions s
			WHERE s.target_type <> '' AND s.target_id <> ''
			  AND NOT EXISTS (SELECT 1 FROM follow_subscriptions f WHERE f.id = s.id)`).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	if hasBilling {
		if err := tx.Exec(`INSERT INTO billing_subscriptions
			(id, user_id, product_id, status, starts_at, ends_at, canceled_at, source_order_id, created_at)
			SELECT s.id, s.user_id, s.product_id, s.status, s.starts_at, s.ends_at, s.canceled_at, s.source_order_id, s.created_at
			FROM subscriptions s
			WHERE s.product_id <> ''
			  AND NOT EXISTS (SELECT 1 FROM billing_subscriptions b WHERE b.id = s.id)`).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Exec("DROP TABLE subscriptions").Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	log.Info("migrated legacy subscriptions table", "follow", hasFollow, "billing", hasBilling)
	return nil
}
