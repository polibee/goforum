package community

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Category struct {
	ID              string `gorm:"primaryKey;size:26"`
	ParentID        string `gorm:"size:26;index"`
	Name            string `gorm:"size:100;not null"`
	Slug            string `gorm:"size:100;uniqueIndex;not null"`
	Description     string `gorm:"size:500"`
	Color           string `gorm:"size:16"`
	Position        int    `gorm:"not null;default:0"`
	Visibility      string `gorm:"size:16;not null;default:public"`
	RequireApproval bool   `gorm:"not null;default:false"`
	MinTrustLevel   int    `gorm:"not null;default:0"`
	TopicCount      int64  `gorm:"not null;default:0"`
	PostCount       int64  `gorm:"not null;default:0"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (Category) TableName() string { return "categories" }

type Tag struct {
	ID          string `gorm:"primaryKey;size:26"`
	Name        string `gorm:"size:50;not null"`
	Slug        string `gorm:"size:50;uniqueIndex;not null"`
	Color       string `gorm:"size:16"`
	Description string `gorm:"size:255"`
	TopicCount  int64  `gorm:"not null;default:0"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Tag) TableName() string { return "tags" }

type Topic struct {
	ID              string `gorm:"primaryKey;size:26"`
	Number          int64  `gorm:"index"`
	CategoryID      string `gorm:"size:26;index;not null;index:idx_topic_feed,priority:2"`
	UserID          string `gorm:"size:26;index;not null"`
	Title           string `gorm:"size:255;not null"`
	Slug            string `gorm:"size:255;not null"`
	Excerpt         string `gorm:"size:500"`
	Archetype       string `gorm:"size:20;not null;default:regular"`
	Status          string `gorm:"size:16;not null;default:open;index;index:idx_topic_feed,priority:1"`
	IsPinned        bool   `gorm:"not null;default:false"`
	IsFeatured      bool   `gorm:"not null;default:false"`
	IsSolved        bool   `gorm:"not null;default:false"`
	AcceptedPostID  string `gorm:"size:26"`
	IsLocked        bool   `gorm:"not null;default:false"`
	ClosedAt        *time.Time
	ArchivedAt      *time.Time
	ReplyCount      int64  `gorm:"not null;default:0"`
	PostCount       int64  `gorm:"not null;default:1"`
	ViewCount       int64  `gorm:"not null;default:0"`
	LikeCount       int64  `gorm:"not null;default:0"`
	BookmarkCount   int64  `gorm:"not null;default:0"`
	LastPostID      string `gorm:"size:26"`
	LastPosterID    string `gorm:"size:26"`
	LastPostedAt    *time.Time
	BumpedAt        time.Time `gorm:"index;index:idx_topic_feed,priority:3"`
	SlowModeSeconds int       `gorm:"not null;default:0"`
	Visibility      string    `gorm:"size:16;not null;default:public"`
	CreatedAt       time.Time `gorm:"index"`
	UpdatedAt       time.Time
	DeletedAt       gorm.DeletedAt `gorm:"index"`
}

func (Topic) TableName() string { return "topics" }

// TopicNumber allocates short, sequential topic URLs (/t/123).
type Poll struct {
	ID        string `gorm:"primaryKey;size:26"`
	TopicID   string `gorm:"size:26;uniqueIndex;not null"`
	Question  string `gorm:"size:300;not null"`
	Multiple  bool   `gorm:"not null;default:false"`
	ClosesAt  *time.Time
	CreatedAt time.Time
}

func (Poll) TableName() string { return "polls" }

func (p *Poll) BeforeCreate(*gorm.DB) error {
	if p.ID == "" {
		p.ID = id.New()
	}
	return nil
}

type PollOption struct {
	ID        string `gorm:"primaryKey;size:26"`
	PollID    string `gorm:"size:26;index;not null"`
	Text      string `gorm:"size:120;not null"`
	Sort      int    `gorm:"not null;default:0"`
	Votes     int64  `gorm:"not null;default:0"`
	CreatedAt time.Time
}

func (PollOption) TableName() string { return "poll_options" }

func (o *PollOption) BeforeCreate(*gorm.DB) error {
	if o.ID == "" {
		o.ID = id.New()
	}
	return nil
}

