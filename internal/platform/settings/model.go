package settings

import (
	"encoding/json"
	"time"
)

type Setting struct {
	Namespace string    `gorm:"primaryKey;size:64"`
	Key       string    `gorm:"primaryKey;size:128"`
	ValueJSON string    `gorm:"type:text;not null"`
	UpdatedBy string    `gorm:"size:26"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (Setting) TableName() string { return "settings" }

type Revision struct {
	ID        string    `gorm:"primaryKey;size:26"`
	Namespace string    `gorm:"size:64;index"`
	Key       string    `gorm:"size:128;index"`
	OldJSON   string    `gorm:"type:text"`
	NewJSON   string    `gorm:"type:text"`
	ActorID   string    `gorm:"size:26;index"`
	CreatedAt time.Time `gorm:"index"`
}

func (Revision) TableName() string { return "setting_revisions" }

func Models() []any { return []any{&Setting{}, &Revision{}} }

func encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
