package db

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Migration is one ordered schema or data change. Up receives the transaction
// used to apply it; the version row is inserted only after Up succeeds.
type Migration struct {
	Version int64
	Name    string
	Up      func(ctx context.Context, tx *gorm.DB) error
}

// SchemaMigration records an applied version. It is the production migration
// ledger; development AutoMigrate does not write to it.
type SchemaMigration struct {
	Version   int64     `gorm:"primaryKey;autoIncrement:false"`
	Name      string    `gorm:"size:128;not null"`
	AppliedAt time.Time `gorm:"not null"`
}

func (SchemaMigration) TableName() string { return "schema_migrations" }

// ApplyMigrations runs every pending migration in order and returns the
// versions applied by this call. Running it twice is safe.
func ApplyMigrations(ctx context.Context, gdb *gorm.DB, migrations []Migration) ([]int64, error) {
	migrations = sortedMigrations(migrations)
	if err := validateMigrations(migrations); err != nil {
		return nil, err
	}
	if err := gdb.WithContext(ctx).AutoMigrate(&SchemaMigration{}); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	var appliedVersions []int64
	if err := gdb.WithContext(ctx).Model(&SchemaMigration{}).Pluck("version", &appliedVersions).Error; err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	applied := make(map[int64]bool, len(appliedVersions))
	for _, v := range appliedVersions {
		applied[v] = true
	}

	appliedNow := make([]int64, 0)
	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := m.Up(ctx, tx); err != nil {
				return err
			}
			return tx.Create(&SchemaMigration{
				Version:   m.Version,
				Name:      m.Name,
				AppliedAt: time.Now().UTC(),
			}).Error
		})
		if err != nil {
			return appliedNow, fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
		}
		appliedNow = append(appliedNow, m.Version)
	}
	return appliedNow, nil
}

// EnsureMigrationsApplied makes production startup fail when the ledger is
// missing or behind the binary's migration list.
func EnsureMigrationsApplied(gdb *gorm.DB, migrations []Migration) error {
	migrations = sortedMigrations(migrations)
	if err := validateMigrations(migrations); err != nil {
		return err
	}
	if !gdb.Migrator().HasTable("schema_migrations") {
		return fmt.Errorf("no migration ledger found; run `goforum migrate`")
	}
	var applied []int64
	if err := gdb.Model(&SchemaMigration{}).Pluck("version", &applied).Error; err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	seen := make(map[int64]bool, len(applied))
	var latest int64
	for _, v := range applied {
		seen[v] = true
		if v > latest {
			latest = v
		}
	}
	missing := make([]string, 0)
	for _, m := range migrations {
		if !seen[m.Version] {
			missing = append(missing, fmt.Sprintf("%d:%s", m.Version, m.Name))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("pending migrations: %s (latest applied %d); run `goforum migrate`",
			strings.Join(missing, ", "), latest)
	}
	return nil
}

// AppliedMigrationVersions lists the ledger for diagnostics.
func AppliedMigrationVersions(gdb *gorm.DB) ([]int64, error) {
	var versions []int64
	if !gdb.Migrator().HasTable("schema_migrations") {
		return versions, nil
	}
	err := gdb.Model(&SchemaMigration{}).Order("version ASC").Pluck("version", &versions).Error
	return versions, err
}

func sortedMigrations(in []Migration) []Migration {
	out := append([]Migration(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

func validateMigrations(migrations []Migration) error {
	if len(migrations) == 0 {
		return fmt.Errorf("no migrations registered")
	}
	var previous int64
	for i, m := range migrations {
		if m.Version <= 0 {
			return fmt.Errorf("migration version must be positive")
		}
		if strings.TrimSpace(m.Name) == "" {
			return fmt.Errorf("migration %d has an empty name", m.Version)
		}
		if m.Up == nil {
			return fmt.Errorf("migration %d (%s) has no Up function", m.Version, m.Name)
		}
		if i > 0 && m.Version == previous {
			return fmt.Errorf("duplicate migration version %d", m.Version)
		}
		previous = m.Version
	}
	return nil
}
