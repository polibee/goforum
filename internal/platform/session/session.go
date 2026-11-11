package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Session struct {
	ID        string    `gorm:"primaryKey;size:26"`
	UserID    string    `gorm:"size:26;index;not null"`
	TokenHash string    `gorm:"size:64;uniqueIndex;not null"`
	CSRF      string    `gorm:"size:64;not null"`
	IP        string    `gorm:"size:64"`
	UserAgent string    `gorm:"size:255"`
	ExpiresAt time.Time `gorm:"index;not null"`
	CreatedAt time.Time
}

func (Session) TableName() string { return "sessions" }

func Models() []any { return []any{&Session{}} }

type Store struct {
	db  *gorm.DB
	ttl time.Duration
}

func NewStore(db *gorm.DB, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	return &Store{db: db, ttl: ttl}
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) Create(ctx context.Context, userID, ip, userAgent string) (*Session, string, error) {
	token, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	if len([]rune(userAgent)) > 255 {
		userAgent = string([]rune(userAgent)[:255])
	}
	sess := &Session{
		ID:        id.New(),
		UserID:    userID,
		TokenHash: hashToken(token),
		CSRF:      csrf,
		IP:        ip,
		UserAgent: userAgent,
		ExpiresAt: time.Now().UTC().Add(s.ttl),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(sess).Error; err != nil {
		return nil, "", err
	}
	return sess, token, nil
}

func (s *Store) Get(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var sess Session
	err := s.db.WithContext(ctx).Where("token_hash = ?", hashToken(token)).First(&sess).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		_ = s.db.WithContext(ctx).Delete(&Session{}, "id = ?", sess.ID).Error
		return nil, gorm.ErrRecordNotFound
	}
	return &sess, nil
}

func (s *Store) Delete(ctx context.Context, sessionID string) error {
	return s.db.WithContext(ctx).Delete(&Session{}, "id = ?", sessionID).Error
}

func (s *Store) DeleteByUser(ctx context.Context, userID string) error {
	return s.db.WithContext(ctx).Delete(&Session{}, "user_id = ?", userID).Error
}

func (s *Store) ListByUser(ctx context.Context, userID string) ([]Session, error) {
	var rows []Session
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND expires_at > ?", userID, time.Now().UTC()).
		Order("created_at DESC").Find(&rows).Error
	return rows, err
}

func (s *Store) Revoke(ctx context.Context, userID, sessionID string) error {
	return s.db.WithContext(ctx).Delete(&Session{}, "id = ? AND user_id = ?", sessionID, userID).Error
}

func (s *Store) DeleteOthers(ctx context.Context, userID, currentID string) error {
	return s.db.WithContext(ctx).Delete(&Session{}, "user_id = ? AND id <> ?", userID, currentID).Error
}

func (s *Store) TTL() time.Duration { return s.ttl }
