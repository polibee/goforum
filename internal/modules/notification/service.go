package notification

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/events"
	"goforum/internal/platform/id"
)

type Service struct {
	notifier func(userID string)
	db       *gorm.DB
	users    contracts.UserReader
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Configure(users contracts.UserReader) { s.users = users }

func (s *Service) Subscribe(bus events.Bus) error {
	if err := bus.Subscribe("post.created", s.onPostCreated); err != nil {
		return err
	}
	if err := bus.Subscribe("reaction.added", s.onReaction); err != nil {
		return err
	}
	if err := bus.Subscribe("message.created", s.onMessage); err != nil {
		return err
	}
	if err := bus.Subscribe("moderation.decided", s.onModeration); err != nil {
		return err
	}
	if err := bus.Subscribe("moderation.report_resolved", s.onReportResolved); err != nil {
		return err
	}
	return bus.Subscribe("appeal.decided", s.onAppeal)
}

type TypeInfo struct {
	Key   string
	Label string
	Short string
}

var Types = []TypeInfo{
	{Key: "post.reply", Label: "回复我的主题", Short: "回复"},
	{Key: "post.quote", Label: "回复我的楼层", Short: "引用"},
	{Key: "post.watch", Label: "关注的主题有新回复", Short: "关注"},
	{Key: "post.mention", Label: "@提及我", Short: "@提及"},
	{Key: "reaction", Label: "点赞我的内容", Short: "点赞"},
	{Key: "message.new", Label: "新私信", Short: "私信"},
	{Key: "moderation", Label: "审核处理", Short: "审核"},
	{Key: "moderation.report", Label: "举报结果", Short: "举报"},
	{Key: "appeal", Label: "申诉结果", Short: "申诉"},
}

func typeShort(key string) string {
	for _, t := range Types {
		if t.Key == key {
			return t.Short
		}
	}
	return "通知"
}

func (s *Service) SetNotifier(fn func(userID string)) { s.notifier = fn }

func (s *Service) notify(userID string) {
	if s.notifier != nil && userID != "" {
		s.notifier(userID)
	}
}

func (s *Service) Allowed(ctx context.Context, userID, typ string) bool {
	var p Preference
	err := s.db.WithContext(ctx).Where("user_id = ? AND type = ?", userID, typ).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true
	}
	if err != nil {
		return true
	}
	return p.InApp
}

func (s *Service) Preferences(ctx context.Context, userID string) map[string]bool {
	out := make(map[string]bool, len(Types))
	for _, t := range Types {
		out[t.Key] = true
	}
	var rows []Preference
	_ = s.db.WithContext(ctx).Where("user_id = ?", userID).Find(&rows).Error
	for _, p := range rows {
		out[p.Type] = p.InApp
	}
	return out
}

func (s *Service) SetPreference(ctx context.Context, userID, typ string, inApp bool) error {
	row := Preference{UserID: userID, Type: typ, InApp: inApp}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "type"}},
		DoUpdates: clause.Assignments(map[string]any{"in_app": inApp}),
	}).Create(&row).Error
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

