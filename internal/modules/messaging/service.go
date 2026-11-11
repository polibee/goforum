package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	mcontracts "goforum/internal/modules/messaging/contracts"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/id"
	"goforum/internal/platform/markdown"
)

type Service struct {
	db    *gorm.DB
	users contracts.UserReader
	md    *markdown.Renderer
	bus   events.Bus
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Configure(users contracts.UserReader, md *markdown.Renderer, bus events.Bus) {
	s.users = users
	s.md = md
	s.bus = bus
}

type ConversationRow struct {
	ID             string
	Title          string
	OtherName      string
	OtherAvatarURL string
	LastExcerpt    string
	LastMessageAt  *time.Time
	Unread         int64
}

type MessageRow struct {
	ID           string
	SenderID     string
	SenderName   string
	SenderAvatar string
	CookedHTML   string
	Raw          string
	CreatedAt    time.Time
	Mine         bool
	ShowDate     bool
	ShowRead     bool
}

type ConversationView struct {
	Conversation   Conversation
	OtherName      string
	OtherAvatarURL string
	Messages       []MessageRow
	// TotalMessages and HasOlder let the page offer "load earlier messages"
	// instead of silently dropping everything past the newest page.
	TotalMessages int64
	HasOlder      bool
	OldestID      string
}

func (s *Service) StartDirect(ctx context.Context, actorID, username string) (*Conversation, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return nil, httpx.Invalid(map[string]string{"username": "请输入用户名"})
	}
	if s.users == nil {
		return nil, httpx.NewError("messaging.unavailable", 503, "私信暂不可用")
	}
	sums, err := s.users.FindByUsernames(ctx, []string{username})
	if err != nil {
		return nil, err
	}
	target, ok := sums[username]
	if !ok {
		return nil, httpx.NotFound("user.not_found", "用户不存在")
	}
	if target.ID == actorID {
		return nil, httpx.Invalid(map[string]string{"username": "不能给自己发私信"})
	}
	if s.IsBlocked(ctx, actorID, target.ID) {
		return nil, httpx.Forbidden("message.blocked", "无法发起会话：对方或你已屏蔽")
	}

	var existing Conversation
	err = s.db.WithContext(ctx).Table("conversations AS c").
		Joins("JOIN conversation_participants p1 ON p1.conversation_id = c.id AND p1.user_id = ?", actorID).
		Joins("JOIN conversation_participants p2 ON p2.conversation_id = c.id AND p2.user_id = ?", target.ID).
		Where("c.type = ?", "direct").
		First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	now := time.Now().UTC()
	conv := &Conversation{
		ID: id.New(), Type: "direct", CreatedBy: actorID,
		ParticipantCount: 2, CreatedAt: now, UpdatedAt: now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(conv).Error; err != nil {
			return err
		}
		for _, uid := range []string{actorID, target.ID} {
			if err := tx.Create(&Participant{
				ConversationID: conv.ID, UserID: uid, JoinedAt: now,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conv, nil
}

func (s *Service) Send(ctx context.Context, conversationID, senderID, raw string) (*Message, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, httpx.Invalid(map[string]string{"raw": "消息不能为空"})
	}
	if len([]rune(raw)) > 10000 {
		return nil, httpx.Invalid(map[string]string{"raw": "消息过长"})
	}
	if err := s.assertParticipant(ctx, conversationID, senderID); err != nil {
		return nil, err
	}
	var recipients []string
	_ = s.db.WithContext(ctx).Model(&Participant{}).
		Where("conversation_id = ? AND user_id <> ?", conversationID, senderID).
		Pluck("user_id", &recipients).Error
	for _, uid := range recipients {
		if s.IsBlocked(ctx, uid, senderID) {
			return nil, httpx.Forbidden("message.blocked", "对方已屏蔽你的消息")
		}
	}
	cooked := ""
	if s.md != nil {
		cooked = s.md.Render(raw)
	}
	now := time.Now().UTC()
	msg := &Message{
		ID: id.New(), ConversationID: conversationID, SenderID: senderID,
		Raw: raw, CookedHTML: cooked, Status: "visible", CreatedAt: now,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(msg).Error; err != nil {
			return err
		}
		if err := tx.Model(&Conversation{}).Where("id = ?", conversationID).Updates(map[string]any{
			"last_message_id": msg.ID, "last_message_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&Participant{}).
			Where("conversation_id = ? AND user_id = ?", conversationID, senderID).
			Update("last_read_at", now).Error
	})
	if err != nil {
		return nil, err
	}

	s.publishMessage(ctx, msg, recipients)
	return msg, nil
}

func (s *Service) IsBlocked(ctx context.Context, a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	var n int64
	_ = s.db.WithContext(ctx).Model(&MessageBlock{}).
		Where("(blocker_id = ? AND blocked_id = ?) OR (blocker_id = ? AND blocked_id = ?)", a, b, b, a).
		Count(&n).Error
	return n > 0
}

func (s *Service) ToggleBlock(ctx context.Context, actorID, otherID string) (bool, error) {
	if actorID == otherID {
		return false, httpx.Invalid(map[string]string{"user": "不能屏蔽自己"})
	}
	var existing MessageBlock
	err := s.db.WithContext(ctx).Where("blocker_id = ? AND blocked_id = ?", actorID, otherID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := s.db.WithContext(ctx).Create(&MessageBlock{
			BlockerID: actorID, BlockedID: otherID, CreatedAt: time.Now().UTC(),
		}).Error; err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.db.WithContext(ctx).Delete(&MessageBlock{}, "blocker_id = ? AND blocked_id = ?", actorID, otherID).Error; err != nil {
		return false, err
	}
	return false, nil
}

func (s *Service) OtherParticipant(ctx context.Context, conversationID, userID string) string {
	var other string
	_ = s.db.WithContext(ctx).Model(&Participant{}).
		Where("conversation_id = ? AND user_id <> ?", conversationID, userID).Limit(1).Pluck("user_id", &other).Error
	return other
}

// UnreadTotal counts unread messages across every conversation the user is in.
// It is a single aggregate query: this runs on every page render, so a per
// conversation loop used to cost 2 queries per conversation.
func (s *Service) UnreadTotal(ctx context.Context, userID string) int64 {
	var total int64
	// The participant row carries last_read_at; messages newer than it and not
	// written by this user are unread.
	_ = s.db.WithContext(ctx).
		Table("messages AS m").
		Joins("JOIN conversation_participants p ON p.conversation_id = m.conversation_id AND p.user_id = ?", userID).
		Where("m.sender_id <> ?", userID).
		Where("m.deleted_at IS NULL").
		Where("p.last_read_at IS NULL OR m.created_at > p.last_read_at").
		Count(&total).Error
	return total
}

func (s *Service) MessageMeta(ctx context.Context, messageID string) (*mcontracts.MessageMeta, error) {
	var m Message
	if err := s.db.WithContext(ctx).First(&m, "id = ?", messageID).Error; err != nil {
		return nil, httpx.NotFound("message.not_found", "消息不存在")
	}
	name := ""
	if s.users != nil {
		if sums, err := s.users.GetSummaries(ctx, []string{m.SenderID}); err == nil {
			name = sums[m.SenderID].Username
		}
	}
	return &mcontracts.MessageMeta{
		ID: m.ID, ConversationID: m.ConversationID, SenderID: m.SenderID,
		SenderName: name, Preview: markdown.Excerpt(m.Raw, 80),
	}, nil
}

func (s *Service) publishMessage(ctx context.Context, msg *Message, recipients []string) {
	if s.bus == nil || len(recipients) == 0 {
		return
	}
	preview := markdown.Excerpt(msg.Raw, 60)
	payload, _ := json.Marshal(map[string]any{
		"conversation_id": msg.ConversationID,
		"message_id":      msg.ID,
		"actor_id":        msg.SenderID,
		"recipient_ids":   recipients,
		"preview":         preview,
	})
	_ = s.bus.Publish(ctx, events.NewEvent("message.created", "message", msg.ID, payload))
}

func (s *Service) List(ctx context.Context, userID string, limit, offset int, query, otherID string, unreadOnly bool) ([]ConversationRow, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	base := s.db.WithContext(ctx).Table("conversations AS c").
		Joins("JOIN conversation_participants p ON p.conversation_id = c.id AND p.user_id = ?", userID)
	if otherID != "" {
		base = base.Joins("JOIN conversation_participants p2 ON p2.conversation_id = c.id AND p2.user_id = ?", otherID)
	}
	if query != "" {
		base = base.Where("EXISTS (SELECT 1 FROM messages m WHERE m.conversation_id = c.id AND m.deleted_at IS NULL AND m.raw LIKE ?)", "%"+query+"%")
	}
	if unreadOnly {
		base = base.Where("EXISTS (SELECT 1 FROM messages mu WHERE mu.conversation_id = c.id AND mu.sender_id <> ? AND (p.last_read_at IS NULL OR mu.created_at > p.last_read_at))", userID)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var convs []Conversation
	if err := base.Select("c.*").Order("c.last_message_at DESC").Limit(limit).Offset(offset).Scan(&convs).Error; err != nil {
		return nil, 0, err
	}

	if len(convs) == 0 {
		return []ConversationRow{}, total, nil
	}
	convIDs := make([]string, 0, len(convs))
	lastIDs := make([]string, 0, len(convs))
	for _, c := range convs {
		convIDs = append(convIDs, c.ID)
		if c.LastMessageID != "" {
			lastIDs = append(lastIDs, c.LastMessageID)
		}
	}
	var parts []Participant
	_ = s.db.WithContext(ctx).Where("conversation_id IN ? AND user_id <> ?", convIDs, userID).Find(&parts).Error
	otherByConv := make(map[string]string, len(parts))
	otherIDs := make([]string, 0, len(parts))
	for _, p := range parts {
		otherByConv[p.ConversationID] = p.UserID
		otherIDs = append(otherIDs, p.UserID)
	}
	names := map[string]string{}
	avatars := map[string]string{}
	seenIDs := make(map[string]bool, len(otherIDs))
	uniqueIDs := otherIDs[:0]
	for _, id := range otherIDs {
		if id != "" && !seenIDs[id] {
			seenIDs[id] = true
			uniqueIDs = append(uniqueIDs, id)
		}
	}
	if s.users != nil && len(uniqueIDs) > 0 {
		if sums, err := s.users.GetSummaries(ctx, uniqueIDs); err == nil {
			for id, u := range sums {
				names[id] = u.Username
				avatars[id] = u.AvatarURL
			}
		}
	}
	lastByID := map[string]Message{}
	if len(lastIDs) > 0 {
		var last []Message
		_ = s.db.WithContext(ctx).Where("id IN ?", lastIDs).Find(&last).Error
		for _, m := range last {
			lastByID[m.ID] = m
		}
	}
	type unreadRow struct {
		ConversationID string
		Total          int64
	}
	var unreadRows []unreadRow
	_ = s.db.WithContext(ctx).Table("messages AS m").
		Select("m.conversation_id, COUNT(*) AS total").
		Joins("JOIN conversation_participants p ON p.conversation_id = m.conversation_id AND p.user_id = ?", userID).
		Where("m.conversation_id IN ? AND m.sender_id <> ? AND m.deleted_at IS NULL AND (p.last_read_at IS NULL OR m.created_at > p.last_read_at)", convIDs, userID).
		Group("m.conversation_id").Scan(&unreadRows).Error
	unreadByConv := make(map[string]int64, len(unreadRows))
	for _, u := range unreadRows {
		unreadByConv[u.ConversationID] = u.Total
	}

	rows := make([]ConversationRow, 0, len(convs))
	for _, c := range convs {
		otherID := otherByConv[c.ID]
		row := ConversationRow{
			ID: c.ID, Title: c.Title, OtherName: names[otherID], OtherAvatarURL: avatars[otherID],
			LastMessageAt: c.LastMessageAt, Unread: unreadByConv[c.ID],
		}
		if m, ok := lastByID[c.LastMessageID]; ok {
			row.LastExcerpt = markdown.Excerpt(m.Raw, 80)
		}
		rows = append(rows, row)
	}
	return rows, total, nil
}

// Get loads the newest page of a conversation.
func (s *Service) Get(ctx context.Context, conversationID, userID string) (*ConversationView, error) {
	return s.GetPage(ctx, conversationID, userID, "", 50)
}

// GetPage loads up to limit messages. With before="" it returns the newest
// page; otherwise only messages older than that message ID. Rows are reversed
// into chronological order before rendering.
func (s *Service) GetPage(ctx context.Context, conversationID, userID, before string, limit int) (*ConversationView, error) {
	if err := s.assertParticipant(ctx, conversationID, userID); err != nil {
		return nil, err
	}
	var conv Conversation
	if err := s.db.WithContext(ctx).First(&conv, "id = ?", conversationID).Error; err != nil {
		return nil, httpx.NotFound("conversation.not_found", "会话不存在")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var total int64
	if err := s.db.WithContext(ctx).Model(&Message{}).
		Where("conversation_id = ?", conversationID).Count(&total).Error; err != nil {
		return nil, err
	}
	// Fetch the newest page first so long conversations never hide new replies.
	q := s.db.WithContext(ctx).Where("conversation_id = ?", conversationID)
	if before != "" {
		// created_at is the sort key; the id breaks ties within the same tick.
		var cursor Message
		if err := s.db.WithContext(ctx).First(&cursor, "id = ? AND conversation_id = ?", before, conversationID).Error; err == nil {
			q = q.Where("(created_at < ?) OR (created_at = ? AND id < ?)",
				cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
		}
	}
	var msgs []Message
	if err := q.Order("created_at DESC, id DESC").Limit(limit).Find(&msgs).Error; err != nil {
		return nil, err
	}
	hasOlder := int64(len(msgs)) < total || total > int64(limit)
	// Reverse into chronological order for rendering.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.SenderID)
	}
	names := map[string]string{}
	senderAvatars := map[string]string{}
	if s.users != nil && len(ids) > 0 {
		if sums, err := s.users.GetSummaries(ctx, ids); err == nil {
			for id, u := range sums {
				names[id] = u.Username
				senderAvatars[id] = u.AvatarURL
			}
		}
	}
	var otherID string
	var otherLastRead *time.Time
	var other Participant
	if err := s.db.WithContext(ctx).Where("conversation_id = ? AND user_id <> ?", conversationID, userID).
		First(&other).Error; err == nil {
		otherID, otherLastRead = other.UserID, other.LastReadAt
	}
	rows := make([]MessageRow, 0, len(msgs))
	lastDate := ""
	lastMine := -1
	for _, m := range msgs {
		date := m.CreatedAt.Format("2006-01-02")
		row := MessageRow{
			ID: m.ID, SenderID: m.SenderID, SenderName: names[m.SenderID], SenderAvatar: senderAvatars[m.SenderID],
			CookedHTML: m.CookedHTML, Raw: m.Raw, CreatedAt: m.CreatedAt, Mine: m.SenderID == userID,
			ShowDate: date != lastDate,
		}
		if row.Mine {
			lastMine = len(rows)
		}
		rows = append(rows, row)
		lastDate = date
	}
	if lastMine >= 0 && otherLastRead != nil && !otherLastRead.Before(rows[lastMine].CreatedAt) {
		rows[lastMine].ShowRead = true
	}

	// Mark read up to now.
	now := time.Now().UTC()
	_ = s.db.WithContext(ctx).Model(&Participant{}).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Update("last_read_at", now).Error

	view := &ConversationView{
		Conversation: conv, Messages: rows,
		TotalMessages: total, HasOlder: hasOlder && int64(len(rows)) < total,
	}
	if len(rows) > 0 {
		view.OldestID = rows[0].ID
	}
	if s.users != nil && otherID != "" {
		if sums, err := s.users.GetSummaries(ctx, []string{otherID}); err == nil {
			view.OtherName = sums[otherID].Username
			view.OtherAvatarURL = sums[otherID].AvatarURL
		}
	}
	return view, nil
}

func (s *Service) MarkRead(ctx context.Context, conversationID, userID string) error {
	return s.db.WithContext(ctx).Model(&Participant{}).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Update("last_read_at", time.Now().UTC()).Error
}

func (s *Service) assertParticipant(ctx context.Context, conversationID, userID string) error {
	var n int64
	if err := s.db.WithContext(ctx).Model(&Participant{}).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return httpx.NotFound("conversation.not_found", "会话不存在")
	}
	return nil
}

func (s *Service) unreadCount(ctx context.Context, conversationID, userID string) int64 {
	var p Participant
	if err := s.db.WithContext(ctx).Where("conversation_id = ? AND user_id = ?", conversationID, userID).First(&p).Error; err != nil {
		return 0
	}
	q := s.db.WithContext(ctx).Model(&Message{}).
		Where("conversation_id = ? AND sender_id <> ?", conversationID, userID)
	if p.LastReadAt != nil {
		q = q.Where("created_at > ?", *p.LastReadAt)
	}
	var n int64
	_ = q.Count(&n).Error
	return n
}
