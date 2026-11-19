package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
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

func newMediaTestService(t *testing.T) (*Service, *storage.Local) {
	t.Helper()
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
	return svc, stg
}

func TestUploadStripsJPEGMetadata(t *testing.T) {
	svc, stg := newMediaTestService(t)
	img := image.NewRGBA(image.Rect(0, 0, 120, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
		}
	}
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	payload := []byte("Exif\x00\x00GPSLatitude=39.9;SecretDevice=phone")
	segment := make([]byte, 4, 4+len(payload))
	segment[0], segment[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(segment[2:4], uint16(len(payload)+2))
	segment = append(segment, payload...)
	withExif := append([]byte{0xFF, 0xD8}, segment...)
	withExif = append(withExif, raw.Bytes()[2:]...)

	u, _, err := svc.Upload(context.Background(), "u1", "photo.jpg", "image/jpeg", bytes.NewReader(withExif))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	stored, ok := stg.LocalPath(u.StorageKey)
	if !ok {
		t.Fatal("stored file missing")
	}
	b, err := os.ReadFile(stored)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("Exif")) || bytes.Contains(b, []byte("GPSLatitude")) || bytes.Contains(b, []byte("SecretDevice")) {
		t.Fatal("metadata survived JPEG re-encode")
	}
}

func TestValidateImageDimensions(t *testing.T) {
	if err := validateImageDimensions(5000, 5000); err != nil {
		t.Fatalf("25MP rejected: %v", err)
	}
	for _, tc := range []struct{ w, h int }{{6000, 5000}, {12000, 12000}, {0, 10}, {12001, 10}} {
		if err := validateImageDimensions(tc.w, tc.h); err == nil {
			t.Fatalf("%dx%d accepted", tc.w, tc.h)
		}
	}
}

func TestStripWebPMetadata(t *testing.T) {
	chunk := func(fourcc string, payload []byte) []byte {
		out := make([]byte, 8, 8+len(payload)+1)
		copy(out[0:4], fourcc)
		binary.LittleEndian.PutUint32(out[4:8], uint32(len(payload)))
		out = append(out, payload...)
		if len(payload)%2 == 1 {
			out = append(out, 0)
		}
		return out
	}
	body := append(chunk("VP8X", []byte{0x0C, 0, 0, 0, 1, 0, 0, 1, 0, 0}), chunk("EXIF", []byte("secret"))...)
	body = append(body, chunk("VP8L", []byte{0x2f, 0, 0, 0, 0})...)
	riff := make([]byte, 12, 12+len(body))
	copy(riff, "RIFF")
	binary.LittleEndian.PutUint32(riff[4:8], uint32(len(body)+4))
	copy(riff[8:12], "WEBP")
	riff = append(riff, body...)

	clean, err := stripWebPMetadata(riff)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(clean, []byte("EXIF")) || bytes.Contains(clean, []byte("secret")) {
		t.Fatal("EXIF chunk survived WebP strip")
	}
	if got := binary.LittleEndian.Uint32(clean[4:8]); int(got) != len(clean)-8 {
		t.Fatalf("RIFF size = %d, want %d", got, len(clean)-8)
	}
	if clean[20]&0x0C != 0 {
		t.Fatalf("VP8X flags still advertise metadata: 0x%02x", clean[20])
	}
}
