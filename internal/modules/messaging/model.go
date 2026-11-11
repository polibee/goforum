package messaging

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Conversation struct {
	ID               string     `gorm:"primaryKey;size:26"`
	Type             string     `gorm:"size:16;not null;default:direct"`
	Title            string     `gorm:"size:255"`
	CreatedBy        string     `gorm:"size:26;index"`
	LastMessageID    string     `gorm:"size:26"`
	LastMessageAt    *time.Time `gorm:"index"`
	ParticipantCount int        `gorm:"not null;default:0"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (Conversation) TableName() string { return "conversations" }

type Participant struct {
	ConversationID string `gorm:"primaryKey;size:26"`
	UserID         string `gorm:"primaryKey;size:26;index"`
	LastReadAt     *time.Time
	Muted          bool `gorm:"not null;default:false"`
	JoinedAt       time.Time
}

func (Participant) TableName() string { return "conversation_participants" }

type Message struct {
	ID             string         `gorm:"primaryKey;size:26"`
	ConversationID string         `gorm:"size:26;index;not null"`
	SenderID       string         `gorm:"size:26;index;not null"`
	Raw            string         `gorm:"type:text;not null"`
	CookedHTML     string         `gorm:"type:text;not null"`
	Status         string         `gorm:"size:16;not null;default:visible"`
	CreatedAt      time.Time      `gorm:"index"`
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}

func (Message) TableName() string { return "messages" }

type MessageBlock struct {
	BlockerID string `gorm:"primaryKey;size:26"`
	BlockedID string `gorm:"primaryKey;size:26;index"`
	CreatedAt time.Time
}

func (MessageBlock) TableName() string { return "message_blocks" }

func (c *Conversation) BeforeCreate(*gorm.DB) error {
	if c.ID == "" {
		c.ID = id.New()
	}
	if c.Type == "" {
		c.Type = "direct"
	}
	return nil
}

func (m *Message) BeforeCreate(*gorm.DB) error {
	if m.ID == "" {
		m.ID = id.New()
	}
	if m.Status == "" {
		m.Status = "visible"
	}
	return nil
}

func Models() []any { return []any{&Conversation{}, &Participant{}, &Message{}, &MessageBlock{}} }
