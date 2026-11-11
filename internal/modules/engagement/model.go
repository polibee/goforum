package engagement

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Reaction struct {
	ID         string    `gorm:"primaryKey;size:26"`
	UserID     string    `gorm:"size:26;not null;uniqueIndex:uk_reaction"`
	TargetType string    `gorm:"size:20;not null;uniqueIndex:uk_reaction;index:idx_reaction_target,priority:1"`
	TargetID   string    `gorm:"size:26;not null;uniqueIndex:uk_reaction;index:idx_reaction_target,priority:2"`
	Reaction   string    `gorm:"size:16;not null;default:like;uniqueIndex:uk_reaction"`
	CreatedAt  time.Time `gorm:"index"`
}

func (Reaction) TableName() string { return "reactions" }

type Bookmark struct {
	ID         string    `gorm:"primaryKey;size:26"`
	UserID     string    `gorm:"size:26;not null;uniqueIndex:uk_bookmark"`
	TargetType string    `gorm:"size:20;not null;uniqueIndex:uk_bookmark"`
	TargetID   string    `gorm:"size:26;not null;uniqueIndex:uk_bookmark"`
	Note       string    `gorm:"size:255"`
	CreatedAt  time.Time `gorm:"index"`
}

func (Bookmark) TableName() string { return "bookmarks" }

type Subscription struct {
	ID         string    `gorm:"primaryKey;size:26"`
	UserID     string    `gorm:"size:26;not null;uniqueIndex:uk_follow_subscription"`
	TargetType string    `gorm:"size:20;not null;uniqueIndex:uk_follow_subscription;index:idx_follow_target,priority:1"`
	TargetID   string    `gorm:"size:26;not null;uniqueIndex:uk_follow_subscription;index:idx_follow_target,priority:2"`
	Level      string    `gorm:"size:16;not null;default:normal"`
	CreatedAt  time.Time `gorm:"index"`
}

func (Subscription) TableName() string { return "follow_subscriptions" }

func (r *Reaction) BeforeCreate(*gorm.DB) error {
	if r.ID == "" {
		r.ID = id.New()
	}
	return nil
}

func (b *Bookmark) BeforeCreate(*gorm.DB) error {
	if b.ID == "" {
		b.ID = id.New()
	}
	return nil
}

func (s *Subscription) BeforeCreate(*gorm.DB) error {
	if s.ID == "" {
		s.ID = id.New()
	}
	return nil
}

func Models() []any { return []any{&Reaction{}, &Bookmark{}, &Subscription{}} }