func (s *Service) onPostCreated(ctx context.Context, e events.Event) error {
	var p postCreatedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	// At most one notification per user per event; mentions win over replies.
	recipients := map[string]string{}
	if p.TopicAuthorID != "" && p.TopicAuthorID != p.ActorID {
		recipients[p.TopicAuthorID] = "post.reply"
	}
	if p.ReplyToAuthorID != "" && p.ReplyToAuthorID != p.ActorID && p.ReplyToAuthorID != p.TopicAuthorID {
		recipients[p.ReplyToAuthorID] = "post.quote"
	}
	for _, id := range p.FollowerIDs {
		if id == "" || id == p.ActorID {
			continue
		}
		if _, exists := recipients[id]; !exists {
			recipients[id] = "post.watch"
		}
	}
	for _, id := range p.MentionIDs {
		if id != "" && id != p.ActorID {
			recipients[id] = "post.mention"
		}
	}
	if len(recipients) == 0 {
		return nil
	}
	data, _ := json.Marshal(map[string]any{
		"title":       p.Title,
		"post_id":     p.PostID,
		"post_number": p.PostNumber,
	})
	// Resolve presence of opt-outs in one query instead of one per recipient.
	// With a few hundred topic followers the old loop issued 2 queries each.
	blocked := map[string]bool{}
	if len(recipients) > 0 {
		ids := make([]string, 0, len(recipients))
		for userID := range recipients {
			ids = append(ids, userID)
		}
		var prefs []Preference
		_ = s.db.WithContext(ctx).Where("user_id IN ?", ids).Find(&prefs).Error
		for _, pref := range prefs {
			if !pref.InApp {
				blocked[pref.UserID+"|"+pref.Type] = true
			}
		}
	}

	now := time.Now().UTC()
	rows := make([]Notification, 0, len(recipients))
	for userID, typ := range recipients {
		if blocked[userID+"|"+typ] {
			continue
		}
		rows = append(rows, Notification{
			ID:         id.New(),
			UserID:     userID,
			Type:       typ,
			ActorID:    p.ActorID,
			TargetType: "topic",
			TargetID:   p.TopicID,
			DataJSON:   string(data),
			EventID:    e.ID,
			CreatedAt:  now,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return s.insertBatch(ctx, rows)
}

type reactionPayload struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	TopicID    string `json:"topic_id"`
	ActorID    string `json:"actor_id"`
	AuthorID   string `json:"author_id"`
	Title      string `json:"title"`
	Reaction   string `json:"reaction"`
}

func (s *Service) onReaction(ctx context.Context, e events.Event) error {
	var p reactionPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.AuthorID == "" || p.AuthorID == p.ActorID || p.TopicID == "" {
		return nil
	}
	if !s.Allowed(ctx, p.AuthorID, "reaction") {
		return nil
	}
	data, _ := json.Marshal(map[string]any{"title": p.Title, "target_id": p.TargetID, "target_type": p.TargetType})
	n := Notification{
		ID:         id.New(),
		UserID:     p.AuthorID,
		Type:       "reaction",
		ActorID:    p.ActorID,
		TargetType: "topic",
		TargetID:   p.TopicID,
		DataJSON:   string(data),
		EventID:    e.ID,
		CreatedAt:  time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&n)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		s.notify(p.AuthorID)
	}
	return nil
}

type messagePayload struct {
	ConversationID string   `json:"conversation_id"`
	MessageID      string   `json:"message_id"`
	ActorID        string   `json:"actor_id"`
	RecipientIDs   []string `json:"recipient_ids"`
	Preview        string   `json:"preview"`
}

func (s *Service) onMessage(ctx context.Context, e events.Event) error {
	var p messagePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.ConversationID == "" || p.ActorID == "" {
		return nil
	}
	data, _ := json.Marshal(map[string]any{"title": p.Preview, "message_id": p.MessageID})
	for _, uid := range p.RecipientIDs {
		if uid == "" || uid == p.ActorID || !s.Allowed(ctx, uid, "message.new") {
			continue
		}
		n := Notification{
			ID:         id.New(),
			UserID:     uid,
			Type:       "message.new",
			ActorID:    p.ActorID,
			TargetType: "conversation",
			TargetID:   p.ConversationID,
			DataJSON:   string(data),
			EventID:    e.ID,
			CreatedAt:  time.Now().UTC(),
		}
		result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&n)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			s.notify(uid)
		}
	}
	return nil
}

type moderationPayload struct {
	RecipientID string `json:"recipient_id"`
	ModeratorID string `json:"moderator_id"`
	TargetType  string `json:"target_type"`
	TargetID    string `json:"target_id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Action      string `json:"action"`
	Note        string `json:"note"`
}

func (s *Service) onModeration(ctx context.Context, e events.Event) error {
	var p moderationPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.RecipientID == "" || p.ModeratorID == "" || !s.Allowed(ctx, p.RecipientID, "moderation") {
		return nil
	}
	data, _ := json.Marshal(map[string]any{
		"title": p.Title, "url": p.URL, "action": p.Action, "note": p.Note,
	})
	n := Notification{
		ID: id.New(), UserID: p.RecipientID, Type: "moderation", ActorID: p.ModeratorID,
		TargetType: p.TargetType, TargetID: p.TargetID, DataJSON: string(data),
		EventID: e.ID, CreatedAt: time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&n)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		s.notify(p.RecipientID)
	}
	return nil
}

func (s *Service) onReportResolved(ctx context.Context, e events.Event) error {
	var p moderationPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.RecipientID == "" || p.ModeratorID == "" || !s.Allowed(ctx, p.RecipientID, "moderation.report") {
		return nil
	}
	data, _ := json.Marshal(map[string]any{
		"title": p.Title, "url": p.URL, "action": p.Action, "note": p.Note,
	})
	n := Notification{
		ID: id.New(), UserID: p.RecipientID, Type: "moderation.report", ActorID: p.ModeratorID,
		TargetType: p.TargetType, TargetID: p.TargetID, DataJSON: string(data),
		EventID: e.ID, CreatedAt: time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&n)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		s.notify(p.RecipientID)
	}
	return nil
}

type appealPayload struct {
	RecipientID string `json:"recipient_id"`
	ModeratorID string `json:"moderator_id"`
	Username    string `json:"username"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Action      string `json:"action"`
	Note        string `json:"note"`
}

