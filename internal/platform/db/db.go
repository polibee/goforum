package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/config"
	"goforum/internal/platform/db/dialect"
)

// Open creates a GORM connection using the configured dialect.
func Open(cfg config.DatabaseConfig) (*gorm.DB, error) {
	d, err := dialect.Get(cfg.Driver)
	if err != nil {
		return nil, err
	}
	gdb, err := d.Open(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", cfg.Driver, err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, err
	}
	maxOpen := cfg.MaxOpen
	if maxOpen < 1 {
		maxOpen = 25
	}
	if cfg.Driver == "sqlite" && maxOpen > 4 {
		maxOpen = 4 // WAL: readers are fine, but keep the writer pool small
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(cfg.MaxIdle)
	if d := cfg.ConnMaxLifetime.Std(); d > 0 {
		sqlDB.SetConnMaxLifetime(d)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping %s: %w", cfg.Driver, err)
	}
	return gdb, nil
}

// Migrate applies the current model set. Development only; production uses versioned SQL.
func Migrate(gdb *gorm.DB, models ...any) error {
	if err := gdb.AutoMigrate(models...); err != nil {
		return fmt.Errorf("auto migrate: %w", err)
	}
	return nil
}

func Close(gdb *gorm.DB) error {
	if gdb == nil {
		return nil
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// DialectOf returns the dialect name for a connection.
func DialectOf(gdb *gorm.DB) string { return gdb.Dialector.Name() }

// EnsureSchema verifies (rather than applies) that the expected tables and
// columns exist. Production startup uses it so a missing migration fails
// loudly instead of silently rewriting tables or serving failing queries.
func EnsureSchema(gdb *gorm.DB, models ...any) error {
	missingTables := make([]string, 0)
	missingColumns := make([]string, 0)
	for _, m := range models {
		stmt := &gorm.Statement{DB: gdb}
		if err := stmt.Parse(m); err != nil || stmt.Schema == nil {
			continue
		}
		table := stmt.Schema.Table
		if !gdb.Migrator().HasTable(m) {
			missingTables = append(missingTables, table)
			continue
		}
		existing := make(map[string]bool)
		types, err := gdb.Migrator().ColumnTypes(m)
		if err == nil {
			for _, ct := range types {
				existing[strings.ToLower(ct.Name())] = true
			}
		}
		for _, name := range stmt.Schema.DBNames {
			if err == nil {
				if !existing[strings.ToLower(name)] {
					missingColumns = append(missingColumns, table+"."+name)
				}
				continue
			}
			// Some drivers cannot list columns; fall back to a direct lookup.
			if !gdb.Migrator().HasColumn(m, name) {
				missingColumns = append(missingColumns, table+"."+name)
			}
		}
	}
	if len(missingTables) == 0 && len(missingColumns) == 0 {
		return nil
	}
	parts := make([]string, 0, 2)
	if len(missingTables) > 0 {
		parts = append(parts, "missing tables: "+strings.Join(missingTables, ", "))
	}
	if len(missingColumns) > 0 {
		parts = append(parts, "missing columns: "+strings.Join(missingColumns, ", "))
	}
	return fmt.Errorf("%s (run `goforum migrate` first)", strings.Join(parts, "; "))
}
