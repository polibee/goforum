package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/platform/storage"
)

func TestUploadGeneratesThumbnail(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "media.db")), &gorm.Config{})
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
	stg, err := storage.NewLocal(t.TempDir(), "/uploads")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	svc := NewService(db)
	svc.Configure(nil, stg)

	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}

	u, url, err := svc.Upload(context.Background(), "user-1", "test.png", "image/png", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if u.Width != 800 || u.Height != 600 {
		t.Fatalf("dimensions = %dx%d", u.Width, u.Height)
	}
	if url == "" || !bytes.HasPrefix([]byte(url), []byte("/uploads/")) {
		t.Fatalf("url = %q", url)
	}
	thumb := svc.ThumbURL(context.Background(), u.ID)
	if thumb == "" || !bytes.Contains([]byte(thumb), []byte("/thumbs/")) {
		t.Fatalf("thumb = %q", thumb)
	}
	var variants int64
	if err := db.Model(&UploadVariant{}).Where("upload_id = ?", u.ID).Count(&variants).Error; err != nil {
		t.Fatalf("count variants: %v", err)
	}
	if variants != 1 {
		t.Fatalf("variants = %d", variants)
	}
}
