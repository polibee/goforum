package engagement

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	cmod "goforum/internal/modules/community/contracts"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/id"
)

type Service struct {
	db      *gorm.DB
	content cmod.Reader
	bus     events.Bus
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Configure(content cmod.Reader, bus events.Bus) {
	s.content = content
	s.bus = bus
}

type target struct {
	TopicID  string
	AuthorID string
	Title    string
}

func (s *Service) resolve(ctx context.Context, targetType, targetID string) (*target, error) {
	if s.content == nil {
		return &target{}, nil
	}
	switch targetType {
	case "post":
		meta, err := s.content.PostMeta(ctx, targetID)
		if err != nil {
			return nil, err
		}
		return &target{TopicID: meta.TopicID, AuthorID: meta.AuthorID, Title: meta.TopicTitle}, nil
	case "topic":
		meta, err := s.content.TopicMeta(ctx, targetID)
		if err != nil {
			return nil, err
		}
		return &target{TopicID: meta.ID, AuthorID: meta.AuthorID, Title: meta.Title}, nil
	default:
		return &target{}, nil
	}
}

func (s *Service) ToggleReaction(ctx context.Context, userID, targetType, targetID, reaction string) (bool, int64, error) {
	if reaction == "" {
		reaction = "like"
	}
	if reaction != "like" && reaction != "thanks" && reaction != "dislike" {
		n := len([]rune(strings.TrimPrefix(reaction, "emoji:")))
		if !strings.HasPrefix(reaction, "emoji:") || n < 1 || n > 8 {
			return false, 0, httpx.Invalid(map[string]string{"reaction": "不支持的表态"})
		}
	}
	t, err := s.resolve(ctx, targetType, targetID)
	if err != nil {
		return false, 0, err
	}
	active := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing Reaction
		err := tx.Where("user_id = ? AND target_type = ? AND target_id = ? AND reaction = ?",
			userID, targetType, targetID, reaction).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&Reaction{
				ID: id.New(), UserID: userID, TargetType: targetType, TargetID: targetID,
				Reaction: reaction, CreatedAt: time.Now().UTC(),
			}).Error
		}
		if err != nil {
			return err
		}
		active = true
		return tx.Delete(&Reaction{}, "id = ?", existing.ID).Error
	})
	if err != nil {
		return false, 0, err
	}
	// toggle semantics: active=true means it was removed
	nowActive := !active
	var count int64
	_ = s.db.WithContext(ctx).Model(&Reaction{}).
		Where("target_type = ? AND target_id = ? AND reaction = ?", targetType, targetID, reaction).Count(&count).Error
	s.publish(ctx, eventName("reaction", nowActive), t, map[string]any{
		"target_type": targetType, "target_id": targetID,
		"topic_id": t.TopicID, "actor_id": userID, "author_id": t.AuthorID,
		"title": t.Title, "reaction": reaction,
	})
	return nowActive, count, nil
}

// ReactionSummary returns reaction counts and the caller's own reactions.
func (s *Service) ReactionSummary(ctx context.Context, userID, targetType string, ids []string) (map[string]map[string]int64, map[string]map[string]bool, error) {
	counts := make(map[string]map[string]int64, len(ids))
	mine := make(map[string]map[string]bool, len(ids))
	if len(ids) == 0 {
		return counts, mine, nil
	}
	var rows []Reaction
	if err := s.db.WithContext(ctx).Where("target_type = ? AND target_id IN ?", targetType, ids).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if counts[r.TargetID] == nil {
			counts[r.TargetID] = map[string]int64{}
		}
		counts[r.TargetID][r.Reaction]++
		if userID != "" && r.UserID == userID {
			if mine[r.TargetID] == nil {
				mine[r.TargetID] = map[string]bool{}
			}
			mine[r.TargetID][r.Reaction] = true
		}
	}
	return counts, mine, nil
}

