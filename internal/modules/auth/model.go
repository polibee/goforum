package auth

import (
	"strings"
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type User struct {
	ID              string `gorm:"primaryKey;size:26"`
	Username        string `gorm:"size:50;not null"`
	UsernameLower   string `gorm:"size:50;uniqueIndex;not null"`
	Email           string `gorm:"size:255;not null"`
	EmailLower      string `gorm:"size:255;uniqueIndex;not null"`
	PasswordHash    string `gorm:"size:255;not null"`
	Bio             string `gorm:"size:1000"`
	BioEnabled      bool   `gorm:"not null;default:true"`
	AvatarURL       string `gorm:"size:500"`
	CoverURL        string `gorm:"size:500"`
	Signature       string `gorm:"size:300"`
	DonateEnabled   bool   `gorm:"not null;default:false"`
	DonateMessage   string `gorm:"size:200"`
	DonateURL       string `gorm:"size:300"`
	IsSuperAdmin    bool   `gorm:"not null;default:false"`
	Status          string `gorm:"size:20;not null;default:active;index"`
	StatusUntil     *time.Time
	TrustLevel      int    `gorm:"not null;default:0"`
	Locale          string `gorm:"size:10"`
	EmailVerifiedAt *time.Time
	// TOTPSecret holds an AES-GCM encrypted base32 secret while enrollment is
	// pending or enabled. TOTPEnabled is only set after a valid code.
	TOTPSecret       string `gorm:"size:255"`
	TOTPEnabled      bool   `gorm:"not null;default:false"`
	TOTPLastStep     int64  `gorm:"not null;default:0"`
	TOTPRecoveryJSON string `gorm:"type:text"`
	LastSeenAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (User) TableName() string { return "users" }

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == "" {
		u.ID = id.New()
	}
	u.Username = strings.TrimSpace(u.Username)
	u.Email = strings.TrimSpace(u.Email)
	u.UsernameLower = strings.ToLower(u.Username)
	u.EmailLower = strings.ToLower(u.Email)
	if u.Status == "" {
		u.Status = "active"
	}
	return nil
}

// EmailToken backs both email verification and password reset links.
type EmailToken struct {
	ID        string    `gorm:"primaryKey;size:26"`
	UserID    string    `gorm:"size:26;index;not null"`
	Purpose   string    `gorm:"size:16;index;not null"`
	Payload   string    `gorm:"size:255"`
	TokenHash string    `gorm:"size:64;uniqueIndex;not null"`
	ExpiresAt time.Time `gorm:"index;not null"`
	UsedAt    *time.Time
	CreatedAt time.Time
}

func (EmailToken) TableName() string { return "email_tokens" }

// EmailSendLog records an account-triggered verification/security email so
// per-user daily and lifetime quotas survive restarts and apply across
// instances. It contains no message content or recipient address.
type EmailSendLog struct {
	ID        string    `gorm:"primaryKey;size:26"`
	UserID    string    `gorm:"size:26;index:idx_email_send_user_created,priority:1;not null"`
	Purpose   string    `gorm:"size:32;not null"`
	CreatedAt time.Time `gorm:"index:idx_email_send_user_created,priority:2"`
}

func (EmailSendLog) TableName() string { return "email_send_logs" }

type Group struct {
	ID              string `gorm:"primaryKey;size:26"`
	Name            string `gorm:"size:50;not null"`
	Slug            string `gorm:"size:50;uniqueIndex;not null"`
	Color           string `gorm:"size:16"`
	Priority        int    `gorm:"not null;default:0"`
	IsDefault       bool   `gorm:"not null;default:false"`
	PermissionsJSON string `gorm:"type:text"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (Group) TableName() string { return "groups" }

type UserGroup struct {
	UserID    string     `gorm:"primaryKey;size:26"`
	GroupID   string     `gorm:"primaryKey;size:26;index"`
	ExpiresAt *time.Time `gorm:"index"`
	CreatedAt time.Time
}

func (UserGroup) TableName() string { return "user_groups" }

func (g *Group) BeforeCreate(*gorm.DB) error {
	if g.ID == "" {
		g.ID = id.New()
	}
	return nil
}

func Models() []any {
	return []any{&User{}, &EmailToken{}, &EmailSendLog{}, &Group{}, &UserGroup{}, &OAuthIdentity{}}
}
