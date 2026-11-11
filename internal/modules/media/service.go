package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"gorm.io/gorm"

	"goforum/internal/platform/httpx"
	"goforum/internal/platform/id"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/storage"
)

type Service struct {
	db       *gorm.DB
	settings *settings.Store
	storage  storage.Storage
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Configure(st *settings.Store, stg storage.Storage) {
	s.settings = st
	s.storage = stg
}

var allowed = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

func (s *Service) Upload(ctx context.Context, userID, filename, declaredType string, r io.Reader) (*Upload, string, error) {
	if s.storage == nil {
		return nil, "", httpx.NewError("media.unavailable", http.StatusServiceUnavailable, "存储未配置")
	}
	maxMB := int64(20)
	if s.settings != nil {
		if v := s.settings.GetInt("media.upload.max_size_mb"); v > 0 {
			maxMB = v
		}
	}
	limit := maxMB * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > limit {
		return nil, "", httpx.Invalid(map[string]string{"file": fmt.Sprintf("文件超过 %d MB 限制", maxMB)})
	}
	if len(data) == 0 {
		return nil, "", httpx.Invalid(map[string]string{"file": "文件为空"})
	}

	quotaMB := int64(500)
	if s.settings != nil {
		if v := s.settings.GetInt("media.upload.quota_per_user_mb"); v > 0 {
			quotaMB = v
		}
	}
	var used int64
	_ = s.db.WithContext(ctx).Model(&Upload{}).Where("user_id = ?", userID).
		Select("COALESCE(SUM(size_bytes), 0)").Scan(&used).Error
	if used+int64(len(data)) > quotaMB*1024*1024 {
		return nil, "", httpx.Invalid(map[string]string{"file": fmt.Sprintf("已超出个人存储配额（%d MB）", quotaMB)})
	}

	mime := http.DetectContentType(data)
	ext, ok := allowed[mime]
	if !ok {
		return nil, "", httpx.Invalid(map[string]string{"file": "仅支持 JPG/PNG/GIF/WebP 图片"})
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", httpx.Invalid(map[string]string{"file": "图片解析失败"})
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 12000 || cfg.Height > 12000 {
		return nil, "", httpx.Invalid(map[string]string{"file": "图片尺寸超出限制"})
	}

	sum := sha256.Sum256(data)
	key := fmt.Sprintf("%s/%s%s", time.Now().UTC().Format("2006/01"), id.New(), ext)
	if err := s.storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), mime); err != nil {
		return nil, "", err
	}

	u := &Upload{
		UserID:     userID,
		Filename:   sanitizeName(filename),
		StorageKey: key,
		MIME:       mime,
		SizeBytes:  int64(len(data)),
		Width:      cfg.Width,
		Height:     cfg.Height,
		SHA256:     hex.EncodeToString(sum[:]),
		Kind:       "image",
	}
	if err := s.db.WithContext(ctx).Create(u).Error; err != nil {
		_ = s.storage.Delete(ctx, key)
		return nil, "", err
	}
	_ = s.makeThumbnail(ctx, u, data)
	return u, s.storage.URL(key), nil
}

func (s *Service) makeThumbnail(ctx context.Context, u *Upload, data []byte) error {
	if u.Width <= 480 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	width := 480
	height := img.Bounds().Dy() * width / img.Bounds().Dx()
	if height < 1 {
		height = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)

	var buf bytes.Buffer
	ext, mime := ".jpg", "image/jpeg"
	if u.MIME == "image/png" {
		ext, mime = ".png", "image/png"
		if err := png.Encode(&buf, dst); err != nil {
			return err
		}
	} else {
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
			return err
		}
	}
	key := fmt.Sprintf("thumbs/%s/%s%s", time.Now().UTC().Format("2006/01"), u.ID, ext)
	if err := s.storage.Put(ctx, key, bytes.NewReader(buf.Bytes()), int64(buf.Len()), mime); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Create(&UploadVariant{
		UploadID: u.ID, Kind: "thumb", StorageKey: key, MIME: mime,
		Width: width, Height: height, SizeBytes: int64(buf.Len()),
	}).Error
}

func (s *Service) ThumbURL(ctx context.Context, uploadID string) string {
	var v UploadVariant
	if err := s.db.WithContext(ctx).Where("upload_id = ? AND kind = ?", uploadID, "thumb").First(&v).Error; err != nil {
		return ""
	}
	if s.storage == nil {
		return ""
	}
	return s.storage.URL(v.StorageKey)
}

func (s *Service) URL(u *Upload) string {
	if s.storage == nil {
		return ""
	}
	return s.storage.URL(u.StorageKey)
}

func sanitizeName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "/" {
		return "upload"
	}
	if len([]rune(name)) > 120 {
		r := []rune(name)
		name = string(r[:120])
	}
	return name
}

func (s *Service) List(ctx context.Context, limit, offset int) ([]Upload, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var total int64
	if err := s.db.WithContext(ctx).Model(&Upload{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Upload
	err := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}