func (s *Service) onAppeal(ctx context.Context, e events.Event) error {
	var p appealPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.RecipientID == "" || p.ModeratorID == "" || !s.Allowed(ctx, p.RecipientID, "appeal") {
		return nil
	}
	data, _ := json.Marshal(map[string]any{
		"title": p.Title, "url": p.URL, "action": p.Action, "note": p.Note,
	})
	n := Notification{
		ID: id.New(), UserID: p.RecipientID, Type: "appeal", ActorID: p.ModeratorID,
		TargetType: "user", TargetID: p.RecipientID, DataJSON: string(data),
		EventID: e.ID, CreatedAt: time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&n)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		s.notify(p.RecipientID)
	}
	return nil
}

type Row struct {
	ID          string
	Type        string
	TypeLabel   string
	ActorName   string
	ActorAvatar string
	Title       string
	URL         string
	Action      string
	Read        bool
	CreatedAt   time.Time
}

func (s *Service) List(ctx context.Context, userID string, page, perPage int, typ string) ([]Row, int64, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	base := s.db.WithContext(ctx).Model(&Notification{}).Where("user_id = ?", userID)
	if typ != "" {
		base = base.Where("type = ?", typ)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Notification
	if err := base.Order("created_at DESC").
		Offset((page - 1) * perPage).Limit(perPage).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	actors := make([]string, 0, len(rows))
	for _, n := range rows {
		if n.ActorID != "" {
			actors = append(actors, n.ActorID)
		}
	}
	nameByID := map[string]string{}
	avatarByID := map[string]string{}
	if s.users != nil && len(actors) > 0 {
		if sums, err := s.users.GetSummaries(ctx, actors); err == nil {
			for id, u := range sums {
				nameByID[id] = u.Username
				avatarByID[id] = u.AvatarURL
			}
		}
	}

	out := make([]Row, 0, len(rows))
	for _, n := range rows {
		var data struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal([]byte(n.DataJSON), &data)
		url := "/t/" + n.TargetID
		if n.Type == "message.new" {
			url = "/messages/" + n.TargetID
		}
		out = append(out, Row{
			ID:        n.ID,
			Type:      n.Type,
			TypeLabel: typeShort(n.Type),
			ActorName: nameByID[n.ActorID],
			Title:     data.Title,
			URL:       url,
			Read:      n.ReadAt != nil,
			CreatedAt: n.CreatedAt,
		})
	}
	return out, total, nil
}

func (s *Service) LatestUnread(ctx context.Context, userID string) (*Row, error) {
	var n Notification
	err := s.db.WithContext(ctx).Where("user_id = ? AND read_at IS NULL", userID).
		Order("created_at DESC").First(&n).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var data struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal([]byte(n.DataJSON), &data)
	actorName := ""
	actorAvatar := ""
	if s.users != nil && n.ActorID != "" {
		if sums, err := s.users.GetSummaries(ctx, []string{n.ActorID}); err == nil {
			actorName = sums[n.ActorID].Username
			actorAvatar = sums[n.ActorID].AvatarURL
		}
	}
	url := "/t/" + n.TargetID
	if n.Type == "message.new" {
		url = "/messages/" + n.TargetID
	}
	return &Row{
		ID: n.ID, Type: n.Type, TypeLabel: typeShort(n.Type), ActorName: actorName, ActorAvatar: actorAvatar,
		Title: data.Title, URL: url, CreatedAt: n.CreatedAt,
	}, nil
}

func (s *Service) UnreadCount(ctx context.Context, userID string, typ ...string) (int64, error) {
	var n int64
	q := s.db.WithContext(ctx).Model(&Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID)
	if len(typ) > 0 && typ[0] != "" {
		q = q.Where("type = ?", typ[0])
	}
	err := q.Count(&n).Error
	return n, err
}

func (s *Service) MarkAllRead(ctx context.Context, userID string) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Update("read_at", now).Error
}

func (s *Service) MarkRead(ctx context.Context, userID, notificationID string) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&Notification{}).
		Where("id = ? AND user_id = ?", notificationID, userID).
		Update("read_at", now).Error
}

// insertBatch writes notifications in chunks and pushes SSE updates for the
// users that actually received a new row. Batching keeps a single reply from
// generating one INSERT per follower.
func (s *Service) insertBatch(ctx context.Context, rows []Notification) error {
	const chunk = 200
	notified := make(map[string]bool, len(rows))
	for start := 0; start < len(rows); start += chunk {
		end := start + chunk
		if end > len(rows) {
			end = len(rows)
		}
		part := rows[start:end]
		result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&part)
		if result.Error != nil {
			return result.Error
		}
		// Create() does not tell which rows were skipped, so notify everyone in
		// the chunk; SSE is idempotent for the client.
		for _, n := range part {
			if !notified[n.UserID] {
				notified[n.UserID] = true
				s.notify(n.UserID)
			}
		}
	}
	return nil
}
