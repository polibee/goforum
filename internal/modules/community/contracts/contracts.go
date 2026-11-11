package contracts

import (
	"context"
	"time"
)

type TopicMeta struct {
	ID         string
	Number     int64
	Title      string
	AuthorID   string
	CategoryID string
	Status     string
	Visibility string
	IsLocked   bool
}

type PostMeta struct {
	ID          string
	TopicID     string
	TopicNumber int64
	TopicTitle  string
	AuthorID    string
	AuthorName  string
	PostNumber  int
}

// Reader lets neighbouring modules validate content and build notifications
// without importing community's models or repositories.
type Reader interface {
	TopicMeta(ctx context.Context, topicID string) (*TopicMeta, error)
	PostMeta(ctx context.Context, postID string) (*PostMeta, error)
	// TopicMetaBatch and PostMetaBatch resolve many ids in one query each,
	// which keeps list pages from issuing a query per row.
	TopicMetaBatch(ctx context.Context, topicIDs []string) (map[string]TopicMeta, error)
	PostMetaBatch(ctx context.Context, postIDs []string) (map[string]PostMeta, error)
}

// EngagementReader is implemented by the engagement module and consumed by
// community pages to render initial like/bookmark/follow state.
type EngagementReader interface {
	TopicState(ctx context.Context, userID, topicID string, postIDs []string) (liked map[string]bool, bookmarked, following bool, err error)
	ReactionSummary(ctx context.Context, userID, targetType string, ids []string) (counts map[string]map[string]int64, mine map[string]map[string]bool, err error)
	Following(ctx context.Context, userID, targetType, targetID string) (bool, error)
	FollowStats(ctx context.Context, userID string) (followers, following int64, err error)
	ListFollowers(ctx context.Context, userID string, limit, offset int) ([]string, int64, error)
	ListFollowing(ctx context.Context, userID string, limit, offset int) ([]string, int64, error)
	FollowedTargets(ctx context.Context, userID string) (authors, categories, tags []string, err error)
	TopicFollowers(ctx context.Context, topicID string, limit int) ([]string, error)
}

// BadgeView is the public badge summary shown on profiles.
type BadgeView struct {
	Name        string
	Description string
	Icon        string
	AwardedAt   time.Time
}

type BadgeReader interface {
	UserBadges(ctx context.Context, userID string) ([]BadgeView, error)
}

// ContentGuard is implemented by moderation and called before content is stored.
type TipService interface {
	Tip(ctx context.Context, fromUserID, toUserID, topicID string, points int64) error
}

type ContentGuard interface {
	Check(ctx context.Context, text string) (action string, matched string, err error)
}

// QueueSink is implemented by moderation for auto-review items.
type QueueSink interface {
	Enqueue(ctx context.Context, itemType, itemID, source string) error
}

// Moderator is the content-action surface used by moderation decisions.
type Moderator interface {
	HideTopic(ctx context.Context, topicID string) error
	HidePost(ctx context.Context, postID string) error
	DeleteTopic(ctx context.Context, topicID string) error
	DeletePost(ctx context.Context, postID string) error
	RestoreTopic(ctx context.Context, topicID string) error
	RestorePost(ctx context.Context, postID string) error
	LockTopic(ctx context.Context, topicID string, locked bool) error
	MoveTopic(ctx context.Context, topicID, categoryID string) error
}
