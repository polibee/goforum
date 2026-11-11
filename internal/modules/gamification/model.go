package gamification

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type PointRule struct {
	ID         string `gorm:"primaryKey;size:26"`
	Code       string `gorm:"size:40;uniqueIndex;not null"`
	Name       string `gorm:"size:80;not null"`
	Points     int64  `gorm:"not null;default:0"`
	DailyLimit int    `gorm:"not null;default:0"`
	Enabled    bool   `gorm:"not null;default:true"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (PointRule) TableName() string { return "point_rules" }

type PointBalance struct {
	UserID      string `gorm:"primaryKey;size:26"`
	Balance     int64  `gorm:"not null;default:0"`
	TotalEarned int64  `gorm:"not null;default:0"`
	TotalSpent  int64  `gorm:"not null;default:0"`
	Level       int    `gorm:"not null;default:0"`
	UpdatedAt   time.Time
}

func (PointBalance) TableName() string { return "point_balances" }

type PointTransaction struct {
	ID           string    `gorm:"primaryKey;size:26"`
	UserID       string    `gorm:"size:26;not null;uniqueIndex:uk_point_tx"`
	Amount       int64     `gorm:"not null"`
	BalanceAfter int64     `gorm:"not null"`
	RuleCode     string    `gorm:"size:40;not null;uniqueIndex:uk_point_tx"`
	RefType      string    `gorm:"size:24;not null;uniqueIndex:uk_point_tx"`
	RefID        string    `gorm:"size:26;not null;uniqueIndex:uk_point_tx"`
	Description  string    `gorm:"size:255"`
	CreatedAt    time.Time `gorm:"index"`
}

func (PointTransaction) TableName() string { return "point_transactions" }

type Tip struct {
	ID         string `gorm:"primaryKey;size:26"`
	FromUserID string `gorm:"size:26;index;not null"`
	ToUserID   string `gorm:"size:26;index;not null"`
	TopicID    string `gorm:"size:26;index"`
	Points     int64  `gorm:"not null"`
	CreatedAt  time.Time
}

func (Tip) TableName() string { return "tips" }

func (t *Tip) BeforeCreate(*gorm.DB) error {
	if t.ID == "" {
		t.ID = id.New()
	}
	return nil
}

type Level struct {
	Level     int    `gorm:"primaryKey;autoIncrement:false"`
	Name      string `gorm:"size:50;not null"`
	MinPoints int64  `gorm:"not null;default:0"`
	Color     string `gorm:"size:16"`
}

func (Level) TableName() string { return "levels" }

type Checkin struct {
	ID          string    `gorm:"primaryKey;size:26"`
	UserID      string    `gorm:"size:26;not null;uniqueIndex:uk_checkin"`
	CheckinDate string    `gorm:"size:10;not null;uniqueIndex:uk_checkin"`
	Streak      int       `gorm:"not null;default:1"`
	Points      int64     `gorm:"not null;default:0"`
	CreatedAt   time.Time `gorm:"index"`
}

func (Checkin) TableName() string { return "checkins" }

type Badge struct {
	ID          string `gorm:"primaryKey;size:26"`
	Slug        string `gorm:"size:50;uniqueIndex;not null"`
	Name        string `gorm:"size:80;not null"`
	Description string `gorm:"size:255"`
	Icon        string `gorm:"size:255"`
	Hidden      bool   `gorm:"not null;default:false"`
	CreatedAt   time.Time
}

func (Badge) TableName() string { return "badges" }

type UserBadge struct {
	ID        string `gorm:"primaryKey;size:26"`
	UserID    string `gorm:"size:26;not null;uniqueIndex:uk_user_badge"`
	BadgeID   string `gorm:"size:26;not null;uniqueIndex:uk_user_badge"`
	AwardedBy string `gorm:"size:26"`
	AwardedAt time.Time
}

func (UserBadge) TableName() string { return "user_badges" }

type Invite struct {
	ID        string `gorm:"primaryKey;size:26"`
	Code      string `gorm:"size:16;uniqueIndex;not null"`
	InviterID string `gorm:"size:26;index;not null"`
	Email     string `gorm:"size:255"`
	Status    string `gorm:"size:16;not null;default:active;index"`
	UsedBy    string `gorm:"size:26"`
	UsedAt    *time.Time
	ExpiresAt time.Time `gorm:"index"`
	CreatedAt time.Time `gorm:"index"`
}

func (Invite) TableName() string { return "invites" }

type Lottery struct {
	ID          string `gorm:"primaryKey;size:26"`
	Title       string `gorm:"size:200;not null"`
	Description string `gorm:"size:1000"`
	Prize       string `gorm:"size:255;not null"`
	WinnerCount int    `gorm:"not null;default:1"`
	EntryCost   int64  `gorm:"not null;default:0"`
	MinLevel    int    `gorm:"not null;default:0"`
	StartsAt    time.Time
	EndsAt      time.Time `gorm:"index"`
	Status      string    `gorm:"size:16;not null;default:open;index"`
	Seed        string    `gorm:"size:64"`
	WinnersJSON string    `gorm:"type:text"`
	DrawnAt     *time.Time
	CreatedAt   time.Time
}

func (Lottery) TableName() string { return "lotteries" }

type LotteryEntry struct {
	ID        string    `gorm:"primaryKey;size:26"`
	LotteryID string    `gorm:"size:26;not null;uniqueIndex:uk_lottery_entry"`
	UserID    string    `gorm:"size:26;not null;uniqueIndex:uk_lottery_entry"`
	CreatedAt time.Time `gorm:"index"`
}

func (LotteryEntry) TableName() string { return "lottery_entries" }

type Bounty struct {
	ID             string `gorm:"primaryKey;size:26"`
	TopicID        string `gorm:"size:26;uniqueIndex;not null"`
	UserID         string `gorm:"size:26;index;not null"`
	Amount         int64  `gorm:"not null"`
	Status         string `gorm:"size:16;not null;default:funded;index"`
	AcceptedPostID string `gorm:"size:26"`
	SettledAt      *time.Time
	RefundedAt     *time.Time
	CreatedAt      time.Time `gorm:"index"`
}

func (Bounty) TableName() string { return "bounties" }

func (r *PointRule) BeforeCreate(*gorm.DB) error {
	if r.ID == "" {
		r.ID = id.New()
	}
	return nil
}
func (t *PointTransaction) BeforeCreate(*gorm.DB) error {
	if t.ID == "" {
		t.ID = id.New()
	}
	return nil
}
func (c *Checkin) BeforeCreate(*gorm.DB) error {
	if c.ID == "" {
		c.ID = id.New()
	}
	return nil
}
func (b *Badge) BeforeCreate(*gorm.DB) error {
	if b.ID == "" {
		b.ID = id.New()
	}
	return nil
}
func (b *UserBadge) BeforeCreate(*gorm.DB) error {
	if b.ID == "" {
		b.ID = id.New()
	}
	return nil
}
func (i *Invite) BeforeCreate(*gorm.DB) error {
	if i.ID == "" {
		i.ID = id.New()
	}
	return nil
}
func (l *Lottery) BeforeCreate(*gorm.DB) error {
	if l.ID == "" {
		l.ID = id.New()
	}
	return nil
}
func (e *LotteryEntry) BeforeCreate(*gorm.DB) error {
	if e.ID == "" {
		e.ID = id.New()
	}
	return nil
}
func (b *Bounty) BeforeCreate(*gorm.DB) error {
	if b.ID == "" {
		b.ID = id.New()
	}
	return nil
}

func Models() []any {
	return []any{
		&PointRule{}, &PointBalance{}, &PointTransaction{}, &Tip{}, &Level{}, &Checkin{},
		&Badge{}, &UserBadge{}, &Invite{}, &Lottery{}, &LotteryEntry{}, &Bounty{},
	}
}
