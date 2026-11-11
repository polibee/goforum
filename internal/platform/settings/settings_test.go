package settings

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/platform/secretbox"
)

func TestDefinitionValidation(t *testing.T) {
	valid := Definition{Namespace: "site", Key: "general.name", Group: "general", Label: "名称", Type: TypeString, Default: "GoForum"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}

	badColor := Definition{Namespace: "site", Key: "theme.color", Group: "appearance", Label: "颜色", Type: TypeColor, Default: "blue"}
	if err := badColor.Validate(); err == nil {
		t.Fatal("invalid color accepted")
	}

	badEnum := Definition{Namespace: "site", Key: "reg.mode", Group: "registration", Label: "模式", Type: TypeEnum, Default: "open", Enum: []string{"invite"}}
	if err := badEnum.Validate(); err == nil {
		t.Fatal("default outside enum accepted")
	}

	badInt := Definition{Namespace: "site", Key: "post.max", Group: "content", Label: "上限", Type: TypeInt, Default: 500, Min: 1, Max: 100}
	if err := badInt.Validate(); err == nil {
		t.Fatal("out of range int accepted")
	}
}

func TestStoreSetAndRevision(t *testing.T) {
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "settings.db")), &gorm.Config{})
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

	reg := NewRegistry()
	if err := reg.Register(
		Definition{Namespace: "site", Key: "general.name", Group: "general", Label: "名称", Type: TypeString, Default: "GoForum"},
		Definition{Namespace: "site", Key: "registration.password_min", Group: "registration", Label: "密码长度", Type: TypeInt, Default: 10, Min: 6, Max: 128},
	); err != nil {
		t.Fatalf("register: %v", err)
	}
	store, err := NewStore(db, reg)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if got := store.GetString("site.general.name"); got != "GoForum" {
		t.Fatalf("default = %q", got)
	}

	ctx := context.Background()
	if err := store.SetMany(ctx, map[string]any{
		"site.general.name":              "测试站",
		"site.registration.password_min": 12,
	}, "actor-1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := store.GetString("site.general.name"); got != "测试站" {
		t.Fatalf("value = %q", got)
	}
	if got := store.GetInt("site.registration.password_min"); got != 12 {
		t.Fatalf("int = %d", got)
	}

	var revisions int64
	if err := db.Model(&Revision{}).Count(&revisions).Error; err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if revisions != 2 {
		t.Fatalf("revisions = %d, want 2", revisions)
	}

	err = store.SetMany(ctx, map[string]any{"site.registration.password_min": 3}, "actor-1")
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func testStoreWithSecret(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "secrets.db")), &gorm.Config{})
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
	reg := NewRegistry()
	if err := reg.Register(
		Definition{Namespace: "mail", Key: "smtp.password", Group: "notification", Label: "SMTP 密码", Type: TypeSecret, Default: "", RegisterURL: "https://example.com/register"},
		Definition{Namespace: "site", Key: "general.name", Group: "general", Label: "名称", Type: TypeString, Default: "GoForum"},
	); err != nil {
		t.Fatalf("register: %v", err)
	}
	store, err := NewStore(db, reg)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store, db
}

