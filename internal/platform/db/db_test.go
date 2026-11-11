package db

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type legacyUser struct {
	ID string `gorm:"primaryKey;size:26"`
}

func (legacyUser) TableName() string { return "users" }

type currentUser struct {
	ID    string `gorm:"primaryKey;size:26"`
	Email string `gorm:"size:255;not null"`
}

func (currentUser) TableName() string { return "users" }

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "schema.db")), &gorm.Config{})
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

func TestEnsureSchemaChecksColumns(t *testing.T) {
	gdb := testDB(t)
	if err := gdb.AutoMigrate(&legacyUser{}); err != nil {
		t.Fatalf("migrate legacy: %v", err)
	}
	err := EnsureSchema(gdb, &currentUser{})
	if err == nil || !strings.Contains(err.Error(), "users.email") {
		t.Fatalf("missing column not reported: %v", err)
	}
	if err := gdb.Migrator().AddColumn(&currentUser{}, "Email"); err != nil {
		t.Fatalf("add column: %v", err)
	}
	if err := EnsureSchema(gdb, &currentUser{}); err != nil {
		t.Fatalf("complete schema rejected: %v", err)
	}
}

func TestEnsureSchemaChecksTables(t *testing.T) {
	gdb := testDB(t)
	err := EnsureSchema(gdb, &currentUser{})
	if err == nil || !strings.Contains(err.Error(), "missing tables: users") {
		t.Fatalf("missing table not reported: %v", err)
	}
}