func (s *Service) ToggleBookmark(ctx context.Context, userID, targetType, targetID string) (bool, int64, error) {
	t, err := s.resolve(ctx, targetType, targetID)
	if err != nil {
		return false, 0, err
	}
	active := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing Bookmark
		err := tx.Where("user_id = ? AND target_type = ? AND target_id = ?", userID, targetType, targetID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&Bookmark{
				ID: id.New(), UserID: userID, TargetType: targetType, TargetID: targetID,
				CreatedAt: time.Now().UTC(),
			}).Error
		}
		if err != nil {
			return err
		}
		active = true
		return tx.Delete(&Bookmark{}, "id = ?", existing.ID).Error
	})
	if err != nil {
		return false, 0, err
	}
	nowActive := !active
	var count int64
	_ = s.db.WithContext(ctx).Model(&Bookmark{}).
		Where("target_type = ? AND target_id = ?", targetType, targetID).Count(&count).Error
	s.publish(ctx, eventName("bookmark", nowActive), t, map[string]any{
		"target_type": targetType, "target_id": targetID,
		"topic_id": t.TopicID, "actor_id": userID, "author_id": t.AuthorID, "title": t.Title,
	})
	return nowActive, count, nil
}

func (s *Service) ToggleSubscription(ctx context.Context, userID, targetType, targetID string) (bool, error) {
	switch targetType {
	case "topic", "category", "tag", "user":
	default:
		return false, httpx.Invalid(map[string]string{"target_type": "不支持的关注类型"})
	}
	if targetType == "topic" {
		if _, err := s.resolve(ctx, targetType, targetID); err != nil {
			return false, err
		}
	}
	active := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing Subscription
		err := tx.Where("user_id = ? AND target_type = ? AND target_id = ?", userID, targetType, targetID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&Subscription{
				ID: id.New(), UserID: userID, TargetType: targetType, TargetID: targetID,
				Level: "watching", CreatedAt: time.Now().UTC(),
			}).Error
		}
		if err != nil {
			return err
		}
		active = true
		return tx.Delete(&Subscription{}, "id = ?", existing.ID).Error
	})
	if err != nil {
		return false, err
	}
	return !active, nil
}

func (s *Service) TopicFollowers(ctx context.Context, topicID string, limit int) ([]string, error) {
	if topicID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	ids := make([]string, 0, limit)
	err := s.db.WithContext(ctx).Model(&Subscription{}).
		Where("target_type = ? AND target_id = ?", "topic", topicID).
		Order("created_at ASC").Limit(limit).Pluck("user_id", &ids).Error
	return ids, err
}

func (s *Service) TopicState(ctx context.Context, userID, topicID string, postIDs []string) (map[string]bool, bool, bool, error) {
	liked := make(map[string]bool, len(postIDs))
	if userID == "" {
		return liked, false, false, nil
	}
	if len(postIDs) > 0 {
		var rows []Reaction
		if err := s.db.WithContext(ctx).Where("user_id = ? AND target_type = ? AND target_id IN ?", userID, "post", postIDs).
			Find(&rows).Error; err != nil {
			return nil, false, false, err
		}
		for _, r := range rows {
			liked[r.TargetID] = true
		}
	}
	var bookmarks int64
	_ = s.db.WithContext(ctx).Model(&Bookmark{}).
		Where("user_id = ? AND target_type = ? AND target_id = ?", userID, "topic", topicID).Count(&bookmarks).Error
	var subs int64
	_ = s.db.WithContext(ctx).Model(&Subscription{}).
		Where("user_id = ? AND target_type = ? AND target_id = ?", userID, "topic", topicID).Count(&subs).Error
	return liked, bookmarks > 0, subs > 0, nil
}

func (s *Service) FollowStats(ctx context.Context, userID string) (followers, following int64, err error) {
	if userID == "" {
		return 0, 0, nil
	}
	if err = s.db.WithContext(ctx).Model(&Subscription{}).
		Where("target_type = ? AND target_id = ?", "user", userID).Count(&followers).Error; err != nil {
		return
	}
	err = s.db.WithContext(ctx).Model(&Subscription{}).
		Where("user_id = ? AND target_type = ?", userID, "user").Count(&following).Error
	return
}

