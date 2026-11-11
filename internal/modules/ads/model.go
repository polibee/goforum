package ads

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Ad struct {
	ID         string     `gorm:"primaryKey;size:26"`
	UserID     string     `gorm:"size:26;index"` // empty for admin-created ads
	Slot       string     `gorm:"size:24;not null;index"`
	Format     string     `gorm:"size:12;not null;default:text"` // text | image | html
	Title      string     `gorm:"size:120;not null"`
	Text       string     `gorm:"size:500"`
	URL        string     `gorm:"size:500"`
	ImageURL   string     `gorm:"size:500"`
	HTML       string     `gorm:"type:text"`
	Height     int        `gorm:"not null;default:0"`
	Priority   int        `gorm:"not null;default:0"`
	Status     string     `gorm:"size:20;not null;default:pending_review;index"`
	Months     int        `gorm:"not null;default:1"`
	Price      int64      `gorm:"not null;default:0"`
	Refunded   bool       `gorm:"not null;default:false"`
	ReviewNote string     `gorm:"size:300"`
	StartAt    *time.Time `gorm:"index"`
	EndAt      *time.Time `gorm:"index"`
	ClickCount int64      `gorm:"not null;default:0"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (Ad) TableName() string { return "ads" }

func (a *Ad) BeforeCreate(*gorm.DB) error {
	if a.ID == "" {
		a.ID = id.New()
	}
	return nil
}

func Models() []any { return []any{&Ad{}, &AdSlot{}} }

func (a Ad) StatusLabel() string {
	switch a.Status {
	case "active":
		return "投放中"
	case "pending_review":
		return "待审核"
	case "pending_payment":
		return "支付中"
	case "paused":
		return "已下架"
	case "banned":
		return "已封禁"
	case "rejected":
		return "已拒绝"
	default:
		return a.Status
	}
}

func (a Ad) SlotLabel() string {
	if p, ok := PlacementByKey(a.Slot); ok {
		return p.Label
	}
	return a.Slot
}

func (a Ad) FormatLabel() string {
	switch a.Format {
	case "image":
		return "图片"
	case "html":
		return "HTML / JS"
	default:
		return "文字"
	}
}

// AdSlot is the admin-managed configuration for a placement. Built-in keys
// map to frontend render points; deleted slots stop rendering and selling.
type AdSlot struct {
	ID          string         `gorm:"primaryKey;size:26"`
	Key         string         `gorm:"size:24;uniqueIndex;not null"`
	Name        string         `gorm:"size:80;not null"`
	Description string         `gorm:"size:300"`
	Size        string         `gorm:"size:40"`
	Height      int            `gorm:"not null;default:90"`
	Sort        int            `gorm:"not null;default:0"`
	Enabled     bool           `gorm:"not null;default:true;index"`
	SellEnabled bool           `gorm:"not null;default:true;index"`
	MaxText     int            `gorm:"not null;default:1"`
	MaxImage    int            `gorm:"not null;default:1"`
	PriceText   int64          `gorm:"not null;default:0"` // USD cents / month
	PriceImage  int64          `gorm:"not null;default:0"` // USD cents / month
	DeletedAt   gorm.DeletedAt `gorm:"index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (AdSlot) TableName() string { return "ad_slots" }

func (s *AdSlot) BeforeCreate(*gorm.DB) error {
	if s.ID == "" {
		s.ID = id.New()
	}
	return nil
}
