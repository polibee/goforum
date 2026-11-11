package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"goforum/internal/modules/auth"
	"goforum/internal/platform/config"
	"goforum/internal/platform/db"
)

func testSchemaConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Env = "production"
	cfg.HTTP.BaseURL = "https://forum.test"
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = filepath.Join(dir, "schema.db")
	cfg.Storage.LocalPath = filepath.Join(dir, "uploads")
	cfg.Cache.Driver = "memory"
	return cfg
}

func testSchemaLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMigrateCreatesAndVerifiesSchema(t *testing.T) {
	cfg := testSchemaConfig(t)
	if err := Migrate(cfg, testSchemaLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb, err := db.Open(cfg.Database)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close(gdb)
	for _, table := range []string{"users", "settings", "email_send_logs", "topics", "orders"} {
		if !gdb.Migrator().HasTable(table) {
			t.Fatalf("table %s missing after migrate", table)
		}
	}
	models, err := SchemaModels()
	if err != nil {
		t.Fatalf("schema models: %v", err)
	}
	if err := db.EnsureSchema(gdb, models...); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	versions, err := db.AppliedMigrationVersions(gdb)
	if err != nil || len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("migration ledger = %v err=%v", versions, err)
	}
	// Running migrate twice must stay idempotent.
	if err := Migrate(cfg, testSchemaLogger()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestOpenRejectsDatabaseWithoutMigrationLedger(t *testing.T) {
	cfg := testSchemaConfig(t)
	if err := Migrate(cfg, testSchemaLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb, err := db.Open(cfg.Database)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := gdb.Migrator().DropTable("schema_migrations"); err != nil {
		_ = db.Close(gdb)
		t.Fatalf("drop ledger: %v", err)
	}
	_ = db.Close(gdb)

	_, err = Open(cfg, "", "test", testSchemaLogger())
	if !errors.Is(err, ErrSchemaIncomplete) {
		t.Fatalf("open error = %v, want ErrSchemaIncomplete for missing ledger", err)
	}
}

func TestMigrateFreshRefusesExistingInstallation(t *testing.T) {
	cfg := testSchemaConfig(t)
	if err := MigrateFresh(cfg, testSchemaLogger()); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}
	gdb, err := db.Open(cfg.Database)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := auth.New(gdb).CreateAdmin(context.Background(), "root", "root@example.com", "root12345678"); err != nil {
		_ = db.Close(gdb)
		t.Fatalf("create admin: %v", err)
	}
	_ = db.Close(gdb)

	if err := MigrateFresh(cfg, testSchemaLogger()); !errors.Is(err, ErrDatabaseInitialized) {
		t.Fatalf("second fresh migrate = %v, want ErrDatabaseInitialized", err)
	}
	// The regular migrate command is allowed to run against an installation.
	if err := Migrate(cfg, testSchemaLogger()); err != nil {
		t.Fatalf("regular migrate after install: %v", err)
	}
}

func TestOpenStopsOnIncompleteInitializedSchema(t *testing.T) {
	cfg := testSchemaConfig(t)
	gdb, err := db.Open(cfg.Database)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := gdb.AutoMigrate(auth.Models()...); err != nil {
		_ = db.Close(gdb)
		t.Fatalf("partial migrate: %v", err)
	}
	if _, err := auth.New(gdb).CreateAdmin(context.Background(), "root", "root@example.com", "root12345678"); err != nil {
		_ = db.Close(gdb)
		t.Fatalf("create admin: %v", err)
	}
	_ = db.Close(gdb)

	_, err = Open(cfg, "", "test", testSchemaLogger())
	if !errors.Is(err, ErrSchemaIncomplete) {
		t.Fatalf("open error = %v, want ErrSchemaIncomplete", err)
	}
}

func TestOpenAllowsEmptyDatabaseForInstaller(t *testing.T) {
	cfg := testSchemaConfig(t)
	_, err := Open(cfg, "", "test", testSchemaLogger())
	if err == nil {
		t.Fatal("empty production database should fail schema validation before install")
	}
	if errors.Is(err, ErrSchemaIncomplete) {
		t.Fatalf("empty database must still reach the installer, got %v", err)
	}
}
