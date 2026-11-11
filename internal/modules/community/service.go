package community

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"goforum/internal/modules/auth/contracts"
	cmod "goforum/internal/modules/community/contracts"
	"goforum/internal/platform/cache"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/id"
	"goforum/internal/platform/markdown"
	"goforum/internal/platform/searchx"
	"goforum/internal/platform/settings"
)

type Service struct {
	db         *gorm.DB
	settings   *settings.Store
	bus        events.Bus
	md         *markdown.Renderer
	users      contracts.UserReader
	engagement cmod.EngagementReader
	search     searchx.Engine
	guard      cmod.ContentGuard
	queue      cmod.QueueSink
	tips       cmod.TipService

	cache      cache.Cache
	catMu      sync.RWMutex
	catCache   []Category
	catCacheAt time.Time
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Configure(st *settings.Store, bus events.Bus, md *markdown.Renderer, users contracts.UserReader) {
	s.settings = st
	s.bus = bus
	s.md = md
	s.users = users
}

func (s *Service) SetEngagement(e cmod.EngagementReader) { s.engagement = e }
func (s *Service) SetSearch(e searchx.Engine)            { s.search = e }
func (s *Service) SetGuard(g cmod.ContentGuard)          { s.guard = g }
func (s *Service) SetQueue(q cmod.QueueSink)             { s.queue = q }
func (s *Service) SetTips(t cmod.TipService)             { s.tips = t }

type TopicRow struct {
	ID           string     `json:"id"`
	Number       int64      `json:"number"`
	Title        string     `json:"title"`
	Slug         string     `json:"slug"`
	Excerpt      string     `json:"excerpt"`
	Status       string     `json:"status"`
	CategoryID   string     `json:"category_id"`
	CategoryName string     `json:"category_name"`
	CategorySlug string     `json:"category_slug"`
	AuthorID     string     `json:"author_id"`
	AuthorName   string     `json:"author_name"`
	AuthorAvatar string     `json:"author_avatar"`
	ReplyCount   int64      `json:"reply_count"`
	ViewCount    int64      `json:"view_count"`
	IsPinned     bool       `json:"is_pinned"`
	IsFeatured   bool       `json:"is_featured"`
	IsLocked     bool       `json:"is_locked"`
	LastPostedAt *time.Time `json:"last_posted_at"`
	CreatedAt    time.Time  `json:"created_at"`
	DeletedAt    *time.Time `json:"-"`
	Unread       bool       `json:"unread"`
}

type PostRow struct {
	ID            string           `json:"id"`
	TopicID       string           `json:"topic_id"`
	TopicTitle    string           `json:"topic_title"`
	UserID        string           `json:"user_id"`
	AuthorName    string           `json:"author_name"`
	PostNumber    int              `json:"post_number"`
	ReplyToPostID string           `json:"reply_to_post_id,omitempty"`
	Raw           string           `json:"raw"`
	CookedHTML    string           `json:"cooked_html"`
	Status        string           `json:"status"`
	LikeCount     int64            `json:"like_count"`
	Reactions     map[string]int64 `json:"reactions,omitempty"`
	MyReactions   map[string]bool  `json:"my_reactions,omitempty"`
	Children      []PostRow        `json:"children,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	EditedAt      *time.Time       `json:"edited_at"`
	CanEdit       bool             `json:"can_edit"`
}

type TopicDetail struct {
	Topic            Topic
	Category         Category
	AuthorName       string
	Signatures       map[string]string
	Authors          map[string]contracts.UserSummary
	Tags             []Tag
	Posts            []PostRow
	Page             int
	PerPage          int
	TotalPosts       int64
	ReadState        TopicUser
	Poll             *PollView
	CanReply         bool
	HasNestedReplies bool
}

type PollOptionView struct {
	PollOption
	Percent int
}

type PollView struct {
	Poll
	Options []PollOptionView
	Total   int64
	Voted   bool
	My      map[string]bool
	Closed  bool
}

type TopicFilter struct {
	CategoryID         string
	TagSlug            string
	AuthorID           string
	Query              string
	Sort               string
	Page               int
	PerPage            int
	FollowedAuthors    []string
	FollowedCategories []string
	FollowedTags       []string
	ViewerID           string
}

type CreateTopicInput struct {
	CategoryID   string
	PollQuestion string
	PollOptions  []string
	PollMultiple bool
	UserID       string
	TrustLevel   int
	Title        string
	Raw          string
	TagIDs       []string
	IP           string
	UserAgent    string
}

type ReplyInput struct {
	TopicID       string
	UserID        string
	Raw           string
	ReplyToPostID string
	IP            string
	UserAgent     string
}

type EditPostInput struct {
	PostID      string
	UserID      string
	Raw         string
	IsModerator bool
}

type EditTopicInput struct {
	TopicID     string
	UserID      string
	Title       string
	Raw         string
	IsModerator bool
}

type AdminTopicFilter struct {
	Status     string
	CategoryID string
	Query      string
	Page       int
	PerPage    int
}

type AdminPostRow struct {
	ID         string    `json:"id"`
	TopicID    string    `json:"topic_id"`
	TopicTitle string    `json:"topic_title"`
	AuthorName string    `json:"author_name"`
	Excerpt    string    `json:"excerpt"`
	Status     string    `json:"status"`
	PostNumber int       `json:"post_number"`
	CreatedAt  time.Time `json:"created_at"`
	Deleted    bool      `json:"deleted"`
}

func (s *Service) EnsureDefaults(ctx context.Context) error {
	var n int64
	if err := s.db.WithContext(ctx).Model(&Category{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&Category{
		Name:        "综合讨论",
		Slug:        "general",
		Description: "社区默认板块，聊什么都行。",
		Color:       "#2563eb",
		Position:    1,
		Visibility:  "public",
	}).Error
}

func (s *Service) ListCategories(ctx context.Context) ([]Category, error) {
	s.catMu.RLock()
	if len(s.catCache) > 0 && time.Since(s.catCacheAt) < 30*time.Second {
		out := append([]Category(nil), s.catCache...)
		s.catMu.RUnlock()
		return out, nil
	}
	s.catMu.RUnlock()
	var rows []Category
	err := s.db.WithContext(ctx).Where("visibility = ?", "public").Order("position ASC, name ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	s.catMu.Lock()
	s.catCache = append([]Category(nil), rows...)
	s.catCacheAt = time.Now()
	s.catMu.Unlock()
	return rows, nil
}

func (s *Service) invalidateCategoryCache() {
	s.catMu.Lock()
	s.catCache = nil
	s.catCacheAt = time.Time{}
	s.catMu.Unlock()
	s.invalidateStats(context.Background())
}

func (s *Service) GetCategoryBySlug(ctx context.Context, slug string) (*Category, error) {
	var c Category
	err := s.db.WithContext(ctx).Where("slug = ? AND visibility = ?", slug, "public").First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, httpx.NotFound("category.not_found", "板块不存在")
	}
	return &c, err
}

func (s *Service) ListTags(ctx context.Context, limit int) ([]Tag, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []Tag
	err := s.db.WithContext(ctx).Order("topic_count DESC, name ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListTagsPage 分页返回标签及总数，后台标签管理使用。
func (s *Service) ListTagsPage(ctx context.Context, limit, offset int) ([]Tag, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	base := s.db.WithContext(ctx).Model(&Tag{})
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Tag
	err := base.Order("topic_count DESC, name ASC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *Service) GetTagBySlug(ctx context.Context, slug string) (*Tag, error) {
	var t Tag
	if err := s.db.WithContext(ctx).Where("slug = ?", slug).First(&t).Error; err != nil {
		return nil, httpx.NotFound("tag.not_found", "标签不存在")
	}
	return &t, nil
}

// ReindexSearch rebuilds the search index in batches. It walks ids with a
// cursor instead of loading every row: a large forum would otherwise pull all
// topics and posts (including raw bodies) into memory at once.
func (s *Service) ReindexSearch(ctx context.Context) (int, error) {
	if s.search == nil {
		return 0, nil
	}
	if r, ok := s.search.(interface{ Reset(context.Context) error }); ok {
		_ = r.Reset(ctx)
	}

	count := 0
	// --- topics, batched by id ---
	lastID := ""
	for {
		var batch []Topic
		q := s.db.WithContext(ctx).Where("deleted_at IS NULL").
			Where("status IN ?", []string{"open", "closed", "archived"}).
			Order("id ASC").Limit(500)
		if lastID != "" {
			q = q.Where("id > ?", lastID)
		}
		if err := q.Find(&batch).Error; err != nil {
			return count, err
		}
		if len(batch) == 0 {
			break
		}
		// One query supplies the first visible post of every topic in the batch.
		ids := make([]string, 0, len(batch))
		for i := range batch {
			ids = append(ids, batch[i].ID)
		}
		firstPosts := make(map[string]Post, len(batch))
		var posts []Post
		if err := s.db.WithContext(ctx).
			Where("topic_id IN ? AND status = ? AND post_number = 1", ids, "visible").
			Find(&posts).Error; err == nil {
			for _, p := range posts {
				firstPosts[p.TopicID] = p
			}
		}
		for i := range batch {
			if first, ok := firstPosts[batch[i].ID]; ok {
				s.indexTopic(ctx, &batch[i], &first)
			} else {
				s.indexTopic(ctx, &batch[i], nil)
			}
			count++
		}
		lastID = batch[len(batch)-1].ID
	}

	// --- posts, batched by id ---
	lastID = ""
	for {
		var batch []Post
		q := s.db.WithContext(ctx).Where("deleted_at IS NULL AND status = ?", "visible").
			Order("id ASC").Limit(500)
		if lastID != "" {
			q = q.Where("id > ?", lastID)
		}
		if err := q.Find(&batch).Error; err != nil {
			return count, err
		}
		if len(batch) == 0 {
			break
		}
		// Load the parent topics once per batch instead of once per post.
		topicIDs := make([]string, 0, len(batch))
		seen := make(map[string]bool, len(batch))
		for _, p := range batch {
			if !seen[p.TopicID] {
				seen[p.TopicID] = true
				topicIDs = append(topicIDs, p.TopicID)
			}
		}
		var topics []Topic
		if err := s.db.WithContext(ctx).Where("id IN ?", topicIDs).Find(&topics).Error; err != nil {
			return count, err
		}
		byID := make(map[string]Topic, len(topics))
		for _, t := range topics {
			if t.Status == "open" || t.Status == "closed" || t.Status == "archived" {
				byID[t.ID] = t
			}
		}
		for i := range batch {
			t, ok := byID[batch[i].TopicID]
			if !ok {
				continue
			}
			s.indexPost(ctx, &t, &batch[i])
			count++
		}
		lastID = batch[len(batch)-1].ID
	}
	return count, nil
}

func (s *Service) UserActivity(ctx context.Context, userID string) (topics, posts, likes int64, err error) {
	if err = s.db.WithContext(ctx).Model(&Topic{}).
		Where("user_id = ? AND deleted_at IS NULL", userID).Count(&topics).Error; err != nil {
		return
	}
	if err = s.db.WithContext(ctx).Model(&Post{}).
		Where("user_id = ? AND deleted_at IS NULL AND status = ?", userID, "visible").Count(&posts).Error; err != nil {
		return
	}
	err = s.db.WithContext(ctx).Table("reactions AS r").
		Joins("JOIN posts p ON p.id = r.target_id AND r.target_type = 'post'").
		Where("p.user_id = ? AND r.reaction = 'like'", userID).
		Count(&likes).Error
	return
}

// UserActivityBatch aggregates topic, post and like counts for a set of users
// with three grouped queries. Calling UserActivity per user would issue three
// statements each, which is what made the hourly trust-level job expensive.
func (s *Service) UserActivityBatch(ctx context.Context, userIDs []string) (map[string]contracts.ActivityCounts, error) {
	out := make(map[string]contracts.ActivityCounts, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	const chunk = 500
	for start := 0; start < len(userIDs); start += chunk {
		end := start + chunk
		if end > len(userIDs) {
			end = len(userIDs)
		}
		part := userIDs[start:end]

		var topicRows []struct {
			UserID string
			N      int64
		}
		if err := s.db.WithContext(ctx).Model(&Topic{}).
			Select("user_id, COUNT(*) AS n").
			Where("user_id IN ? AND deleted_at IS NULL", part).
			Group("user_id").Scan(&topicRows).Error; err != nil {
			return nil, err
		}
		for _, r := range topicRows {
			v := out[r.UserID]
			v.Topics = r.N
			out[r.UserID] = v
		}

		var postRows []struct {
			UserID string
			N      int64
		}
		if err := s.db.WithContext(ctx).Model(&Post{}).
			Select("user_id, COUNT(*) AS n").
			Where("user_id IN ? AND deleted_at IS NULL AND status = ?", part, "visible").
			Group("user_id").Scan(&postRows).Error; err != nil {
			return nil, err
		}
		for _, r := range postRows {
			v := out[r.UserID]
			v.Posts = r.N
			out[r.UserID] = v
		}

		var likeRows []struct {
			UserID string
			N      int64
		}
		if err := s.db.WithContext(ctx).Table("reactions AS r").
			Select("p.user_id AS user_id, COUNT(*) AS n").
			Joins("JOIN posts p ON p.id = r.target_id AND r.target_type = 'post'").
			Where("p.user_id IN ? AND r.reaction = 'like'", part).
			Group("p.user_id").Scan(&likeRows).Error; err != nil {
			return nil, err
		}
		for _, r := range likeRows {
			v := out[r.UserID]
			v.Likes = r.N
			out[r.UserID] = v
		}
	}
	return out, nil
}

func (s *Service) SearchTopics(ctx context.Context, q string, page, perPage int) ([]TopicRow, int64, error) {
	return s.SearchTopicsAdvanced(ctx, q, "", "", page, perPage)
}

func (s *Service) SearchTopicsAdvanced(ctx context.Context, q, typ, sort string, page, perPage int) ([]TopicRow, int64, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return s.ListTopics(ctx, TopicFilter{Page: page, PerPage: perPage})
	}
	if s.search == nil {
		return s.ListTopics(ctx, TopicFilter{Query: q, Page: page, PerPage: perPage})
	}
	// Pass paging through so page 6+ is not silently empty.
	if page < 1 {
		page = 1
	}
	if perPage <= 0 || perPage > 100 {
		perPage = 20
	}
	results, err := s.search.Search(ctx, searchx.Query{
		Text: q, Type: typ, Sort: sort, Page: page, PerPage: perPage,
	})
	if err != nil || len(results) == 0 {
		return s.ListTopics(ctx, TopicFilter{Query: q, Page: page, PerPage: perPage})
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(results))
	for _, r := range results {
		id := r.TopicID
		if id == "" {
			id = r.ID
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return s.ListTopics(ctx, TopicFilter{Query: q, Page: page, PerPage: perPage})
	}

	var topics []Topic
	if err := s.db.WithContext(ctx).Where("id IN ? AND deleted_at IS NULL", ids).
		Where("status IN ?", []string{"open", "closed", "archived"}).Find(&topics).Error; err != nil {
		return nil, 0, err
	}
	byID := make(map[string]Topic, len(topics))
	for _, t := range topics {
		byID[t.ID] = t
	}
	catIDs := make([]string, 0, len(topics))
	authorIDs := make([]string, 0, len(topics))
	for _, t := range topics {
		catIDs = append(catIDs, t.CategoryID)
		authorIDs = append(authorIDs, t.UserID)
	}
	var cats []Category
	_ = s.db.WithContext(ctx).Where("id IN ?", dedupe(catIDs)).Find(&cats).Error
	catByID := make(map[string]Category, len(cats))
	for _, c := range cats {
		catByID[c.ID] = c
	}
	authors, _ := s.authorInfoMap(ctx, authorIDs)

	rows := make([]TopicRow, 0, len(ids))
	for _, id := range ids {
		t, ok := byID[id]
		if !ok {
			continue
		}
		cat := catByID[t.CategoryID]
		if cat.Visibility != "" && cat.Visibility != "public" {
			continue
		}
		rows = append(rows, TopicRow{
			ID: t.ID, Title: t.Title, Slug: t.Slug, Excerpt: t.Excerpt, Status: t.Status,
			CategoryID: t.CategoryID, CategoryName: cat.Name, CategorySlug: cat.Slug,
			AuthorID: t.UserID, AuthorName: authors[t.UserID].Name, AuthorAvatar: authors[t.UserID].Avatar,
			ReplyCount: t.ReplyCount, ViewCount: t.ViewCount,
			IsPinned: t.IsPinned, IsFeatured: t.IsFeatured, IsLocked: t.IsLocked,
			LastPostedAt: t.LastPostedAt, CreatedAt: t.CreatedAt,
		})
	}
	// The engine already applied limit/offset, so this page is the result set.
	// Report page+1 availability by asking whether another row exists.
	total := int64((page-1)*perPage + len(rows))
	if len(rows) == perPage {
		// One extra page may exist; make total reflect at least one more page so
		// the pager renders "next" without a separate count query.
		total += 1
	}
	return rows, total, nil
}

func (s *Service) ListTopics(ctx context.Context, f TopicFilter) ([]TopicRow, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PerPage < 1 || f.PerPage > 100 {
		f.PerPage = 20
	}

	base := s.db.WithContext(ctx).Table("topics AS t").
		Joins("JOIN categories c ON c.id = t.category_id").
		Where("t.deleted_at IS NULL").
		Where("c.visibility = ?", "public").
		Where("t.status IN ?", []string{"open", "closed", "archived"})

	if f.CategoryID != "" {
		base = base.Where("t.category_id = ?", f.CategoryID)
	}
	if f.AuthorID != "" {
		base = base.Where("t.user_id = ?", f.AuthorID)
	}
	if f.TagSlug != "" {
		base = base.Where("EXISTS (SELECT 1 FROM topic_tags tt JOIN tags tg ON tg.id = tt.tag_id WHERE tt.topic_id = t.id AND tg.slug = ?)", f.TagSlug)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + q + "%"
		base = base.Where("(t.title LIKE ? OR t.excerpt LIKE ?)", like, like)
	}
	if len(f.FollowedAuthors)+len(f.FollowedCategories)+len(f.FollowedTags) > 0 {
		parts := make([]string, 0, 3)
		args := make([]any, 0, 3)
		if len(f.FollowedAuthors) > 0 {
			parts = append(parts, "t.user_id IN ?")
			args = append(args, f.FollowedAuthors)
		}
		if len(f.FollowedCategories) > 0 {
			parts = append(parts, "t.category_id IN ?")
			args = append(args, f.FollowedCategories)
		}
		if len(f.FollowedTags) > 0 {
			parts = append(parts, "EXISTS (SELECT 1 FROM topic_tags tt WHERE tt.topic_id = t.id AND tt.tag_id IN ?)")
			args = append(args, f.FollowedTags)
		}
		base = base.Where("("+strings.Join(parts, " OR ")+")", args...)
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	switch f.Sort {
	case "hot":
		base = base.Order("t.is_pinned DESC, t.reply_count DESC, t.bumped_at DESC")
	case "new":
		base = base.Order("t.is_pinned DESC, t.created_at DESC")
	default:
		base = base.Order("t.is_pinned DESC, t.bumped_at DESC")
	}

	var rows []TopicRow
	err := base.Select(`t.id, t.number, t.title, t.slug, t.excerpt, t.status, t.category_id,
			c.name AS category_name, c.slug AS category_slug,
			t.user_id AS author_id, t.reply_count, t.view_count,
			t.is_pinned, t.is_featured, t.is_locked, t.last_posted_at, t.created_at`).
		Offset((f.Page - 1) * f.PerPage).Limit(f.PerPage).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}

	authors, err := s.authorInfoMap(ctx, topicAuthorIDs(rows))
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		info := authors[rows[i].AuthorID]
		rows[i].AuthorName = info.Name
		rows[i].AuthorAvatar = info.Avatar
	}
	if f.ViewerID != "" && len(rows) > 0 {
		ids := make([]string, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].ID)
		}
		var states []TopicUser
		_ = s.db.WithContext(ctx).Where("user_id = ? AND topic_id IN ?", f.ViewerID, ids).Find(&states).Error
		read := make(map[string]int, len(states))
		for _, st := range states {
			read[st.TopicID] = st.LastReadPostNumber
		}
		for i := range rows {
			rows[i].Unread = int64(read[rows[i].ID]) < rows[i].ReplyCount+1
		}
	}
	return rows, total, nil
}

func (s *Service) GetTopicDetail(ctx context.Context, topicID string, page, perPage int, currentUserID string, isModerator bool) (*TopicDetail, error) {
	return s.getTopicDetail(ctx, topicID, page, perPage, currentUserID, isModerator, false, false)
}

// GetTopicDetailOnlyOP returns the topic with only the original poster's replies.
func (s *Service) GetTopicDetailOnlyOP(ctx context.Context, topicID string, page, perPage int, currentUserID string, isModerator bool) (*TopicDetail, error) {
	return s.getTopicDetail(ctx, topicID, page, perPage, currentUserID, isModerator, true, false)
}

// GetTopicDetailOrdered returns the topic with an optional newest-first reply order.
func (s *Service) GetTopicDetailOrdered(ctx context.Context, topicID string, page, perPage int, currentUserID string, isModerator, onlyOP, newestFirst bool) (*TopicDetail, error) {
	return s.getTopicDetail(ctx, topicID, page, perPage, currentUserID, isModerator, onlyOP, newestFirst)
}

func (s *Service) getTopicDetail(ctx context.Context, topicID string, page, perPage int, currentUserID string, isModerator, onlyOP, newestFirst bool) (*TopicDetail, error) {
	var topic Topic
	err := s.db.WithContext(ctx).Unscoped().First(&topic, "id = ?", topicID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, httpx.NotFound("topic.not_found", "主题不存在")
	}
	if err != nil {
		return nil, err
	}
	if topic.Status == "deleted" || topic.DeletedAt.Valid {
		return nil, httpx.NewError("topic.gone", http.StatusGone, "主题已删除")
	}
	if (topic.Status == "pending" || topic.Status == "hidden") && !isModerator && topic.UserID != currentUserID {
		return nil, httpx.NotFound("topic.not_found", "主题不存在")
	}

	var category Category
	if err := s.db.WithContext(ctx).First(&category, "id = ?", topic.CategoryID).Error; err != nil {
		return nil, err
	}

	var tags []Tag
	_ = s.db.WithContext(ctx).Table("tags AS tg").
		Joins("JOIN topic_tags tt ON tt.tag_id = tg.id").
		Where("tt.topic_id = ?", topic.ID).
		Order("tg.name ASC").Scan(&tags).Error

	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	var total int64
	postBase := s.db.WithContext(ctx).Model(&Post{}).Where("topic_id = ? AND deleted_at IS NULL", topic.ID)
	if !isModerator {
		postBase = postBase.Where("status = ?", "visible")
	}
	if onlyOP {
		postBase = postBase.Where("user_id = ?", topic.UserID)
	}
	if err := postBase.Count(&total).Error; err != nil {
		return nil, err
	}

	order := "post_number ASC"
	if newestFirst {
		order = "post_number DESC"
	}
	var posts []Post
	if err := postBase.Order(order).
		Offset((page - 1) * perPage).Limit(perPage).
		Find(&posts).Error; err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(posts)+1)
	ids = append(ids, topic.UserID)
	for i := range posts {
		ids = append(ids, posts[i].UserID)
	}
	// One lookup supplies names, avatars and signatures: previously this ran
	// GetSummaries twice for the same id set on every topic view.
	signatures := map[string]string{}
	authorsMeta := map[string]contracts.UserSummary{}
	authors := map[string]string{}
	if s.users != nil && len(ids) > 0 {
		if summaries, err := s.users.GetSummaries(ctx, dedupe(ids)); err == nil {
			authorsMeta = summaries
			for id, summary := range summaries {
				authors[id] = summary.Username
				if summary.Signature != "" {
					if s.md != nil {
						signatures[id] = s.md.RenderSignature(summary.Signature)
					} else {
						signatures[id] = summary.Signature
					}
				}
			}
		}
	}

	window := time.Duration(s.settingInt("community.post.edit_window_minutes", 30)) * time.Minute
	now := time.Now().UTC()
	rows := make([]PostRow, 0, len(posts))
	for i := range posts {
		p := &posts[i]
		canEdit := isModerator || (p.UserID == currentUserID && now.Sub(p.CreatedAt) <= window)
		rows = append(rows, PostRow{
			ID:            p.ID,
			UserID:        p.UserID,
			AuthorName:    authors[p.UserID],
			PostNumber:    p.PostNumber,
			ReplyToPostID: p.ReplyToPostID,
			Raw:           p.Raw,
			CookedHTML:    p.CookedHTML,
			Status:        p.Status,
			LikeCount:     p.LikeCount,
			CreatedAt:     p.CreatedAt,
			EditedAt:      p.EditedAt,
			CanEdit:       canEdit,
		})
	}

	if s.engagement != nil && len(rows) > 0 {
		ids := make([]string, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].ID)
		}
		if counts, mine, err := s.engagement.ReactionSummary(ctx, currentUserID, "post", ids); err == nil {
			for i := range rows {
				rows[i].Reactions = counts[rows[i].ID]
				rows[i].MyReactions = mine[rows[i].ID]
			}
		}
	}
	detailHasNested := false
	if len(rows) > 0 {
		byID := make(map[string]int, len(rows))
		for i := range rows {
			byID[rows[i].ID] = i
		}
		childIdx := make(map[string][]int, len(rows))
		hasNested := false
		nested := make(map[string]bool, len(rows))
		for i := range rows {
			if parent := rows[i].ReplyToPostID; parent != "" {
				if _, ok := byID[parent]; ok {
					childIdx[parent] = append(childIdx[parent], i)
					nested[rows[i].ID] = true
					hasNested = true
				}
			}
		}
		// Build after every child index is known so deeper replies are not
		// dropped by copying a parent before its own children are attached.
		seen := make(map[string]bool, len(rows))
		var build func(int) PostRow
		build = func(i int) PostRow {
			row := rows[i]
			if seen[row.ID] {
				return row
			}
			seen[row.ID] = true
			for _, ci := range childIdx[row.ID] {
				row.Children = append(row.Children, build(ci))
			}
			return row
		}
		top := make([]PostRow, 0, len(rows))
		for i := range rows {
			if !nested[rows[i].ID] {
				top = append(top, build(i))
			}
		}
		rows = top
		if hasNested {
			detailHasNested = true
		}
	}
	var readState TopicUser
	if currentUserID != "" {
		_ = s.db.WithContext(ctx).Where("topic_id = ? AND user_id = ?", topic.ID, currentUserID).First(&readState).Error
	}

	poll, _ := s.loadPoll(ctx, topic.ID, currentUserID)
	return &TopicDetail{
		Topic:            topic,
		Category:         category,
		AuthorName:       authors[topic.UserID],
		Poll:             poll,
		Signatures:       signatures,
		Authors:          authorsMeta,
		Tags:             tags,
		Posts:            rows,
		Page:             page,
		PerPage:          perPage,
		TotalPosts:       total,
		ReadState:        readState,
		CanReply:         topic.Status == "open" && !topic.IsLocked,
		HasNestedReplies: detailHasNested,
	}, nil
}

func (s *Service) CreateTopic(ctx context.Context, in CreateTopicInput) (*Topic, *Post, error) {
	defer s.invalidateCategoryCache()
	var category Category
	if err := s.db.WithContext(ctx).First(&category, "id = ?", in.CategoryID).Error; err != nil {
		return nil, nil, httpx.Invalid(map[string]string{"category_id": "板块不存在"})
	}
	if category.Visibility != "public" {
		return nil, nil, httpx.NotFound("category.not_found", "板块不存在")
	}

	title := strings.TrimSpace(in.Title)
	minTitle := int(s.settingInt("community.topic.title_min", 5))
	maxTitle := int(s.settingInt("community.topic.title_max", 120))
	if n := len([]rune(title)); n < minTitle || n > maxTitle {
		return nil, nil, httpx.Invalid(map[string]string{"title": "标题长度不符合要求"})
	}
	if strings.TrimSpace(in.Raw) == "" {
		return nil, nil, httpx.Invalid(map[string]string{"raw": "正文不能为空"})
	}
	if max := int(s.settingInt("community.post.max_length", 50000)); len([]rune(in.Raw)) > max {
		return nil, nil, httpx.Invalid(map[string]string{"raw": "正文超出长度限制"})
	}

	status := "open"
	if category.RequireApproval {
		status = "pending"
	} else if s.settingBool("moderation.new_user_review", true) && in.TrustLevel <= 0 {
		status = "pending"
	}
	if s.guard != nil {
		action, _, err := s.guard.Check(ctx, title+"\n"+in.Raw)
		if err != nil {
			return nil, nil, err
		}
		switch action {
		case "block":
			return nil, nil, httpx.Invalid(map[string]string{"raw": "内容包含敏感词，无法发布"})
		case "review":
			status = "pending"
		}
	}

	now := time.Now().UTC()
	topic := &Topic{
		ID:         id.New(),
		CategoryID: category.ID,
		UserID:     in.UserID,
		Title:      title,
		Slug:       markdown.Slug(title),
		Excerpt:    markdown.Excerpt(in.Raw, 160),
		Archetype:  "regular",
		Status:     status,
		PostCount:  1,
		BumpedAt:   now,
		Visibility: "public",
	}
	postID := id.New()
	topic.LastPostID = postID
	topic.LastPosterID = in.UserID
	topic.LastPostedAt = &now

	post := &Post{
		ID:         postID,
		TopicID:    topic.ID,
		UserID:     in.UserID,
		PostNumber: 1,
		Raw:        in.Raw,
		CookedHTML: s.render(in.Raw),
		Status:     "visible",
		Action:     "create",
		IP:         in.IP,
		UserAgent:  in.UserAgent,
	}
	if topic.Status == "pending" {
		post.Status = "visible" // author can still see their pending topic
	}

	var tags []Tag
	if len(in.TagIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("id IN ?", in.TagIDs).Find(&tags).Error; err != nil {
			return nil, nil, err
		}
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var seq TopicNumber
		if err := tx.Create(&seq).Error; err != nil {
			return err
		}
		topic.Number = int64(seq.ID)
		if err := tx.Create(topic).Error; err != nil {
			return err
		}
		if err := tx.Create(post).Error; err != nil {
			return err
		}
		if strings.TrimSpace(in.PollQuestion) != "" && len(in.PollOptions) >= 2 {
			poll := Poll{TopicID: topic.ID, Question: strings.TrimSpace(in.PollQuestion), Multiple: in.PollMultiple}
			if err := tx.Create(&poll).Error; err != nil {
				return err
			}
			for i, text := range in.PollOptions {
				text = strings.TrimSpace(text)
				if text == "" {
					continue
				}
				if err := tx.Create(&PollOption{PollID: poll.ID, Text: text, Sort: i}).Error; err != nil {
					return err
				}
			}
		}
		for _, tag := range tags {
			if err := tx.Create(&TopicTag{TopicID: topic.ID, TagID: tag.ID}).Error; err != nil {
				return err
			}
			if err := tx.Model(&Tag{}).Where("id = ?", tag.ID).
				UpdateColumn("topic_count", gorm.Expr("topic_count + 1")).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&TopicUser{
			TopicID:            topic.ID,
			UserID:             in.UserID,
			LastReadPostNumber: 1,
			HighestPostNumber:  1,
			NotificationLevel:  "watching",
			LastVisitedAt:      now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&Category{}).Where("id = ?", category.ID).Updates(map[string]any{
			"topic_count": gorm.Expr("topic_count + 1"),
			"post_count":  gorm.Expr("post_count + 1"),
		}).Error
	})
	if err != nil {
		return nil, nil, err
	}

	s.publishPost(ctx, topic, post, "")
	if topic.Status == "pending" {
		if s.queue != nil {
			_ = s.queue.Enqueue(ctx, "topic", topic.ID, "auto")
		}
	} else {
		s.indexTopic(ctx, topic, post)
	}
	return topic, post, nil
}

var errRetry = errors.New("retry post number")

func (s *Service) Reply(ctx context.Context, in ReplyInput) (*Post, error) {
	defer s.invalidateCategoryCache()
	if strings.TrimSpace(in.Raw) == "" {
		return nil, httpx.Invalid(map[string]string{"raw": "回复不能为空"})
	}
	if max := int(s.settingInt("community.post.max_length", 50000)); len([]rune(in.Raw)) > max {
		return nil, httpx.Invalid(map[string]string{"raw": "回复超出长度限制"})
	}

	postID := id.New()
	cooked := s.render(in.Raw)
	review := false
	if s.guard != nil {
		action, _, err := s.guard.Check(ctx, in.Raw)
		if err != nil {
			return nil, err
		}
		switch action {
		case "block":
			return nil, httpx.Invalid(map[string]string{"raw": "内容包含敏感词，无法发布"})
		case "review":
			review = true
		}
	}
	var created *Post

	for attempt := 0; attempt < 3; attempt++ {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var topic Topic
			if err := tx.First(&topic, "id = ?", in.TopicID).Error; err != nil {
				return httpx.NotFound("topic.not_found", "主题不存在")
			}
			if topic.Status != "open" || topic.IsLocked {
				return httpx.Forbidden("topic.closed", "主题已关闭，无法回复")
			}
			next := topic.PostCount + 1
			now := time.Now().UTC()
			res := tx.Model(&Topic{}).
				Where("id = ? AND post_count = ?", topic.ID, topic.PostCount).
				Updates(map[string]any{
					"post_count":     next,
					"reply_count":    next - 1,
					"last_post_id":   postID,
					"last_poster_id": in.UserID,
					"last_posted_at": now,
					"bumped_at":      now,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return errRetry
			}
			post := &Post{
				ID:            postID,
				TopicID:       topic.ID,
				UserID:        in.UserID,
				PostNumber:    int(next),
				ReplyToPostID: in.ReplyToPostID,
				Raw:           in.Raw,
				CookedHTML:    cooked,
				Status:        "visible",
				Action:        "create",
				IP:            in.IP,
				UserAgent:     in.UserAgent,
			}
			if review {
				post.Status = "hidden"
			}
			if err := tx.Create(post).Error; err != nil {
				return err
			}
			if err := tx.Model(&Category{}).Where("id = ?", topic.CategoryID).
				UpdateColumn("post_count", gorm.Expr("post_count + 1")).Error; err != nil {
				return err
			}
			tu := TopicUser{
				TopicID:            topic.ID,
				UserID:             in.UserID,
				LastReadPostNumber: int(next),
				HighestPostNumber:  int(next),
				NotificationLevel:  "watching",
				LastVisitedAt:      now,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "topic_id"}, {Name: "user_id"}},
				DoUpdates: clause.Assignments(map[string]any{
					"last_read_post_number": gorm.Expr("CASE WHEN topic_users.last_read_post_number < ? THEN ? ELSE topic_users.last_read_post_number END", int(next), int(next)),
					"highest_post_number":   gorm.Expr("CASE WHEN topic_users.highest_post_number < ? THEN ? ELSE topic_users.highest_post_number END", int(next), int(next)),
					"last_visited_at":       now,
				}),
			}).Create(&tu).Error; err != nil {
				return err
			}
			created = post
			return nil
		})
		if err == nil {
			break
		}
		if errors.Is(err, errRetry) {
			continue
		}
		return nil, err
	}
	if created == nil {
		return nil, httpx.NewError("topic.busy", 409, "主题繁忙，请稍后重试")
	}

	var topic Topic
	_ = s.db.WithContext(ctx).First(&topic, "id = ?", in.TopicID).Error
	if created.Status == "visible" {
		s.publishPost(ctx, &topic, created, in.ReplyToPostID)
		s.indexPost(ctx, &topic, created)
	} else if s.queue != nil {
		_ = s.queue.Enqueue(ctx, "post", created.ID, "auto")
	}
	return created, nil
}

// EditTopic updates the topic title and its first post while keeping a
// revision of the previous body.
func (s *Service) EditTopic(ctx context.Context, in EditTopicInput) (*Topic, *Post, error) {
	var topic Topic
	if err := s.db.WithContext(ctx).First(&topic, "id = ?", in.TopicID).Error; err != nil {
		return nil, nil, httpx.NotFound("topic.not_found", "主题不存在")
	}
	if topic.Status == "deleted" || topic.DeletedAt.Valid {
		return nil, nil, httpx.NotFound("topic.not_found", "主题不存在")
	}
	if !in.IsModerator {
		if topic.UserID != in.UserID {
			return nil, nil, httpx.Forbidden("permission.denied", "没有权限编辑该主题")
		}
		window := time.Duration(s.settingInt("community.post.edit_window_minutes", 30)) * time.Minute
		if time.Since(topic.CreatedAt) > window {
			return nil, nil, httpx.Forbidden("topic.edit_expired", "已超过编辑时限")
		}
	}
	title := strings.TrimSpace(in.Title)
	minTitle := int(s.settingInt("community.topic.title_min", 5))
	maxTitle := int(s.settingInt("community.topic.title_max", 120))
	if n := len([]rune(title)); n < minTitle || n > maxTitle {
		return nil, nil, httpx.Invalid(map[string]string{"title": "标题长度不符合要求"})
	}
	if strings.TrimSpace(in.Raw) == "" {
		return nil, nil, httpx.Invalid(map[string]string{"raw": "正文不能为空"})
	}
	if max := int(s.settingInt("community.post.max_length", 50000)); len([]rune(in.Raw)) > max {
		return nil, nil, httpx.Invalid(map[string]string{"raw": "正文超出长度限制"})
	}

	var post Post
	if err := s.db.WithContext(ctx).Where("topic_id = ? AND post_number = 1", topic.ID).First(&post).Error; err != nil {
		return nil, nil, httpx.NotFound("post.not_found", "主题正文不存在")
	}
	cooked := s.render(in.Raw)
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&PostRevision{PostID: post.ID, UserID: in.UserID, Raw: post.Raw}).Error; err != nil {
			return err
		}
		if err := tx.Model(&Post{}).Where("id = ?", post.ID).Updates(map[string]any{
			"raw":         in.Raw,
			"cooked_html": cooked,
			"edited_at":   now,
			"edit_count":  gorm.Expr("edit_count + 1"),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&Topic{}).Where("id = ?", topic.ID).Updates(map[string]any{
			"title": title, "excerpt": markdown.Excerpt(in.Raw, 160), "updated_at": now,
		}).Error
	})
	if err != nil {
		return nil, nil, err
	}
	topic.Title = title
	topic.Excerpt = markdown.Excerpt(in.Raw, 160)
	topic.UpdatedAt = now
	post.Raw = in.Raw
	post.CookedHTML = cooked
	post.EditedAt = &now
	post.EditCount++
	s.indexTopic(ctx, &topic, &post)
	return &topic, &post, nil
}

func (s *Service) EditPost(ctx context.Context, in EditPostInput) (*Post, error) {
	var post Post
	if err := s.db.WithContext(ctx).First(&post, "id = ?", in.PostID).Error; err != nil {
		return nil, httpx.NotFound("post.not_found", "回复不存在")
	}
	if post.Status == "deleted" {
		return nil, httpx.NotFound("post.not_found", "回复不存在")
	}
	if !in.IsModerator {
		if post.UserID != in.UserID {
			return nil, httpx.Forbidden("permission.denied", "没有权限编辑该回复")
		}
		window := time.Duration(s.settingInt("community.post.edit_window_minutes", 30)) * time.Minute
		if time.Since(post.CreatedAt) > window {
			return nil, httpx.Forbidden("post.edit_expired", "已超过编辑时限")
		}
	}
	if strings.TrimSpace(in.Raw) == "" {
		return nil, httpx.Invalid(map[string]string{"raw": "内容不能为空"})
	}

	cooked := s.render(in.Raw)
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&PostRevision{PostID: post.ID, UserID: in.UserID, Raw: post.Raw}).Error; err != nil {
			return err
		}
		return tx.Model(&Post{}).Where("id = ?", post.ID).Updates(map[string]any{
			"raw":         in.Raw,
			"cooked_html": cooked,
			"edited_at":   now,
			"edit_count":  gorm.Expr("edit_count + 1"),
		}).Error
	})
	if err != nil {
		return nil, err
	}
	post.Raw = in.Raw
	post.CookedHTML = cooked
	post.EditedAt = &now
	post.EditCount++
	var topic Topic
	if err := s.db.WithContext(ctx).First(&topic, "id = ?", post.TopicID).Error; err == nil {
		s.indexPost(ctx, &topic, &post)
	}
	return &post, nil
}

func (s *Service) GetPost(ctx context.Context, postID string) (*Post, error) {
	var post Post
	err := s.db.WithContext(ctx).First(&post, "id = ?", postID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, httpx.NotFound("post.not_found", "回复不存在")
	}
	if err != nil {
		return nil, err
	}
	if post.Status == "deleted" {
		return nil, httpx.NotFound("post.not_found", "回复不存在")
	}
	return &post, nil
}

func (s *Service) MarkRead(ctx context.Context, topicID, userID string, postNumber int) error {
	if userID == "" || topicID == "" || postNumber <= 0 {
		return nil
	}
	now := time.Now().UTC()
	tu := TopicUser{
		TopicID:            topicID,
		UserID:             userID,
		LastReadPostNumber: postNumber,
		HighestPostNumber:  postNumber,
		NotificationLevel:  "normal",
		LastVisitedAt:      now,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "topic_id"}, {Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"last_read_post_number": gorm.Expr("CASE WHEN topic_users.last_read_post_number < ? THEN ? ELSE topic_users.last_read_post_number END", postNumber, postNumber),
			"highest_post_number":   gorm.Expr("CASE WHEN topic_users.highest_post_number < ? THEN ? ELSE topic_users.highest_post_number END", postNumber, postNumber),
			"last_visited_at":       now,
		}),
	}).Create(&tu).Error
}

func (s *Service) IncrementView(ctx context.Context, topicID string) {
	_ = s.db.WithContext(ctx).Model(&Topic{}).Where("id = ?", topicID).
		UpdateColumn("view_count", gorm.Expr("view_count + 1")).Error
}

func (s *Service) AdminListTopics(ctx context.Context, f AdminTopicFilter) ([]TopicRow, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PerPage < 1 || f.PerPage > 100 {
		f.PerPage = 20
	}
	base := s.db.WithContext(ctx).Table("topics AS t").
		Joins("JOIN categories c ON c.id = t.category_id")
	if f.Status == "deleted" {
		base = base.Where("t.deleted_at IS NOT NULL")
	} else {
		base = base.Where("t.deleted_at IS NULL")
		if f.Status != "" {
			base = base.Where("t.status = ?", f.Status)
		}
	}
	if f.CategoryID != "" {
		base = base.Where("t.category_id = ?", f.CategoryID)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		base = base.Where("t.title LIKE ?", "%"+q+"%")
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []TopicRow
	err := base.Select(`t.id, t.number, t.title, t.slug, t.excerpt, t.status, t.category_id,
			c.name AS category_name, c.slug AS category_slug,
			t.user_id AS author_id, t.reply_count, t.view_count,
			t.is_pinned, t.is_featured, t.is_locked, t.last_posted_at, t.created_at, t.deleted_at`).
		Order("t.created_at DESC").
		Offset((f.Page - 1) * f.PerPage).Limit(f.PerPage).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	authors, err := s.authorInfoMap(ctx, topicAuthorIDs(rows))
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		info := authors[rows[i].AuthorID]
		rows[i].AuthorName = info.Name
		rows[i].AuthorAvatar = info.Avatar
	}
	return rows, total, nil
}

func (s *Service) AdminListPosts(ctx context.Context, status string, page, perPage int) ([]AdminPostRow, int64, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	base := s.db.WithContext(ctx).Table("posts AS p").
		Joins("JOIN topics t ON t.id = p.topic_id")
	if status == "deleted" {
		base = base.Where("p.deleted_at IS NOT NULL")
	} else {
		base = base.Where("p.deleted_at IS NULL")
		if status != "" {
			base = base.Where("p.status = ?", status)
		}
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var raw []struct {
		ID         string
		TopicID    string
		TopicTitle string
		UserID     string
		Excerpt    string
		Status     string
		PostNumber int
		CreatedAt  time.Time
		DeletedAt  *time.Time
	}
	err := base.Select("p.id, p.topic_id, t.title AS topic_title, p.user_id, p.raw AS excerpt, p.status, p.post_number, p.created_at, p.deleted_at").
		Order("p.created_at DESC").
		Offset((page - 1) * perPage).Limit(perPage).
		Scan(&raw).Error
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(raw))
	for _, r := range raw {
		ids = append(ids, r.UserID)
	}
	authors, err := s.authorMap(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]AdminPostRow, 0, len(raw))
	for _, r := range raw {
		rows = append(rows, AdminPostRow{
			ID:         r.ID,
			TopicID:    r.TopicID,
			TopicTitle: r.TopicTitle,
			AuthorName: authors[r.UserID],
			Excerpt:    markdown.Excerpt(r.Excerpt, 120),
			Status:     r.Status,
			PostNumber: r.PostNumber,
			CreatedAt:  r.CreatedAt,
			Deleted:    r.DeletedAt != nil,
		})
	}
	return rows, total, nil
}

// AcceptAnswer marks a reply as the accepted answer.
func (s *Service) AcceptAnswer(ctx context.Context, topicID, postID, userID string, isModerator bool) error {
	var topic Topic
	if err := s.db.WithContext(ctx).First(&topic, "id = ?", topicID).Error; err != nil {
		return httpx.NotFound("topic.not_found", "主题不存在")
	}
	if !isModerator && topic.UserID != userID {
		return httpx.Forbidden("permission.denied", "只有楼主或版主可以采纳答案")
	}
	var post Post
	if err := s.db.WithContext(ctx).First(&post, "id = ? AND topic_id = ?", postID, topicID).Error; err != nil {
		return httpx.NotFound("post.not_found", "回复不存在")
	}
	return s.db.WithContext(ctx).Model(&Topic{}).Where("id = ?", topicID).
		Updates(map[string]any{"accepted_post_id": post.ID, "is_solved": true}).Error
}

func (s *Service) SetTopicFlags(ctx context.Context, topicID string, updates map[string]any) error {
	allowed := map[string]bool{
		"is_pinned": true, "is_featured": true, "is_locked": true, "is_solved": true,
		"status": true,
	}
	clean := make(map[string]any, len(updates))
	for k, v := range updates {
		if allowed[k] {
			clean[k] = v
		}
	}
	if len(clean) == 0 {
		return httpx.Invalid(map[string]string{"action": "没有可更新的字段"})
	}
	return s.db.WithContext(ctx).Model(&Topic{}).Where("id = ?", topicID).Updates(clean).Error
}

func (s *Service) loadPoll(ctx context.Context, topicID, userID string) (*PollView, error) {
	var poll Poll
	if err := s.db.WithContext(ctx).Where("topic_id = ?", topicID).First(&poll).Error; err != nil {
		return nil, err
	}
	var opts []PollOption
	if err := s.db.WithContext(ctx).Where("poll_id = ?", poll.ID).Order("sort ASC").Find(&opts).Error; err != nil {
		return nil, err
	}
	view := &PollView{Poll: poll, My: map[string]bool{}}
	var total int64
	for _, opt := range opts {
		total += opt.Votes
	}
	for _, opt := range opts {
		pct := 0
		if total > 0 {
			pct = int(opt.Votes * 100 / total)
		}
		view.Options = append(view.Options, PollOptionView{PollOption: opt, Percent: pct})
	}
	view.Total = total
	if userID != "" {
		var votes []PollVote
		_ = s.db.WithContext(ctx).Where("poll_id = ? AND user_id = ?", poll.ID, userID).Find(&votes).Error
		for _, v := range votes {
			view.My[v.OptionID] = true
		}
		view.Voted = len(votes) > 0
	}
	view.Closed = poll.ClosesAt != nil && time.Now().UTC().After(*poll.ClosesAt)
	return view, nil
}

// TipTopic transfers points to the topic author.
func (s *Service) TipTopic(ctx context.Context, fromUserID, topicID string, points int64) error {
	if s.tips == nil {
		return httpx.Invalid(map[string]string{"tip": "打赏功能未启用"})
	}
	meta, err := s.TopicMeta(ctx, topicID)
	if err != nil {
		return err
	}
	return s.tips.Tip(ctx, fromUserID, meta.AuthorID, topicID, points)
}

// TipPost transfers points to the author of a specific post.
func (s *Service) TipPost(ctx context.Context, fromUserID, postID string, points int64) error {
	if s.tips == nil {
		return httpx.Invalid(map[string]string{"tip": "打赏功能未启用"})
	}
	meta, err := s.PostMeta(ctx, postID)
	if err != nil {
		return err
	}
	return s.tips.Tip(ctx, fromUserID, meta.AuthorID, meta.TopicID, points)
}

// VotePoll records the caller's poll choices, replacing any previous vote.
func (s *Service) VotePoll(ctx context.Context, topicID, userID string, optionIDs []string) error {
	var poll Poll
	if err := s.db.WithContext(ctx).Where("topic_id = ?", topicID).First(&poll).Error; err != nil {
		return httpx.NotFound("poll.not_found", "投票不存在")
	}
	if poll.ClosesAt != nil && time.Now().UTC().After(*poll.ClosesAt) {
		return httpx.Invalid(map[string]string{"poll": "投票已结束"})
	}
	if len(optionIDs) == 0 {
		return httpx.Invalid(map[string]string{"option": "请选择选项"})
	}
	if !poll.Multiple && len(optionIDs) > 1 {
		return httpx.Invalid(map[string]string{"option": "该投票为单选"})
	}
	var valid []PollOption
	if err := s.db.WithContext(ctx).Where("poll_id = ? AND id IN ?", poll.ID, optionIDs).Find(&valid).Error; err != nil {
		return err
	}
	if len(valid) != len(optionIDs) {
		return httpx.Invalid(map[string]string{"option": "选项无效"})
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("poll_id = ? AND user_id = ?", poll.ID, userID).Delete(&PollVote{}).Error; err != nil {
			return err
		}
		for _, opt := range valid {
			if err := tx.Create(&PollVote{PollID: poll.ID, OptionID: opt.ID, UserID: userID}).Error; err != nil {
				return err
			}
		}
		for _, opt := range valid {
			var count int64
			if err := tx.Model(&PollVote{}).Where("option_id = ?", opt.ID).Count(&count).Error; err != nil {
				return err
			}
			if err := tx.Model(&PollOption{}).Where("id = ?", opt.ID).Update("votes", count).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// AdminUpdateTopic edits the topic title, category and status from the admin panel.

// EnsureNumbers backfills short topic numbers for existing rows.
func (s *Service) EnsureNumbers(ctx context.Context) error {
	var topics []Topic
	if err := s.db.WithContext(ctx).Where("number = 0 OR number IS NULL").
		Order("created_at ASC").Find(&topics).Error; err != nil {
		return err
	}
	for i := range topics {
		var seq TopicNumber
		if err := s.db.WithContext(ctx).Create(&seq).Error; err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Model(&Topic{}).Where("id = ?", topics[i].ID).
			Update("number", seq.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// ResolveTopicIdent accepts a short number, a legacy ULID, or ULID-slug.
func (s *Service) ResolveTopicIdent(ctx context.Context, ident string) (string, error) {
	ident = strings.TrimSpace(ident)
	if n, err := strconv.ParseInt(ident, 10, 64); err == nil && n > 0 {
		var topic Topic
		if err := s.db.WithContext(ctx).Select("id").Where("number = ?", n).First(&topic).Error; err != nil {
			return "", httpx.NotFound("topic.not_found", "主题不存在")
		}
		return topic.ID, nil
	}
	if i := strings.IndexByte(ident, '-'); i > 0 {
		return ident[:i], nil
	}
	return ident, nil
}

func (s *Service) AdminUpdateTopic(ctx context.Context, topicID, title, categoryID, status string) error {
	var topic Topic
	if err := s.db.WithContext(ctx).First(&topic, "id = ?", topicID).Error; err != nil {
		return httpx.NotFound("topic.not_found", "主题不存在")
	}
	title = strings.TrimSpace(title)
	if n := len([]rune(title)); n < 2 || n > 200 {
		return httpx.Invalid(map[string]string{"title": "标题长度需在 2–200 个字符之间"})
	}
	if err := s.db.WithContext(ctx).Model(&Topic{}).Where("id = ?", topic.ID).
		Update("title", title).Error; err != nil {
		return err
	}
	if categoryID != "" && categoryID != topic.CategoryID {
		if err := s.MoveTopic(ctx, topic.ID, categoryID); err != nil {
			return err
		}
	}
	if status != "" && status != topic.Status {
		switch status {
		case "open", "closed", "archived", "hidden", "pending":
		default:
			return httpx.Invalid(map[string]string{"status": "状态无效"})
		}
		if err := s.SetTopicFlags(ctx, topic.ID, map[string]any{"status": status}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) DeleteTopic(ctx context.Context, topicID string) error {
	defer s.invalidateCategoryCache()
	var postIDs []string
	_ = s.db.WithContext(ctx).Model(&Post{}).Where("topic_id = ?", topicID).Pluck("id", &postIDs).Error
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&Topic{}).Where("id = ?", topicID).Updates(map[string]any{
		"status": "deleted", "deleted_at": now,
	}).Error; err != nil {
		return err
	}
	s.deleteIndex(ctx, "topic", topicID)
	for _, id := range postIDs {
		s.deleteIndex(ctx, "post", id)
	}
	return nil
}

func (s *Service) RestoreTopic(ctx context.Context, topicID string) error {
	defer s.invalidateCategoryCache()
	if err := s.db.WithContext(ctx).Unscoped().Model(&Topic{}).Where("id = ?", topicID).Updates(map[string]any{
		"status": "open", "deleted_at": nil,
	}).Error; err != nil {
		return err
	}
	var topic Topic
	if err := s.db.WithContext(ctx).First(&topic, "id = ?", topicID).Error; err != nil {
		return nil
	}
	var posts []Post
	_ = s.db.WithContext(ctx).Where("topic_id = ?", topicID).Order("post_number ASC").Find(&posts).Error
	for i := range posts {
		if i == 0 {
			s.indexTopic(ctx, &topic, &posts[i])
		}
		s.indexPost(ctx, &topic, &posts[i])
	}
	return nil
}

func (s *Service) HideTopic(ctx context.Context, topicID string) error {
	return s.SetTopicFlags(ctx, topicID, map[string]any{"status": "hidden"})
}

func (s *Service) HidePost(ctx context.Context, postID string) error {
	return s.SetPostStatus(ctx, postID, "hidden")
}

func (s *Service) LockTopic(ctx context.Context, topicID string, locked bool) error {
	return s.SetTopicFlags(ctx, topicID, map[string]any{"is_locked": locked})
}

func (s *Service) MoveTopic(ctx context.Context, topicID, categoryID string) error {
	var t Topic
	if err := s.db.WithContext(ctx).First(&t, "id = ?", topicID).Error; err != nil {
		return httpx.NotFound("topic.not_found", "主题不存在")
	}
	if t.CategoryID == categoryID {
		return nil
	}
	var target Category
	if err := s.db.WithContext(ctx).First(&target, "id = ?", categoryID).Error; err != nil {
		return httpx.Invalid(map[string]string{"category_id": "目标板块不存在"})
	}
	if target.Visibility != "public" {
		return httpx.Invalid(map[string]string{"category_id": "目标板块不可用"})
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Topic{}).Where("id = ? AND category_id = ?", t.ID, t.CategoryID).
			Update("category_id", categoryID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		if err := tx.Model(&Category{}).Where("id = ?", t.CategoryID).Updates(map[string]any{
			"topic_count": gorm.Expr("CASE WHEN topic_count > 0 THEN topic_count - 1 ELSE 0 END"),
			"post_count":  gorm.Expr("CASE WHEN post_count >= ? THEN post_count - ? ELSE 0 END", t.PostCount, t.PostCount),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&Category{}).Where("id = ?", categoryID).Updates(map[string]any{
			"topic_count": gorm.Expr("topic_count + 1"),
			"post_count":  gorm.Expr("post_count + ?", t.PostCount),
		}).Error
	})
}

func (s *Service) SetPostStatus(ctx context.Context, postID, status string) error {
	if status != "visible" && status != "hidden" && status != "deleted" {
		return httpx.Invalid(map[string]string{"status": "无效的回复状态"})
	}
	var post Post
	if err := s.db.WithContext(ctx).Unscoped().First(&post, "id = ?", postID).Error; err != nil {
		return httpx.NotFound("post.not_found", "回复不存在")
	}
	// A reply occupies a slot while its status is visible; toggling away from
	// visible must release it, toggling back must reclaim it. Without this the
	// topic's reply counter drifts and pagination shows the wrong page count.
	wasVisible := post.Status == "visible"
	willBeVisible := status == "visible"
	updates := map[string]any{"status": status}
	if status == "deleted" {
		updates["deleted_at"] = time.Now().UTC()
	} else {
		updates["deleted_at"] = nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Unscoped().Model(&Post{}).Where("id = ?", postID).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		if wasVisible && !willBeVisible {
			s.adjustTopicCounters(tx, post.TopicID, -1)
		} else if !wasVisible && willBeVisible {
			s.adjustTopicCounters(tx, post.TopicID, 1)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if status == "visible" {
		s.reindexPostByID(ctx, postID)
	} else {
		s.deleteIndex(ctx, "post", postID)
	}
	return nil
}

// adjustTopicCounters shifts reply/post counters by delta inside a transaction.
func (s *Service) adjustTopicCounters(tx *gorm.DB, topicID string, delta int64) {
	if topicID == "" || delta == 0 {
		return
	}
	expr := gorm.Expr("CASE WHEN reply_count + ? < 0 THEN 0 ELSE reply_count + ? END", delta, delta)
	postExpr := gorm.Expr("CASE WHEN post_count + ? < 0 THEN 0 ELSE post_count + ? END", delta, delta)
	_ = tx.Model(&Topic{}).Where("id = ?", topicID).
		Updates(map[string]any{"reply_count": expr, "post_count": postExpr}).Error
	var catID string
	if err := tx.Model(&Topic{}).Where("id = ?", topicID).Pluck("category_id", &catID).Error; err == nil && catID != "" {
		catExpr := gorm.Expr("CASE WHEN post_count + ? < 0 THEN 0 ELSE post_count + ? END", delta, delta)
		_ = tx.Model(&Category{}).Where("id = ?", catID).Update("post_count", catExpr).Error
	}
}

func (s *Service) DeletePost(ctx context.Context, postID string) error {
	return s.SetPostStatus(ctx, postID, "deleted")
}

func (s *Service) RestorePost(ctx context.Context, postID string) error {
	return s.SetPostStatus(ctx, postID, "visible")
}

func (s *Service) restorePostLegacy(ctx context.Context, postID string) error {
	if err := s.db.WithContext(ctx).Unscoped().Model(&Post{}).Where("id = ?", postID).Updates(map[string]any{
		"status": "visible", "deleted_at": nil,
	}).Error; err != nil {
		return err
	}
	s.reindexPostByID(ctx, postID)
	return nil
}

func (s *Service) reindexPostByID(ctx context.Context, postID string) {
	var p Post
	if err := s.db.WithContext(ctx).First(&p, "id = ?", postID).Error; err != nil {
		return
	}
	var t Topic
	if err := s.db.WithContext(ctx).First(&t, "id = ?", p.TopicID).Error; err != nil {
		return
	}
	s.indexPost(ctx, &t, &p)
}

func (s *Service) CreateCategory(ctx context.Context, c *Category) error {
	if c.Name == "" {
		return httpx.Invalid(map[string]string{"name": "名称不能为空"})
	}
	if c.Slug == "" {
		c.Slug = markdown.Slug(c.Name)
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&Category{}).Where("slug = ?", c.Slug).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return httpx.Invalid(map[string]string{"slug": "板块 slug 已存在"})
	}
	if err := s.db.WithContext(ctx).Create(c).Error; err != nil {
		return err
	}
	s.invalidateCategoryCache()
	return nil
}

func (s *Service) UpdateCategory(ctx context.Context, c *Category) error {
	err := s.db.WithContext(ctx).Model(&Category{}).Where("id = ?", c.ID).Updates(map[string]any{
		"name": c.Name, "description": c.Description, "color": c.Color,
		"position": c.Position, "visibility": c.Visibility, "require_approval": c.RequireApproval,
	}).Error
	if err == nil {
		s.invalidateCategoryCache()
	}
	return err
}

func (s *Service) DeleteCategory(ctx context.Context, id string) error {
	var n int64
	if err := s.db.WithContext(ctx).Model(&Topic{}).Where("category_id = ? AND deleted_at IS NULL", id).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return httpx.Invalid(map[string]string{"category": "板块下仍有主题，无法删除"})
	}
	if err := s.db.WithContext(ctx).Delete(&Category{}, "id = ?", id).Error; err != nil {
		return err
	}
	s.invalidateCategoryCache()
	return nil
}

func (s *Service) CreateTag(ctx context.Context, t *Tag) error {
	if t.Name == "" {
		return httpx.Invalid(map[string]string{"name": "名称不能为空"})
	}
	if t.Slug == "" {
		t.Slug = markdown.Slug(t.Name)
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&Tag{}).Where("slug = ?", t.Slug).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return httpx.Invalid(map[string]string{"slug": "标签 slug 已存在"})
	}
	return s.db.WithContext(ctx).Create(t).Error
}

func (s *Service) DeleteTag(ctx context.Context, id string) error {
	var n int64
	if err := s.db.WithContext(ctx).Model(&TopicTag{}).Where("tag_id = ?", id).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return httpx.Invalid(map[string]string{"tag": "标签仍被主题使用，无法删除"})
	}
	return s.db.WithContext(ctx).Delete(&Tag{}, "id = ?", id).Error
}

func (s *Service) ListUserTopics(ctx context.Context, userID string, page, perPage int) ([]TopicRow, int64, error) {
	return s.ListTopics(ctx, TopicFilter{AuthorID: userID, Page: page, PerPage: perPage})
}

type ActivityDay struct {
	Date  time.Time
	Count int
	Level int
}

// UserDailyActivity returns a compact contribution-style activity series.
func (s *Service) UserDailyActivity(ctx context.Context, userID string, days int) ([]ActivityDay, error) {
	if days < 7 || days > 365 {
		days = 84
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -(days - 1))
	var topicTimes, postTimes []time.Time
	if err := s.db.WithContext(ctx).Model(&Topic{}).
		Where("user_id = ? AND deleted_at IS NULL AND created_at >= ?", userID, start).
		Pluck("created_at", &topicTimes).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(&Post{}).
		Where("user_id = ? AND deleted_at IS NULL AND status = ? AND created_at >= ?", userID, "visible", start).
		Pluck("created_at", &postTimes).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(topicTimes)+len(postTimes))
	for _, t := range append(topicTimes, postTimes...) {
		counts[t.UTC().Format("2006-01-02")]++
	}
	out := make([]ActivityDay, 0, days)
	max := 0
	for i := 0; i < days; i++ {
		d := start.AddDate(0, 0, i)
		n := counts[d.Format("2006-01-02")]
		if n > max {
			max = n
		}
		out = append(out, ActivityDay{Date: d, Count: n})
	}
	if max > 0 {
		for i := range out {
			if out[i].Count == 0 {
				continue
			}
			level := 1 + (out[i].Count*3)/max
			if level > 4 {
				level = 4
			}
			out[i].Level = level
		}
	}
	return out, nil
}

func (s *Service) ListUserPosts(ctx context.Context, userID string, page, perPage int) ([]PostRow, int64, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	base := s.db.WithContext(ctx).Table("posts AS p").
		Joins("JOIN topics t ON t.id = p.topic_id").
		Where("p.user_id = ? AND p.status = ? AND p.deleted_at IS NULL", userID, "visible").
		Where("t.deleted_at IS NULL")
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []PostRow
	err := base.Select("p.id, p.topic_id, t.title AS topic_title, p.user_id, p.post_number, p.raw, p.cooked_html, p.status, p.created_at, p.edited_at").
		Order("p.created_at DESC").
		Offset((page - 1) * perPage).Limit(perPage).
		Scan(&rows).Error
	return rows, total, err
}

func (s *Service) render(raw string) string {
	if s.md == nil {
		return ""
	}
	return s.md.Render(raw)
}

// authorInfo carries the display fields a list row needs, fetched in one call.
type authorInfo struct {
	Name   string
	Avatar string
}

// authorInfoMap resolves display names and avatars for a set of user IDs.
func (s *Service) authorInfoMap(ctx context.Context, ids []string) (map[string]authorInfo, error) {
	out := make(map[string]authorInfo, len(ids))
	if s.users == nil || len(ids) == 0 {
		return out, nil
	}
	sums, err := s.users.GetSummaries(ctx, dedupe(ids))
	if err != nil {
		return nil, err
	}
	for id, u := range sums {
		out[id] = authorInfo{Name: u.Username, Avatar: u.AvatarURL}
	}
	return out, nil
}

func (s *Service) authorMap(ctx context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if s.users == nil || len(ids) == 0 {
		return out, nil
	}
	sums, err := s.users.GetSummaries(ctx, dedupe(ids))
	if err != nil {
		return nil, err
	}
	for id, u := range sums {
		out[id] = u.Username
	}
	return out, nil
}

func topicAuthorIDs(rows []TopicRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.AuthorID)
	}
	return out
}

func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

type postCreatedPayload struct {
	TopicID         string   `json:"topic_id"`
	PostID          string   `json:"post_id"`
	TopicAuthorID   string   `json:"topic_author_id"`
	ActorID         string   `json:"actor_id"`
	ReplyToPostID   string   `json:"reply_to_post_id"`
	ReplyToAuthorID string   `json:"reply_to_author_id"`
	FollowerIDs     []string `json:"follower_ids"`
	Title           string   `json:"title"`
	PostNumber      int      `json:"post_number"`
	MentionIDs      []string `json:"mention_ids"`
}

var mentionPattern = regexp.MustCompile(`@([\p{L}\p{N}_]{2,50})`)

func (s *Service) resolveMentions(ctx context.Context, raw string) []string {
	if s.users == nil {
		return nil
	}
	matches := mentionPattern.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return nil
	}
	names := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, m := range matches {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sums, err := s.users.FindByUsernames(ctx, names)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(sums))
	added := map[string]bool{}
	for _, u := range sums {
		if u.ID != "" && !added[u.ID] {
			added[u.ID] = true
			out = append(out, u.ID)
		}
	}
	return out
}

func (s *Service) publishPost(ctx context.Context, topic *Topic, post *Post, replyTo string) {
	if s.bus == nil || topic == nil || post == nil {
		return
	}
	followerIDs := []string{}
	if s.engagement != nil {
		followerIDs, _ = s.engagement.TopicFollowers(ctx, topic.ID, 200)
	}
	replyAuthorID := ""
	if replyTo != "" {
		var parent Post
		if err := s.db.WithContext(ctx).Select("user_id").First(&parent, "id = ?", replyTo).Error; err == nil {
			replyAuthorID = parent.UserID
		}
	}
	payload, _ := json.Marshal(postCreatedPayload{
		TopicID:         topic.ID,
		PostID:          post.ID,
		TopicAuthorID:   topic.UserID,
		ActorID:         post.UserID,
		ReplyToPostID:   replyTo,
		ReplyToAuthorID: replyAuthorID,
		FollowerIDs:     followerIDs,
		Title:           topic.Title,
		PostNumber:      post.PostNumber,
		MentionIDs:      s.resolveMentions(ctx, post.Raw),
	})
	_ = s.bus.Publish(ctx, events.NewEvent("post.created", "post", post.ID, payload))
}

func (s *Service) TopicMeta(ctx context.Context, topicID string) (*cmod.TopicMeta, error) {
	var t Topic
	if err := s.db.WithContext(ctx).First(&t, "id = ?", topicID).Error; err != nil {
		return nil, httpx.NotFound("topic.not_found", "主题不存在")
	}
	return &cmod.TopicMeta{
		ID: t.ID, Number: t.Number, Title: t.Title, AuthorID: t.UserID, CategoryID: t.CategoryID,
		Status: t.Status, Visibility: t.Visibility, IsLocked: t.IsLocked,
	}, nil
}

// TopicMetaBatch resolves many topics in one query.
func (s *Service) TopicMetaBatch(ctx context.Context, topicIDs []string) (map[string]cmod.TopicMeta, error) {
	out := make(map[string]cmod.TopicMeta, len(topicIDs))
	if len(topicIDs) == 0 {
		return out, nil
	}
	var rows []Topic
	if err := s.db.WithContext(ctx).Where("id IN ?", dedupe(topicIDs)).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, t := range rows {
		out[t.ID] = cmod.TopicMeta{
			ID: t.ID, Number: t.Number, Title: t.Title, AuthorID: t.UserID, CategoryID: t.CategoryID,
			Status: t.Status, Visibility: t.Visibility, IsLocked: t.IsLocked,
		}
	}
	return out, nil
}

// PostMetaBatch resolves many posts plus their parent topics in two queries.
func (s *Service) PostMetaBatch(ctx context.Context, postIDs []string) (map[string]cmod.PostMeta, error) {
	out := make(map[string]cmod.PostMeta, len(postIDs))
	if len(postIDs) == 0 {
		return out, nil
	}
	var posts []Post
	if err := s.db.WithContext(ctx).Where("id IN ?", dedupe(postIDs)).Find(&posts).Error; err != nil {
		return nil, err
	}
	topicIDs := make([]string, 0, len(posts))
	for _, p := range posts {
		topicIDs = append(topicIDs, p.TopicID)
	}
	topics, err := s.TopicMetaBatch(ctx, topicIDs)
	if err != nil {
		return nil, err
	}
	senderIDs := make([]string, 0, len(posts))
	for _, p := range posts {
		senderIDs = append(senderIDs, p.UserID)
	}
	names := map[string]string{}
	if s.users != nil && len(senderIDs) > 0 {
		if sums, err := s.users.GetSummaries(ctx, dedupe(senderIDs)); err == nil {
			for id, u := range sums {
				names[id] = u.Username
			}
		}
	}
	for _, p := range posts {
		t, ok := topics[p.TopicID]
		if !ok {
			continue
		}
		out[p.ID] = cmod.PostMeta{
			ID: p.ID, TopicID: t.ID, TopicNumber: t.Number, TopicTitle: t.Title,
			AuthorID: p.UserID, AuthorName: names[p.UserID], PostNumber: p.PostNumber,
		}
	}
	return out, nil
}

func (s *Service) PostMeta(ctx context.Context, postID string) (*cmod.PostMeta, error) {
	var p Post
	if err := s.db.WithContext(ctx).First(&p, "id = ?", postID).Error; err != nil {
		return nil, httpx.NotFound("post.not_found", "回复不存在")
	}
	var t Topic
	if err := s.db.WithContext(ctx).First(&t, "id = ?", p.TopicID).Error; err != nil {
		return nil, httpx.NotFound("topic.not_found", "主题不存在")
	}
	name := ""
	if sums, err := s.users.GetSummaries(ctx, []string{p.UserID}); err == nil {
		name = sums[p.UserID].Username
	}
	return &cmod.PostMeta{
		ID: p.ID, TopicID: t.ID, TopicNumber: t.Number, TopicTitle: t.Title,
		AuthorID: p.UserID, AuthorName: name, PostNumber: p.PostNumber,
	}, nil
}

type engagementEvent struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
}

func (s *Service) Subscribe(bus events.Bus) error {
	if bus == nil {
		return nil
	}
	if err := bus.Subscribe("reaction.added", s.counterHandler("reaction", 1)); err != nil {
		return err
	}
	if err := bus.Subscribe("reaction.removed", s.counterHandler("reaction", -1)); err != nil {
		return err
	}
	if err := bus.Subscribe("bookmark.added", s.counterHandler("bookmark", 1)); err != nil {
		return err
	}
	return bus.Subscribe("bookmark.removed", s.counterHandler("bookmark", -1))
}

func (s *Service) counterHandler(kind string, delta int64) events.Handler {
	return func(ctx context.Context, e events.Event) error {
		var p engagementEvent
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if p.TargetID == "" {
			return nil
		}
		var expr any
		if delta > 0 {
			expr = gorm.Expr("like_count + ?", delta)
		} else {
			expr = gorm.Expr("CASE WHEN like_count > 0 THEN like_count - 1 ELSE 0 END")
		}
		switch kind {
		case "reaction":
			switch p.TargetType {
			case "post":
				return s.db.WithContext(ctx).Model(&Post{}).Where("id = ?", p.TargetID).UpdateColumn("like_count", expr).Error
			case "topic":
				return s.db.WithContext(ctx).Model(&Topic{}).Where("id = ?", p.TargetID).UpdateColumn("like_count", expr).Error
			}
		case "bookmark":
			if p.TargetType == "topic" {
				var bexpr any
				if delta > 0 {
					bexpr = gorm.Expr("bookmark_count + ?", delta)
				} else {
					bexpr = gorm.Expr("CASE WHEN bookmark_count > 0 THEN bookmark_count - 1 ELSE 0 END")
				}
				return s.db.WithContext(ctx).Model(&Topic{}).Where("id = ?", p.TargetID).UpdateColumn("bookmark_count", bexpr).Error
			}
		}
		return nil
	}
}

func (s *Service) indexTopic(ctx context.Context, topic *Topic, first *Post) {
	if s.search == nil || topic == nil {
		return
	}
	body := ""
	if first != nil {
		body = first.Raw
	}
	_ = s.search.Index(ctx, searchx.Document{
		ID: topic.ID, Type: "topic", Title: topic.Title, Body: body,
		CategoryID: topic.CategoryID, AuthorID: topic.UserID, Visibility: topic.Visibility,
	})
}

func (s *Service) indexPost(ctx context.Context, topic *Topic, post *Post) {
	if s.search == nil || topic == nil || post == nil {
		return
	}
	_ = s.search.Index(ctx, searchx.Document{
		ID: post.ID, Type: "post", TopicID: topic.ID, Title: topic.Title, Body: post.Raw,
		CategoryID: topic.CategoryID, AuthorID: post.UserID, Visibility: topic.Visibility,
	})
}

func (s *Service) deleteIndex(ctx context.Context, docType, docID string) {
	if s.search == nil {
		return
	}
	_ = s.search.Delete(ctx, docType, docID)
}

func (s *Service) settingInt(key string, fallback int) int64 {
	if s.settings == nil {
		return int64(fallback)
	}
	if v := s.settings.GetInt(key); v > 0 {
		return v
	}
	return int64(fallback)
}

func (s *Service) settingBool(key string, fallback bool) bool {
	if s.settings == nil {
		return fallback
	}
	return s.settings.GetBool(key)
}

// RenderMarkdown exposes sanitized markdown rendering for the editor preview.
func (s *Service) RenderMarkdown(raw string) string {
	if len([]rune(raw)) > 50000 {
		raw = string([]rune(raw)[:50000])
	}
	return s.render(raw)
}

// TrendingTopics returns recently active public topics ranked by a simple
// engagement score. It is used by the home sidebar.
func (s *Service) TrendingTopics(ctx context.Context, limit int) ([]Topic, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	// This runs on every feed page and sorts an expression over the last seven
	// days, so it cannot use an index. Cache it briefly; a few minutes of
	// staleness in a sidebar widget is harmless.
	cacheKey := "trending:" + strconv.Itoa(limit)
	if s.cache != nil {
		if raw, ok := s.cache.Get(ctx, cacheKey); ok {
			var rows []Topic
			if err := json.Unmarshal(raw, &rows); err == nil {
				return rows, nil
			}
		}
	}
	var rows []Topic
	err := s.db.WithContext(ctx).
		Where("deleted_at IS NULL AND status IN ? AND visibility = ?", []string{"open", "closed", "archived"}, "public").
		Where("bumped_at >= ?", time.Now().UTC().AddDate(0, 0, -7)).
		Order("(reply_count * 3 + like_count * 2 + view_count / 10) DESC").
		Order("bumped_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		if raw, err := json.Marshal(rows); err == nil {
			_ = s.cache.Set(ctx, cacheKey, raw, 2*time.Minute)
		}
	}
	return rows, nil
}

type SiteStats struct {
	Users  int64
	Topics int64
	Posts  int64
}

// SiteStats returns lightweight counters for sidebar cards.
func (s *Service) SiteStats(ctx context.Context) SiteStats {
	var out SiteStats
	// Rendered on every feed page; three COUNT(*) per request is wasteful, so
	// the aggregate is cached briefly and invalidated on writes.
	if s.cache != nil {
		if raw, ok := s.cache.Get(ctx, "stats:site"); ok {
			if err := json.Unmarshal(raw, &out); err == nil {
				return out
			}
		}
	}
	_ = s.db.WithContext(ctx).Table("users").Count(&out.Users).Error
	_ = s.db.WithContext(ctx).Model(&Topic{}).Where("deleted_at IS NULL").Count(&out.Topics).Error
	_ = s.db.WithContext(ctx).Model(&Post{}).Where("deleted_at IS NULL").Count(&out.Posts).Error
	if s.cache != nil {
		if raw, err := json.Marshal(out); err == nil {
			_ = s.cache.Set(ctx, "stats:site", raw, 60*time.Second)
		}
	}
	return out
}

// SetCache installs the shared cache used for expensive read aggregates.
func (s *Service) SetCache(c cache.Cache) { s.cache = c }

// invalidateStats drops the cached site counters after a write.
func (s *Service) invalidateStats(ctx context.Context) {
	if s.cache == nil {
		return
	}
	_ = s.cache.Delete(ctx, "stats:site")
	_ = s.cache.DeletePrefix(ctx, "trending:")
}

// RelatedTopics returns recent topics from the same category.
func (s *Service) RelatedTopics(ctx context.Context, topicID, categoryID string, limit int) ([]Topic, error) {
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	var tagIDs []string
	_ = s.db.WithContext(ctx).Table("topic_tags").Where("topic_id = ?", topicID).Pluck("tag_id", &tagIDs).Error
	if len(tagIDs) > 0 {
		var ids []string
		_ = s.db.WithContext(ctx).Table("topic_tags").
			Select("topic_id").
			Where("tag_id IN ? AND topic_id <> ?", tagIDs, topicID).
			Group("topic_id").Order("COUNT(*) DESC").Limit(limit).Pluck("topic_id", &ids).Error
		if len(ids) > 0 {
			var rows []Topic
			err := s.db.WithContext(ctx).
				Where("id IN ? AND deleted_at IS NULL AND status IN ?", ids, []string{"open", "closed", "archived"}).
				Order("bumped_at DESC").Limit(limit).Find(&rows).Error
			if err == nil && len(rows) > 0 {
				return rows, nil
			}
		}
	}
	var rows []Topic
	err := s.db.WithContext(ctx).
		Where("category_id = ? AND id <> ? AND deleted_at IS NULL AND status IN ?", categoryID, topicID, []string{"open", "closed", "archived"}).
		Order("bumped_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}
