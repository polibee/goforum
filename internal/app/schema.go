package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"goforum/internal/modules/ads"
	"goforum/internal/modules/auth"
	"goforum/internal/modules/billing"
	"goforum/internal/modules/community"
	"goforum/internal/modules/engagement"
	"goforum/internal/modules/gamification"
	"goforum/internal/modules/media"
	"goforum/internal/modules/messaging"
	"goforum/internal/modules/moderation"
	"goforum/internal/modules/notification"
	"goforum/internal/modules/placeholder"
	seomod "goforum/internal/modules/seo"
	sitemod "goforum/internal/modules/site"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/config"
	"goforum/internal/platform/db"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
)

var (
	// ErrSchemaIncomplete means an initialized database is missing tables or
	// columns. Startup must stop instead of serving requests against it.
	ErrSchemaIncomplete = errors.New("database schema is incomplete")
	// ErrDatabaseInitialized protects an existing installation from being
	// migrated by the installer path.
	ErrDatabaseInitialized = errors.New("database already contains users")
)

// SchemaModels returns every table model used by the application.
//
// It builds the same feature module list as Open, with nil dependencies: the
// constructors only retain pointers, and Models() is static. Keeping this list
// in one place lets the installer, the migrate command and startup validation
// agree on the expected schema.
func SchemaModels() ([]any, error) {
	mods := []Module{
		auth.NewModule(auth.New(nil)),
		community.New(community.NewService(nil)),
		media.New(nil, media.NewService(nil)),
		engagement.New(nil, engagement.NewService(nil)),
		messaging.New(nil, messaging.NewService(nil)),
		notification.New(nil, notification.NewService(nil)),
		moderation.New(nil, moderation.NewService(nil)),
		gamification.New(nil, gamification.NewService(nil)),
		billing.New(nil, billing.NewService(nil)),
		seomod.New(nil),
		sitemod.New(nil),
		ads.New(ads.NewService(nil)),
		placeholder.New("search", []string{"auth", "community"}, nil,
			[]settings.Definition{
				{Namespace: "search", Key: "min_query_length", Group: "search", Label: "最短查询长度", Type: settings.TypeInt, Default: 2, Min: 1, Max: 20, Permission: "settings.write"},
			}, nil),
	}
	registry, err := NewRegistry(mods...)
	if err != nil {
		return nil, err
	}
	models := append(settings.Models(), session.Models()...)
	models = append(models, audit.Models()...)
	models = append(models, registry.Models()...)
	return models, nil
}

// Migrate applies the current schema. It is the production upgrade path and
// the fallback for environments where development AutoMigrate is disabled.
func Migrate(cfg *config.Config, log *slog.Logger) error {
	return migrate(cfg, log, false)
}

// MigrateFresh creates the schema only when the database has no users. The
// installer uses it to make a production-mode first install work without
// touching an existing installation.
func MigrateFresh(cfg *config.Config, log *slog.Logger) error {
	return migrate(cfg, log, true)
}

func migrate(cfg *config.Config, log *slog.Logger, requireEmpty bool) error {
	gdb, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close(gdb)

	if requireEmpty {
		hasUsers, err := databaseHasUsers(gdb)
		if err != nil {
			return err
		}
		if hasUsers {
			return ErrDatabaseInitialized
		}
	}
	models, err := SchemaModels()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if _, err := db.ApplyMigrations(ctx, gdb, schemaMigrations(log)); err != nil {
		return err
	}
	if err := db.EnsureSchema(gdb, models...); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaIncomplete, err)
	}
	return nil
}

// schemaMigrations is the ordered production migration ledger. Version 1 is
// the baseline generated from the current model set; later releases append new
// entries instead of editing it. A future dialect-specific change can branch
// on tx.Dialector.Name() inside Up.
func schemaMigrations(log *slog.Logger) []db.Migration {
	return []db.Migration{
		{
			Version: 1,
			Name:    "baseline_models",
			Up: func(_ context.Context, tx *gorm.DB) error {
				models, err := SchemaModels()
				if err != nil {
					return err
				}
				return db.Migrate(tx, models...)
			},
		},
		{
			Version: 2,
			Name:    "legacy_subscriptions",
			Up: func(_ context.Context, tx *gorm.DB) error {
				return migrateLegacySubscriptions(tx, log)
			},
		},
	}
}

// databaseHasUsers reports whether an installation already exists. A missing
// users table means the database is empty (or only partially created by a
// failed install), which the installer may safely repair.
func databaseHasUsers(gdb *gorm.DB) (bool, error) {
	if !gdb.Migrator().HasTable("users") {
		return false, nil
	}
	var n int64
	if err := gdb.Table("users").Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