func (s *Service) ListFollowers(ctx context.Context, userID string, limit, offset int) ([]string, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var total int64
	if err := s.db.WithContext(ctx).Model(&Subscription{}).
		Where("target_type = ? AND target_id = ?", "user", userID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var ids []string
	err := s.db.WithContext(ctx).Model(&Subscription{}).
		Where("target_type = ? AND target_id = ?", "user", userID).
		Order("created_at DESC").Limit(limit).Offset(offset).Pluck("user_id", &ids).Error
	return ids, total, err
}

func (s *Service) ListFollowing(ctx context.Context, userID string, limit, offset int) ([]string, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var total int64
	if err := s.db.WithContext(ctx).Model(&Subscription{}).
		Where("user_id = ? AND target_type = ?", userID, "user").Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var ids []string
	err := s.db.WithContext(ctx).Model(&Subscription{}).
		Where("user_id = ? AND target_type = ?", userID, "user").
		Order("created_at DESC").Limit(limit).Offset(offset).Pluck("target_id", &ids).Error
	return ids, total, err
}

func (s *Service) FollowedTargets(ctx context.Context, userID string) (authors, categories, tags []string, err error) {
	if userID == "" {
		return nil, nil, nil, nil
	}
	var rows []Subscription
	err = s.db.WithContext(ctx).Where("user_id = ? AND target_type IN ?", userID, []string{"user", "category", "tag"}).
		Find(&rows).Error
	if err != nil {
		return nil, nil, nil, err
	}
	for _, r := range rows {
		switch r.TargetType {
		case "user":
			authors = append(authors, r.TargetID)
		case "category":
			categories = append(categories, r.TargetID)
		case "tag":
			tags = append(tags, r.TargetID)
		}
	}
	return
}

func (s *Service) Following(ctx context.Context, userID, targetType, targetID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	var n int64
	err := s.db.WithContext(ctx).Model(&Subscription{}).
		Where("user_id = ? AND target_type = ? AND target_id = ?", userID, targetType, targetID).Count(&n).Error
	return n > 0, err
}

type SubscriptionRow struct {
	TargetID   string
	TargetType string
	Title      string
	Subtitle   string
	URL        string
	AvatarURL  string
	CreatedAt  time.Time
}

// ListSubscriptions returns followed users, topics, categories or tags with
// enough metadata to render a management list.
func (s *Service) ListSubscriptions(ctx context.Context, userID, targetType string, limit, offset int) ([]SubscriptionRow, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	base := s.db.WithContext(ctx).Table("follow_subscriptions AS s")
	var selectSQL, joinSQL string
	switch targetType {
	case "topic":
		selectSQL = "s.target_id AS target_id, t.title AS title, t.number AS number, t.reply_count AS reply_count, s.created_at AS created_at"
		joinSQL = "JOIN topics t ON t.id = s.target_id AND t.deleted_at IS NULL"
	case "user":
		selectSQL = "s.target_id AS target_id, u.username AS title, u.avatar_url AS avatar_url, u.trust_level AS trust_level, s.created_at AS created_at"
		joinSQL = "JOIN users u ON u.id = s.target_id"
	case "category":
		selectSQL = "s.target_id AS target_id, c.name AS title, c.slug AS slug, s.created_at AS created_at"
		joinSQL = "JOIN categories c ON c.id = s.target_id"
	case "tag":
		selectSQL = "s.target_id AS target_id, tg.name AS title, tg.slug AS slug, s.created_at AS created_at"
		joinSQL = "JOIN tags tg ON tg.id = s.target_id"
	default:
		return nil, 0, httpx.Invalid(map[string]string{"type": "不支持的关注类型"})
	}
	base = base.Joins(joinSQL).Where("s.user_id = ? AND s.target_type = ?", userID, targetType)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var raw []struct {
		TargetID   string    `gorm:"column:target_id"`
		Title      string    `gorm:"column:title"`
		Number     int64     `gorm:"column:number"`
		ReplyCount int64     `gorm:"column:reply_count"`
		Slug       string    `gorm:"column:slug"`
		AvatarURL  string    `gorm:"column:avatar_url"`
		TrustLevel int       `gorm:"column:trust_level"`
		CreatedAt  time.Time `gorm:"column:created_at"`
	}
	if err := base.Select(selectSQL).Order("s.created_at DESC").Limit(limit).Offset(offset).Scan(&raw).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]SubscriptionRow, 0, len(raw))
	for _, r := range raw {
		row := SubscriptionRow{TargetID: r.TargetID, TargetType: targetType, Title: r.Title, CreatedAt: r.CreatedAt}
		switch targetType {
		case "topic":
			row.URL = "/t/" + strconv.FormatInt(r.Number, 10)
			row.Subtitle = strconv.FormatInt(r.ReplyCount, 10) + " 回复"
		case "user":
			row.URL = "/u/" + r.Title
			row.AvatarURL = r.AvatarURL
			row.Subtitle = "TL" + strconv.Itoa(r.TrustLevel)
		case "category":
			row.URL = "/c/" + r.Slug
		case "tag":
			row.URL = "/tags/" + r.Slug
		}
		rows = append(rows, row)
	}
	return rows, total, nil
}

