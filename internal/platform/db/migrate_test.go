package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type migrationWidget struct {
	ID string `gorm:"primaryKey;size:26"`
}

func (migrationWidget) TableName() string { return "migration_widgets" }

func testMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrate.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return gdb
}

func TestApplyMigrationsRecordsVersions(t *testing.T) {
	gdb := testMigrationDB(t)
	migrations := []Migration{
		{Version: 2, Name: "second", Up: func(context.Context, *gorm.DB) error { return nil }},
		{Version: 1, Name: "first", Up: func(_ context.Context, tx *gorm.DB) error {
			return tx.AutoMigrate(&migrationWidget{})
		}},
	}
	applied, err := ApplyMigrations(context.Background(), gdb, migrations)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied) != 2 || applied[0] != 1 || applied[1] != 2 {
		t.Fatalf("applied = %v", applied)
	}
	if !gdb.Migrator().HasTable("migration_widgets") {
		t.Fatal("migration table missing")
	}
	again, err := ApplyMigrations(context.Background(), gdb, migrations)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second apply ran %v", again)
	}
	versions, err := AppliedMigrationVersions(gdb)
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions = %v err=%v", versions, err)
	}
}

func TestEnsureMigrationsApplied(t *testing.T) {
	gdb := testMigrationDB(t)
	migrations := []Migration{
		{Version: 1, Name: "first", Up: func(context.Context, *gorm.DB) error { return nil }},
		{Version: 2, Name: "second", Up: func(context.Context, *gorm.DB) error { return nil }},
	}
	if err := EnsureMigrationsApplied(gdb, migrations); err == nil {
		t.Fatal("missing ledger accepted")
	}
	if _, err := ApplyMigrations(context.Background(), gdb, migrations[:1]); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := EnsureMigrationsApplied(gdb, migrations); err == nil {
		t.Fatal("pending migration accepted")
	}
	if _, err := ApplyMigrations(context.Background(), gdb, migrations); err != nil {
		t.Fatalf("apply rest: %v", err)
	}
	if err := EnsureMigrationsApplied(gdb, migrations); err != nil {
		t.Fatalf("complete ledger rejected: %v", err)
	}
}

func TestMigrationFailureDoesNotRecord(t *testing.T) {
	gdb := testMigrationDB(t)
	failing := []Migration{
		{Version: 1, Name: "bad", Up: func(context.Context, *gorm.DB) error { return errors.New("boom") }},
	}
	if _, err := ApplyMigrations(context.Background(), gdb, failing); err == nil {
		t.Fatal("failing migration accepted")
	}
	versions, err := AppliedMigrationVersions(gdb)
	if err != nil || len(versions) != 0 {
		t.Fatalf("failed migration recorded: %v err=%v", versions, err)
	}
}

func TestMigrationValidation(t *testing.T) {
	noop := func(context.Context, *gorm.DB) error { return nil }
	cases := []struct {
		name       string
		migrations []Migration
	}{
		{"empty", nil},
		{"zero version", []Migration{{Version: 0, Name: "x", Up: noop}}},
		{"empty name", []Migration{{Version: 1, Name: "", Up: noop}}},
		{"nil up", []Migration{{Version: 1, Name: "x"}}},
		{"duplicate", []Migration{{Version: 1, Name: "a", Up: noop}, {Version: 1, Name: "b", Up: noop}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMigrations(tc.migrations); err == nil {
				t.Fatal("invalid migrations accepted")
			}
		})
	}
}
