package moderation

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Flag struct {
	ID         string `gorm:"primaryKey;size:26"`
	ReporterID string `gorm:"size:26;not null;uniqueIndex:uk_flag"`
	TargetType string `gorm:"size:20;not null;uniqueIndex:uk_flag"`
	TargetID   string `gorm:"size:26;not null;uniqueIndex:uk_flag"`
	ReasonCode string `gorm:"size:32;not null;default:other"`
	ReasonText string `gorm:"size:500"`
	Status     string `gorm:"size:16;not null;default:pending;index"`
	HandledBy  string `gorm:"size:26"`
	HandledAt  *time.Time
	Resolution string    `gorm:"size:32"`
	CreatedAt  time.Time `gorm:"index"`
}

func (Flag) TableName() string { return "flags" }

type ReviewItem struct {
	ID        string `gorm:"primaryKey;size:26"`
	ItemType  string `gorm:"size:20;not null;uniqueIndex:uk_review_item"`
	ItemID    string `gorm:"size:26;not null;uniqueIndex:uk_review_item"`
	Source    string `gorm:"size:16;not null;default:flag"`
	Status    string `gorm:"size:16;not null;default:pending;index"`
	Priority  int    `gorm:"not null;default:0"`
	ClaimedBy string `gorm:"size:26"`
	ClaimedAt *time.Time
	DecidedBy string `gorm:"size:26"`
	DecidedAt *time.Time
	Decision  string    `gorm:"size:24"`
	Note      string    `gorm:"size:500"`
	CreatedAt time.Time `gorm:"index"`
}

func (ReviewItem) TableName() string { return "review_queue" }

type BannedWord struct {
	ID          string `gorm:"primaryKey;size:26"`
	Word        string `gorm:"size:100;not null;uniqueIndex"`
	MatchType   string `gorm:"size:16;not null;default:contains"`
	Action      string `gorm:"size:16;not null;default:block"`
	Replacement string `gorm:"size:100"`
	Enabled     bool   `gorm:"not null;default:true"`
	CreatedAt   time.Time
}

func (BannedWord) TableName() string { return "banned_words" }

type Appeal struct {
	ID        string `gorm:"primaryKey;size:26"`
	UserID    string `gorm:"size:26;not null;index"`
	Username  string `gorm:"size:50;not null"`
	Reason    string `gorm:"type:text;not null"`
	Status    string `gorm:"size:16;not null;default:pending;index"`
	HandledBy string `gorm:"size:26"`
	HandledAt *time.Time
	Note      string    `gorm:"size:500"`
	CreatedAt time.Time `gorm:"index"`
}

func (Appeal) TableName() string { return "appeals" }

func (a *Appeal) BeforeCreate(*gorm.DB) error {
	if a.ID == "" {
		a.ID = id.New()
	}
	if a.Status == "" {
		a.Status = "pending"
	}
	return nil
}

func (f *Flag) BeforeCreate(*gorm.DB) error {
	if f.ID == "" {
		f.ID = id.New()
	}
	if f.Status == "" {
		f.Status = "pending"
	}
	return nil
}

func (r *ReviewItem) BeforeCreate(*gorm.DB) error {
	if r.ID == "" {
		r.ID = id.New()
	}
	if r.Status == "" {
		r.Status = "pending"
	}
	return nil
}

func (w *BannedWord) BeforeCreate(*gorm.DB) error {
	if w.ID == "" {
		w.ID = id.New()
	}
	return nil
}

func Models() []any { return []any{&Flag{}, &ReviewItem{}, &BannedWord{}, &Appeal{}} }
