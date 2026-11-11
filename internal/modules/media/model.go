package media

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Upload struct {
	ID         string `gorm:"primaryKey;size:26"`
	UserID     string `gorm:"size:26;index;not null"`
	Filename   string `gorm:"size:255"`
	StorageKey string `gorm:"size:255;uniqueIndex;not null"`
	MIME       string `gorm:"size:100;not null"`
	SizeBytes  int64  `gorm:"not null"`
	Width      int
	Height     int
	SHA256     string    `gorm:"size:64;index"`
	Kind       string    `gorm:"size:16;not null;default:image"`
	CreatedAt  time.Time `gorm:"index"`
}

func (Upload) TableName() string { return "uploads" }

func (u *Upload) BeforeCreate(*gorm.DB) error {
	if u.ID == "" {
		u.ID = id.New()
	}
	return nil
}

type UploadVariant struct {
	ID         string `gorm:"primaryKey;size:26"`
	UploadID   string `gorm:"size:26;index;not null"`
	Kind       string `gorm:"size:16;not null"`
	StorageKey string `gorm:"size:255;uniqueIndex;not null"`
	MIME       string `gorm:"size:100"`
	Width      int
	Height     int
	SizeBytes  int64
	CreatedAt  time.Time
}

func (UploadVariant) TableName() string { return "upload_variants" }

func (v *UploadVariant) BeforeCreate(*gorm.DB) error {
	if v.ID == "" {
		v.ID = id.New()
	}
	return nil
}

func Models() []any { return []any{&Upload{}, &UploadVariant{}} }
