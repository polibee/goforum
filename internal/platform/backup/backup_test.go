package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/platform/config"
)

func TestSQLiteBackupIncludesDatabaseAndUploads(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	gdb, err := gorm.Open(sqlite.Open(src), &gorm.Config{})
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	if err := gdb.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)").Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := gdb.Exec("INSERT INTO notes (body) VALUES ('hello')").Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	sqlDB, _ := gdb.DB()
	_ = sqlDB.Close()

	uploads := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(filepath.Join(uploads, "2026"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploads, "2026", "a.txt"), []byte("asset"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Database.DSN = src
	cfg.Storage.LocalPath = uploads
	out := filepath.Join(dir, "out")
	res, err := RunWithOptions(context.Background(), cfg, "test", out, Options{AllowPlaintext: true})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if len(res.Manifest.Files) != 2 {
		t.Fatalf("files = %+v", res.Manifest.Files)
	}
	for _, name := range []string{"database.db", "uploads.tar.gz", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest.Driver != "sqlite" || manifest.Version != "test" {
		t.Fatalf("manifest = %+v", manifest)
	}

	copyDB, err := gorm.Open(sqlite.Open(filepath.Join(out, "database.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	var body string
	if err := copyDB.Raw("SELECT body FROM notes LIMIT 1").Scan(&body).Error; err != nil {
		t.Fatalf("query copy: %v", err)
	}
	if body != "hello" {
		t.Fatalf("body = %q", body)
	}
	copySQL, _ := copyDB.DB()
	_ = copySQL.Close()
}