type BookmarkRow struct {
	ID         string
	TargetType string
	TargetID   string
	Title      string
	URL        string
	CreatedAt  time.Time
}

func (s *Service) ListBookmarks(ctx context.Context, userID string, limit, offset int) ([]BookmarkRow, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var total int64
	if err := s.db.WithContext(ctx).Model(&Bookmark{}).Where("user_id = ?", userID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Bookmark
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]BookmarkRow, 0, len(rows))
	if len(rows) == 0 || s.content == nil {
		return out, total, nil
	}
	// Resolve every bookmarked target in two queries instead of one per row.
	topicIDs := make([]string, 0, len(rows))
	postIDs := make([]string, 0, len(rows))
	for _, b := range rows {
		switch b.TargetType {
		case "topic":
			topicIDs = append(topicIDs, b.TargetID)
		case "post":
			postIDs = append(postIDs, b.TargetID)
		}
	}
	topics, err := s.content.TopicMetaBatch(ctx, topicIDs)
	if err != nil {
		return nil, 0, err
	}
	posts, err := s.content.PostMetaBatch(ctx, postIDs)
	if err != nil {
		return nil, 0, err
	}
	for _, b := range rows {
		row := BookmarkRow{ID: b.ID, TargetType: b.TargetType, TargetID: b.TargetID, CreatedAt: b.CreatedAt}
		switch b.TargetType {
		case "topic":
			if meta, ok := topics[b.TargetID]; ok {
				row.Title = meta.Title
				if meta.Number > 0 {
					row.URL = "/t/" + strconv.FormatInt(meta.Number, 10)
				} else {
					row.URL = "/t/" + meta.ID
				}
			}
		case "post":
			if meta, ok := posts[b.TargetID]; ok {
				row.Title = meta.TopicTitle
				if meta.TopicNumber > 0 {
					row.URL = "/t/" + strconv.FormatInt(meta.TopicNumber, 10) + "#post-" + meta.ID
				} else {
					row.URL = "/t/" + meta.TopicID + "#post-" + meta.ID
				}
			}
		}
		if row.Title == "" {
			continue
		}
		out = append(out, row)
	}
	return out, total, nil
}

func eventName(kind string, active bool) string {
	if active {
		return kind + ".added"
	}
	return kind + ".removed"
}

func (s *Service) publish(ctx context.Context, name string, t *target, payload map[string]any) {
	if s.bus == nil {
		return
	}
	b, _ := json.Marshal(payload)
	_ = s.bus.Publish(ctx, events.NewEvent(name, "engagement", t.TopicID, b))
}
