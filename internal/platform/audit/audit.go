package audit

import (
	"context"
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Entry struct {
	ID         string    `gorm:"primaryKey;size:26"`
	ActorID    string    `gorm:"size:26;index"`
	Action     string    `gorm:"size:64;index;not null"`
	TargetType string    `gorm:"size:24;index"`
	TargetID   string    `gorm:"size:26;index"`
	Reason     string    `gorm:"size:500"`
	BeforeJSON string    `gorm:"type:text"`
	AfterJSON  string    `gorm:"type:text"`
	IP         string    `gorm:"size:45"`
	RequestID  string    `gorm:"size:26"`
	CreatedAt  time.Time `gorm:"index"`
}

func (Entry) TableName() string { return "audit_logs" }

func (e *Entry) BeforeCreate(*gorm.DB) error {
	if e.ID == "" {
		e.ID = id.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	return nil
}

func Models() []any { return []any{&Entry{}} }

type Writer interface {
	Write(ctx context.Context, entry Entry) error
}

type Service struct{ db *gorm.DB }

func New(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Write(ctx context.Context, entry Entry) error {
	return s.db.WithContext(ctx).Create(&entry).Error
}

type Filter struct {
	ActorID    string
	Action     string
	TargetType string
	TargetID   string
	Page       int
	PerPage    int
}

func (s *Service) List(ctx context.Context, f Filter) ([]Entry, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PerPage < 1 || f.PerPage > 100 {
		f.PerPage = 20
	}
	q := s.db.WithContext(ctx).Model(&Entry{})
	if f.ActorID != "" {
		q = q.Where("actor_id = ?", f.ActorID)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.TargetType != "" {
		q = q.Where("target_type = ?", f.TargetType)
	}
	if f.TargetID != "" {
		q = q.Where("target_id = ?", f.TargetID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Entry
	err := q.Order("created_at DESC").Offset((f.Page - 1) * f.PerPage).Limit(f.PerPage).Find(&rows).Error
	return rows, total, err
}