type PollVote struct {
	ID        string    `gorm:"primaryKey;size:26"`
	PollID    string    `gorm:"size:26;not null;uniqueIndex:uk_poll_vote"`
	OptionID  string    `gorm:"size:26;not null;uniqueIndex:uk_poll_vote"`
	UserID    string    `gorm:"size:26;not null;uniqueIndex:uk_poll_vote"`
	CreatedAt time.Time `gorm:"index"`
}

func (PollVote) TableName() string { return "poll_votes" }

func (v *PollVote) BeforeCreate(*gorm.DB) error {
	if v.ID == "" {
		v.ID = id.New()
	}
	return nil
}

type TopicNumber struct {
	ID uint `gorm:"primaryKey;autoIncrement"`
}

func (TopicNumber) TableName() string { return "topic_numbers" }

type Post struct {
	ID            string `gorm:"primaryKey;size:26"`
	TopicID       string `gorm:"size:26;index;not null;index:idx_post_topic_page,priority:1"`
	UserID        string `gorm:"size:26;index;not null"`
	PostNumber    int    `gorm:"not null;index:idx_post_topic_page,priority:3"`
	ReplyToPostID string `gorm:"size:26"`
	Raw           string `gorm:"type:text;not null"`
	CookedHTML    string `gorm:"type:text;not null"`
	Status        string `gorm:"size:16;not null;default:visible;index:idx_post_topic_page,priority:2"`
	Action        string `gorm:"size:16;not null;default:create"`
	LikeCount     int64  `gorm:"not null;default:0"`
	EditCount     int    `gorm:"not null;default:0"`
	EditedAt      *time.Time
	DeletedBy     string    `gorm:"size:26"`
	IP            string    `gorm:"size:45"`
	UserAgent     string    `gorm:"size:255"`
	CreatedAt     time.Time `gorm:"index"`
	UpdatedAt     time.Time
	DeletedAt     gorm.DeletedAt `gorm:"index"`
}

func (Post) TableName() string { return "posts" }

type PostRevision struct {
	ID        string `gorm:"primaryKey;size:26"`
	PostID    string `gorm:"size:26;index;not null"`
	UserID    string `gorm:"size:26;index;not null"`
	Raw       string `gorm:"type:text;not null"`
	Reason    string `gorm:"size:255"`
	CreatedAt time.Time
}

func (PostRevision) TableName() string { return "post_revisions" }

type TopicTag struct {
	TopicID string `gorm:"primaryKey;size:26"`
	TagID   string `gorm:"primaryKey;size:26;index"`
}

func (TopicTag) TableName() string { return "topic_tags" }

type TopicUser struct {
	TopicID            string `gorm:"primaryKey;size:26"`
	UserID             string `gorm:"primaryKey;size:26;index"`
	LastReadPostNumber int    `gorm:"not null;default:0"`
	HighestPostNumber  int    `gorm:"not null;default:0"`
	NotificationLevel  string `gorm:"size:16;not null;default:normal"`
	LastVisitedAt      time.Time
	TotalMsecsViewed   int64 `gorm:"not null;default:0"`
}

func (TopicUser) TableName() string { return "topic_users" }

func (c *Category) BeforeCreate(*gorm.DB) error {
	if c.ID == "" {
		c.ID = id.New()
	}
	if c.Visibility == "" {
		c.Visibility = "public"
	}
	return nil
}

func (t *Tag) BeforeCreate(*gorm.DB) error {
	if t.ID == "" {
		t.ID = id.New()
	}
	return nil
}

func (t *Topic) BeforeCreate(*gorm.DB) error {
	if t.ID == "" {
		t.ID = id.New()
	}
	if t.Status == "" {
		t.Status = "open"
	}
	if t.Visibility == "" {
		t.Visibility = "public"
	}
	if t.Archetype == "" {
		t.Archetype = "regular"
	}
	return nil
}

func (p *Post) BeforeCreate(*gorm.DB) error {
	if p.ID == "" {
		p.ID = id.New()
	}
	if p.Status == "" {
		p.Status = "visible"
	}
	if p.Action == "" {
		p.Action = "create"
	}
	return nil
}

func (r *PostRevision) BeforeCreate(*gorm.DB) error {
	if r.ID == "" {
		r.ID = id.New()
	}
	return nil
}

func Models() []any {
	return []any{
		&Category{}, &Tag{}, &Topic{}, &TopicNumber{}, &Post{}, &PostRevision{}, &TopicTag{}, &TopicUser{}, &Poll{}, &PollOption{}, &PollVote{},
	}
}