func TestSecretSettingsEncryptedAtRest(t *testing.T) {
	store, db := testStoreWithSecret(t)
	ctx := context.Background()
	if err := store.SetSecretKey("test-encryption-key"); err != nil {
		t.Fatalf("set key: %v", err)
	}
	if err := store.Set(ctx, "mail.smtp.password", "s3cret-value", "actor"); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	var row Setting
	if err := db.First(&row, "namespace = ? AND key = ?", "mail", "smtp.password").Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if strings.Contains(row.ValueJSON, "s3cret-value") {
		t.Fatalf("secret stored in plaintext: %q", row.ValueJSON)
	}
	var encoded string
	if err := json.Unmarshal([]byte(row.ValueJSON), &encoded); err != nil {
		t.Fatalf("stored value is not a JSON string: %q", row.ValueJSON)
	}
	if !secretbox.IsEncrypted(encoded) {
		t.Fatalf("stored value is not encrypted: %q", encoded)
	}
	if got := store.GetString("mail.smtp.password"); got != "s3cret-value" {
		t.Fatalf("decrypted value = %q", got)
	}
	items := store.Items("notification")
	if len(items) != 1 || !items[0].Secret || !items[0].Configured || items[0].Value != "••••••••" || items[0].RegisterURL != "https://example.com/register" {
		t.Fatalf("secret item = %+v", items)
	}
	var rev Revision
	if err := db.Order("created_at DESC").First(&rev).Error; err != nil {
		t.Fatalf("revision: %v", err)
	}
	if rev.NewJSON != `"***"` {
		t.Fatalf("revision leaks secret: %q", rev.NewJSON)
	}
}

func TestLegacyPlaintextSecretMigrated(t *testing.T) {
	store, db := testStoreWithSecret(t)
	ctx := context.Background()
	if err := db.Create(&Setting{
		Namespace: "mail", Key: "smtp.password", ValueJSON: `"legacy-secret"`,
	}).Error; err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	reg := NewRegistry()
	_ = reg.Register(Definition{Namespace: "mail", Key: "smtp.password", Group: "notification", Label: "SMTP 密码", Type: TypeSecret, Default: "", RegisterURL: "https://example.com/register"})
	store, err := NewStore(db, reg)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if got := store.GetString("mail.smtp.password"); got != "legacy-secret" {
		t.Fatalf("legacy read = %q", got)
	}
	if err := store.SetSecretKey("test-encryption-key"); err != nil {
		t.Fatalf("set key: %v", err)
	}
	var row Setting
	if err := db.First(&row, "namespace = ? AND key = ?", "mail", "smtp.password").Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if strings.Contains(row.ValueJSON, "legacy-secret") || !strings.Contains(row.ValueJSON, secretbox.Prefix) {
		t.Fatalf("legacy secret not migrated: %q", row.ValueJSON)
	}
	if got := store.GetString("mail.smtp.password"); got != "legacy-secret" {
		t.Fatalf("migrated read = %q", got)
	}
	_ = ctx
}

func TestSecretWriteRequiresKey(t *testing.T) {
	store, _ := testStoreWithSecret(t)
	err := store.Set(context.Background(), "mail.smtp.password", "secret", "actor")
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected validation error, got %v", err)
	}
	if !strings.Contains(ve.Fields["mail.smtp.password"], "APP_KEY") {
		t.Fatalf("error = %v", ve.Fields)
	}
}

func TestUnreadableSecretReported(t *testing.T) {
	store, db := testStoreWithSecret(t)
	ctx := context.Background()
	if err := store.SetSecretKey("key-one"); err != nil {
		t.Fatalf("set key: %v", err)
	}
	if err := store.Set(ctx, "mail.smtp.password", "secret", "actor"); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	reg := NewRegistry()
	_ = reg.Register(Definition{Namespace: "mail", Key: "smtp.password", Group: "notification", Label: "SMTP 密码", Type: TypeSecret, Default: "", RegisterURL: "https://example.com/register"})
	reopened, err := NewStore(db, reg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := reopened.SetSecretKey("key-two"); err != nil {
		t.Fatalf("set wrong key: %v", err)
	}
	errors := reopened.SecretErrors()
	if len(errors) != 1 || errors[0] != "mail.smtp.password" {
		t.Fatalf("secret errors after startup = %v", errors)
	}
	if got := reopened.GetString("mail.smtp.password"); got != "" {
		t.Fatalf("unreadable secret returned %q", got)
	}
	errors = reopened.SecretErrors()
	if len(errors) != 1 || errors[0] != "mail.smtp.password" {
		t.Fatalf("secret errors = %v", errors)
	}
}
