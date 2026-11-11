package auth

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

// OAuthIdentity links an external provider account to a local user. A user may
// link several providers; each provider account maps to exactly one user.
type OAuthIdentity struct {
	ID             string `gorm:"primaryKey;size:26"`
	UserID         string `gorm:"size:26;index;not null"`
	Provider       string `gorm:"size:20;uniqueIndex:idx_oauth_identity;not null"`
	ProviderUserID string `gorm:"size:191;uniqueIndex:idx_oauth_identity;not null"`
	Email          string `gorm:"size:255"`
	Username       string `gorm:"size:100"`
	AvatarURL      string `gorm:"size:500"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (OAuthIdentity) TableName() string { return "oauth_identities" }

func (o *OAuthIdentity) BeforeCreate(*gorm.DB) error {
	if o.ID == "" {
		o.ID = id.New()
	}
	return nil
}
