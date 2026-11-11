package contracts

import (
	"context"
	"time"
)

type UserView struct {
	ID              string     `json:"id"`
	Username        string     `json:"username"`
	Email           string     `json:"email"`
	Bio             string     `json:"bio"`
	BioEnabled      bool       `json:"bio_enabled"`
	AvatarURL       string     `json:"avatar_url,omitempty"`
	CoverURL        string     `json:"cover_url,omitempty"`
	Signature       string     `json:"signature,omitempty"`
	DonateEnabled   bool       `json:"donate_enabled"`
	DonateMessage   string     `json:"donate_message,omitempty"`
	DonateURL       string     `json:"donate_url,omitempty"`
	SuperAdmin      bool       `json:"super_admin"`
	Status          string     `json:"status"`
	StatusUntil     *time.Time `json:"status_until,omitempty"`
	TrustLevel      int        `json:"trust_level"`
	Locale          string     `json:"locale,omitempty"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	TOTPEnabled     bool       `json:"totp_enabled"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
}

type UserSummary struct {
	ID            string    `json:"id"`
	Username      string    `json:"username"`
	Bio           string    `json:"bio"`
	Signature     string    `json:"signature,omitempty"`
	AvatarURL     string    `json:"avatar_url,omitempty"`
	DonateEnabled bool      `json:"donate_enabled"`
	DonateMessage string    `json:"donate_message,omitempty"`
	DonateURL     string    `json:"donate_url,omitempty"`
	TrustLevel    int       `json:"trust_level"`
	CreatedAt     time.Time `json:"created_at"`
}

// UserReader is the batch read surface used by content modules.
type UserReader interface {
	GetSummaries(ctx context.Context, ids []string) (map[string]UserSummary, error)
	FindByUsernames(ctx context.Context, usernames []string) (map[string]UserSummary, error)
}

type GroupView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Color       string   `json:"color"`
	Priority    int      `json:"priority"`
	IsDefault   bool     `json:"is_default"`
	Permissions []string `json:"permissions"`
}

type GroupInput struct {
	ID          string
	Name        string
	Slug        string
	Color       string
	Priority    int
	Permissions []string
}

// ActivityCounts is one user's aggregated activity, used to derive trust level.
type ActivityCounts struct {
	Topics int64
	Posts  int64
	Likes  int64
}

// ActivityReader is implemented by community for trust-level calculation.
type ActivityReader interface {
	UserActivity(ctx context.Context, userID string) (topics, posts, likes int64, err error)
	// UserActivityBatch returns counts for many users in a few aggregate
	// queries instead of three per user. Users with no activity are omitted.
	UserActivityBatch(ctx context.Context, userIDs []string) (map[string]ActivityCounts, error)
}

// InviteService is implemented by gamification and consumed during registration.
type InviteService interface {
	ValidateInvite(ctx context.Context, code, email string) error
	ConsumeInvite(ctx context.Context, code, userID, email string) error
}

// Authenticator is the only auth surface other modules may depend on.
type Authenticator interface {
	Authenticate(ctx context.Context, login, plainPassword string) (*UserView, error)
	GetByID(ctx context.Context, id string) (*UserView, error)
	GetByUsername(ctx context.Context, username string) (*UserView, error)
	GetSummaries(ctx context.Context, ids []string) (map[string]UserSummary, error)
	FindByUsernames(ctx context.Context, usernames []string) (map[string]UserSummary, error)
	Count(ctx context.Context) (int64, error)
	CountPending(ctx context.Context) (int64, error)
	List(ctx context.Context, limit, offset int) ([]UserView, error)
	ListFiltered(ctx context.Context, query, status string, includeEmail bool, limit, offset int) ([]UserView, int64, error)
	SetStatus(ctx context.Context, id, status string) error
	SetStatusUntil(ctx context.Context, id, status string, until *time.Time) error
	PermissionsOf(ctx context.Context, userID string) (map[string]struct{}, error)
	ListGroups(ctx context.Context) ([]GroupView, error)
	GroupsOf(ctx context.Context, userID string) ([]GroupView, error)
	CreateGroup(ctx context.Context, in GroupInput) (*GroupView, error)
	UpdateGroup(ctx context.Context, in GroupInput) error
	DeleteGroup(ctx context.Context, id string) error
	SetGroups(ctx context.Context, userID string, groupIDs []string) error
	RecomputeTrustLevels(ctx context.Context) (int, error)
	SetTrustLevel(ctx context.Context, userID string, level int) error
}

// TwoFactorAuthenticator is implemented by the auth service. The admin module
// uses it after its own password check so /admin/login cannot bypass TOTP.
type TwoFactorAuthenticator interface {
	TwoFactorMode(ctx context.Context, user *UserView) string
	CreateLoginChallenge(ctx context.Context, userID, next string) (string, error)
}

// OAuthProviderView is the minimal provider info shared by login and register
// pages. The admin shell reuses the same template, so the shape lives here.
type OAuthProviderView struct {
	Key  string
	Name string
	Icon string
}
