package site

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Page struct {
	ID             string `gorm:"primaryKey;size:26"`
	Slug           string `gorm:"size:120;uniqueIndex;not null"`
	Title          string `gorm:"size:200;not null"`
	BodyRaw        string `gorm:"type:text"`
	BodyHTML       string `gorm:"type:text"`
	Status         string `gorm:"size:16;not null;default:draft;index"`
	SEOTitle       string `gorm:"size:200"`
	SEODescription string `gorm:"size:300"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Page) TableName() string { return "pages" }

func (p *Page) BeforeCreate(*gorm.DB) error {
	if p.ID == "" {
		p.ID = id.New()
	}
	return nil
}

func Models() []any { return []any{&Page{}, &NavItem{}, &SidebarWidget{}} }

// NavItem is an admin-managed header or footer link.
type NavItem struct {
	ID         string `gorm:"primaryKey;size:26"`
	Location   string `gorm:"size:16;not null;index"` // header | footer
	Label      string `gorm:"size:80;not null"`
	URL        string `gorm:"size:300;not null"`
	Permission string `gorm:"size:80"`
	Section    string `gorm:"size:80"`
	Sort       int    `gorm:"not null;default:0"`
	Enabled    bool   `gorm:"not null;default:true;index"`
	External   bool   `gorm:"not null;default:false"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (NavItem) TableName() string { return "nav_items" }

func (n *NavItem) BeforeCreate(*gorm.DB) error {
	if n.ID == "" {
		n.ID = id.New()
	}
	return nil
}

// SidebarWidget is an admin-managed card shown in the forum sidebar.
type SidebarWidget struct {
	ID        string `gorm:"primaryKey;size:26"`
	Key       string `gorm:"size:40;index"` // system cards: user_card, categories, trending, tags
	Title     string `gorm:"size:120;not null"`
	Type      string `gorm:"size:20;not null;default:links"` // links | text | user_card | categories | trending | tags
	Body      string `gorm:"type:text"`
	// Location decides which pages render this card. "home" only on the feed,
	// "topic" only on topic/thread pages, "all" everywhere. Empty means "all"
	// so rows created before this field existed keep working.
	Location  string `gorm:"size:16;not null;default:all;index"`
	Sort      int    `gorm:"not null;default:0"`
	Enabled   bool   `gorm:"not null;default:true;index"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (SidebarWidget) TableName() string { return "sidebar_widgets" }

func (w *SidebarWidget) BeforeCreate(*gorm.DB) error {
	if w.ID == "" {
		w.ID = id.New()
	}
	return nil
}
