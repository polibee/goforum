package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/gif"
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

// maxImagePixels bounds decoded image memory. A 25 MP RGBA image is roughly
// 100 MB, which is already large for a public upload endpoint.
const maxImagePixels = 25_000_000
const maxGIFrames = 300

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

	mime := http.DetectContentType(data)
	ext, ok := allowed[mime]
	if !ok {
		return nil, "", httpx.Invalid(map[string]string{"file": "仅支持 JPG/PNG/GIF/WebP 图片"})
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", httpx.Invalid(map[string]string{"file": "图片解析失败"})
	}
	if err := validateImageDimensions(cfg.Width, cfg.Height); err != nil {
		return nil, "", err
	}

	// Re-encode static images and strip metadata containers from WebP/GIF so
	// uploaded files cannot leak GPS/device data or carry trailing payloads.
	clean, err := sanitizeImage(data, mime)
	if err != nil {
		return nil, "", httpx.Invalid(map[string]string{"file": "图片处理失败：" + err.Error()})
	}
	if int64(len(clean)) > limit {
		return nil, "", httpx.Invalid(map[string]string{"file": fmt.Sprintf("处理后文件超过 %d MB 限制", maxMB)})
	}

	var used int64
	_ = s.db.WithContext(ctx).Model(&Upload{}).Where("user_id = ?", userID).
		Select("COALESCE(SUM(size_bytes), 0)").Scan(&used).Error
	if used+int64(len(clean)) > quotaMB*1024*1024 {
		return nil, "", httpx.Invalid(map[string]string{"file": fmt.Sprintf("已超出个人存储配额（%d MB）", quotaMB)})
	}

	sum := sha256.Sum256(clean)
	key := fmt.Sprintf("%s/%s%s", time.Now().UTC().Format("2006/01"), id.New(), ext)
	if err := s.storage.Put(ctx, key, bytes.NewReader(clean), int64(len(clean)), mime); err != nil {
		return nil, "", err
	}

	u := &Upload{
		UserID:     userID,
		Filename:   sanitizeName(filename),
		StorageKey: key,
		MIME:       mime,
		SizeBytes:  int64(len(clean)),
		Width:      cfg.Width,
		Height:     cfg.Height,
		SHA256:     hex.EncodeToString(sum[:]),
		Kind:       "image",
	}
	if err := s.db.WithContext(ctx).Create(u).Error; err != nil {
		_ = s.storage.Delete(ctx, key)
		return nil, "", err
	}
	_ = s.makeThumbnail(ctx, u, clean)
	return u, s.storage.URL(key), nil
}

func validateImageDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return httpx.Invalid(map[string]string{"file": "图片尺寸无效"})
	}
	if width > 12000 || height > 12000 {
		return httpx.Invalid(map[string]string{"file": "图片尺寸超出限制"})
	}
	if int64(width)*int64(height) > maxImagePixels {
		return httpx.Invalid(map[string]string{"file": fmt.Sprintf("图片像素总量超出限制（最多 %d 万像素）", maxImagePixels/10000)})
	}
	return nil
}

func sanitizeImage(data []byte, mime string) ([]byte, error) {
	switch mime {
	case "image/jpeg":
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case "image/png":
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case "image/gif":
		anim, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if len(anim.Image) > maxGIFrames {
			return nil, fmt.Errorf("GIF 帧数超过限制")
		}
		var buf bytes.Buffer
		if err := gif.EncodeAll(&buf, anim); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case "image/webp":
		return stripWebPMetadata(data)
	default:
		return nil, fmt.Errorf("不支持的图片格式")
	}
}

// stripWebPMetadata rebuilds the RIFF container without EXIF/XMP chunks while
// preserving image and animation chunks.
func stripWebPMetadata(data []byte) ([]byte, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, fmt.Errorf("WebP 容器格式无效")
	}
	var body bytes.Buffer
	offset := 12
	for offset < len(data) {
		if offset+8 > len(data) {
			return nil, fmt.Errorf("WebP 数据块截断")
		}
		fourcc := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		end := offset + 8 + size
		if size < 0 || end > len(data) {
			return nil, fmt.Errorf("WebP 数据块长度无效")
		}
		if fourcc != "EXIF" && fourcc != "XMP " {
			chunk := append([]byte(nil), data[offset:end]...)
			if fourcc == "VP8X" && len(chunk) >= 9 {
				// Clear the EXIF (0x08) and XMP (0x04) feature flags so the
				// container no longer advertises the stripped chunks.
				chunk[8] &^= 0x0C
			}
			body.Write(chunk)
			if size%2 == 1 {
				body.WriteByte(0)
			}
		}
		offset = end
		if size%2 == 1 {
			offset++
		}
	}
	out := make([]byte, 12, 12+body.Len())
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(body.Len()+4))
	copy(out[8:12], "WEBP")
	return append(out, body.Bytes()...), nil
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
