package notification

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Notification struct {
	ID         string     `gorm:"primaryKey;size:26"`
	UserID     string     `gorm:"size:26;not null;uniqueIndex:uk_notification_event;index"`
	Type       string     `gorm:"size:40;not null;index"`
	ActorID    string     `gorm:"size:26"`
	TargetType string     `gorm:"size:20"`
	TargetID   string     `gorm:"size:26"`
	DataJSON   string     `gorm:"type:text"`
	EventID    string     `gorm:"size:64;not null;uniqueIndex:uk_notification_event"`
	ReadAt     *time.Time `gorm:"index"`
	CreatedAt  time.Time  `gorm:"index"`
}

func (Notification) TableName() string { return "notifications" }

func (n *Notification) BeforeCreate(*gorm.DB) error {
	if n.ID == "" {
		n.ID = id.New()
	}
	return nil
}

type Preference struct {
	UserID string `gorm:"primaryKey;size:26"`
	Type   string `gorm:"primaryKey;size:40"`
	InApp  bool   `gorm:"not null;default:true"`
	Email  bool   `gorm:"not null;default:false"`
}

func (Preference) TableName() string { return "notification_preferences" }

func Models() []any { return []any{&Notification{}, &Preference{}} }
