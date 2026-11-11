package moderation

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"goforum/internal/modules/auth/contracts"
	cmod "goforum/internal/modules/community/contracts"
	mcontracts "goforum/internal/modules/messaging/contracts"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/id"
	"goforum/internal/platform/settings"
)

type Service struct {
	db        *gorm.DB
	settings  *settings.Store
	community cmod.Reader
	moderator cmod.Moderator
	auth      contracts.Authenticator
	messages  mcontracts.Reader
	audit     audit.Writer
	bus       events.Bus
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Configure(st *settings.Store, community cmod.Reader, moderator cmod.Moderator, auth contracts.Authenticator, messages mcontracts.Reader, auditWriter audit.Writer, bus events.Bus) {
	s.settings = st
	s.community = community
	s.moderator = moderator
	s.auth = auth
	s.messages = messages
	s.audit = auditWriter
	s.bus = bus
}

func (s *Service) TargetInfo(ctx context.Context, targetType, targetID string) (title, author string, err error) {
	switch targetType {
	case "topic":
		if s.community == nil {
			return "", "", httpx.NewError("moderation.unavailable", 503, "审核服务未就绪")
		}
		meta, err := s.community.TopicMeta(ctx, targetID)
		if err != nil {
			return "", "", err
		}
		return meta.Title, meta.AuthorID, nil
	case "post":
		if s.community == nil {
			return "", "", httpx.NewError("moderation.unavailable", 503, "审核服务未就绪")
		}
		meta, err := s.community.PostMeta(ctx, targetID)
		if err != nil {
			return "", "", err
		}
		return meta.TopicTitle, meta.AuthorID, nil
	case "message":
		if s.messages == nil {
			return "", "", httpx.NewError("moderation.unavailable", 503, "审核服务未就绪")
		}
		meta, err := s.messages.MessageMeta(ctx, targetID)
		if err != nil {
			return "", "", err
		}
		return meta.Preview, meta.SenderID, nil
	default:
		return "", "", httpx.Invalid(map[string]string{"target_type": "不支持的举报对象"})
	}
}

func (s *Service) Flag(ctx context.Context, reporterID, targetType, targetID, reasonCode, reasonText string) error {
	if reporterID == "" || targetID == "" {
		return httpx.Invalid(map[string]string{"target": "举报对象无效"})
	}
	_, author, err := s.TargetInfo(ctx, targetType, targetID)
	if err != nil {
		return err
	}
	if author != "" && author == reporterID {
		return httpx.Invalid(map[string]string{"target": "不能举报自己的内容"})
	}
	if reasonCode == "" {
		reasonCode = "other"
	}
	flag := Flag{
		ReporterID: reporterID, TargetType: targetType, TargetID: targetID,
		ReasonCode: reasonCode, ReasonText: strings.TrimSpace(reasonText),
		Status: "pending", CreatedAt: time.Now().UTC(),
	}
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&flag)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil // duplicate report
	}

	var count int64
	_ = s.db.WithContext(ctx).Model(&Flag{}).
		Where("target_type = ? AND target_id = ?", targetType, targetID).Count(&count).Error

	item := ReviewItem{
		ID: id.New(), ItemType: targetType, ItemID: targetID, Source: "flag",
		Status: "pending", Priority: int(count), CreatedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error; err != nil {
		return err
	}

	threshold := int64(3)
	if s.settings != nil {
		if v := s.settings.GetInt("moderation.auto_hide_flags"); v > 0 {
			threshold = v
		}
	}
	if count >= threshold && s.moderator != nil {
		switch targetType {
		case "post":
			_ = s.moderator.HidePost(ctx, targetID)
		case "topic":
			_ = s.moderator.HideTopic(ctx, targetID)
		}
	}

	s.writeAudit(ctx, reporterID, "flag.create", targetType, targetID, reasonText)
	return nil
}

func (s *Service) Enqueue(ctx context.Context, itemType, itemID, source string) error {
	item := ReviewItem{
		ID: id.New(), ItemType: itemType, ItemID: itemID, Source: source,
		Status: "pending", Priority: 0, CreatedAt: time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error
}

type ReviewRow struct {
	Item         ReviewItem
	TargetTitle  string
	TargetAuthor string
	FlagCount    int64
	Flags        []Flag
}

type QueueStats struct {
	Pending      int64
	Claimed      int64
	DecidedToday int64
}

func (s *Service) Stats(ctx context.Context) (QueueStats, error) {
	var out QueueStats
	if err := s.db.WithContext(ctx).Model(&ReviewItem{}).Where("status = ?", "pending").Count(&out.Pending).Error; err != nil {
		return out, err
	}
	if err := s.db.WithContext(ctx).Model(&ReviewItem{}).Where("status = ?", "claimed").Count(&out.Claimed).Error; err != nil {
		return out, err
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	err := s.db.WithContext(ctx).Model(&ReviewItem{}).Where("status = ? AND decided_at >= ?", "decided", start).Count(&out.DecidedToday).Error
	return out, err
}

func (s *Service) ListQueue(ctx context.Context, status string, page, perPage int) ([]ReviewRow, int64, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	q := s.db.WithContext(ctx).Model(&ReviewItem{})
	if status == "" {
		q = q.Where("status IN ?", []string{"pending", "claimed"})
	} else {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []ReviewItem
	if err := q.Order("priority DESC, created_at ASC").
		Offset((page - 1) * perPage).Limit(perPage).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	if len(items) == 0 {
		return nil, total, nil
	}
	// Resolve titles, flag counts and recent flags for the whole page in a
	// handful of queries instead of three per row.
	rowIndex := make(map[string]int, len(items)*2)
	topicIDs := make([]string, 0, len(items))
	postIDs := make([]string, 0, len(items))
	pairs := make([]FlagPair, 0, len(items))
	for i, item := range items {
		key := item.ItemType + "|" + item.ItemID
		rowIndex[key] = i
		pairs = append(pairs, FlagPair{TargetType: item.ItemType, TargetID: item.ItemID})
		switch item.ItemType {
		case "topic":
			topicIDs = append(topicIDs, item.ItemID)
		case "post":
			postIDs = append(postIDs, item.ItemID)
		}
	}
	titles := s.batchTargetTitles(ctx, topicIDs, postIDs)
	counts, recent := s.flagBatch(ctx, pairs)

	rows := make([]ReviewRow, 0, len(items))
	for _, item := range items {
		row := ReviewRow{Item: item}
		key := item.ItemType + "|" + item.ItemID
		row.TargetTitle, row.TargetAuthor = titles[key].Title, titles[key].Author
		row.FlagCount = counts[key]
		row.Flags = recent[key]
		rows = append(rows, row)
	}
	return rows, total, nil
}

// FlagPair names one target whose flags are being counted.
type FlagPair struct {
	TargetType string
	TargetID   string
}

// batchTargetTitles resolves topic and post titles/authors for a page of
// review items. Types without a batch path fall back to the single lookup.
func (s *Service) batchTargetTitles(ctx context.Context, topicIDs, postIDs []string) map[string]struct{ Title, Author string } {
	out := make(map[string]struct{ Title, Author string }, len(topicIDs)+len(postIDs))
	if s.community == nil {
		return out
	}
	if len(topicIDs) > 0 {
		if metas, err := s.community.TopicMetaBatch(ctx, topicIDs); err == nil {
			for id, m := range metas {
				out["topic|"+id] = struct{ Title, Author string }{m.Title, m.AuthorID}
			}
		}
	}
	if len(postIDs) > 0 {
		if metas, err := s.community.PostMetaBatch(ctx, postIDs); err == nil {
			for id, m := range metas {
				out["post|"+id] = struct{ Title, Author string }{m.TopicTitle, m.AuthorID}
			}
		}
	}
	return out
}

// flagBatch returns per-target flag counts and the five most recent flags.
func (s *Service) flagBatch(ctx context.Context, pairs []FlagPair) (map[string]int64, map[string][]Flag) {
	counts := make(map[string]int64, len(pairs))
	recent := make(map[string][]Flag, len(pairs))
	if len(pairs) == 0 {
		return counts, recent
	}
	// One grouped count for the whole page.
	type countRow struct {
		TargetType string
		TargetID   string
		N          int64
	}
	var countRows []countRow
	_ = s.db.WithContext(ctx).Model(&Flag{}).
		Select("target_type, target_id, COUNT(*) AS n").
		Where("(target_type, target_id) IN ?", pairTuples(pairs)).
		Group("target_type, target_id").Scan(&countRows).Error
	for _, r := range countRows {
		counts[r.TargetType+"|"+r.TargetID] = r.N
	}

	// One query fetches recent flags for every target, then the newest five
	// per target are kept in memory (bounded by page size × 5).
	var flags []Flag
	_ = s.db.WithContext(ctx).
		Where("(target_type, target_id) IN ?", pairTuples(pairs)).
		Order("created_at DESC").Limit(len(pairs) * 5).Find(&flags).Error
	for _, f := range flags {
		key := f.TargetType + "|" + f.TargetID
		if len(recent[key]) >= 5 {
			continue
		}
		recent[key] = append(recent[key], f)
	}
	return counts, recent
}

// pairTuples expands pairs for a SQL row-value IN clause.
func pairTuples(pairs []FlagPair) [][]any {
	out := make([][]any, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, []any{p.TargetType, p.TargetID})
	}
	return out
}

func (s *Service) Claim(ctx context.Context, itemID, moderatorID string) error {
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&ReviewItem{}).
		Where("id = ? AND status = ?", itemID, "pending").
		Updates(map[string]any{"status": "claimed", "claimed_by": moderatorID, "claimed_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return httpx.NewError("review.claimed", 409, "该项已被处理或领取")
	}
	return nil
}

func (s *Service) Decide(ctx context.Context, itemID, moderatorID, action, note string, until *time.Time) error {
	var item ReviewItem
	if err := s.db.WithContext(ctx).First(&item, "id = ?", itemID).Error; err != nil {
		return httpx.NotFound("review.not_found", "审核项不存在")
	}
	if item.Status == "decided" {
		return httpx.NewError("review.decided", 409, "该项已处理")
	}
	_, author, err := s.TargetInfo(ctx, item.ItemType, item.ItemID)
	if err != nil && action != "dismiss" {
		return err
	}

	switch action {
	case "dismiss":
		// no content action
	case "hide":
		err = s.apply(ctx, item, "hide")
	case "delete":
		err = s.apply(ctx, item, "delete")
	case "restore", "approve":
		err = s.apply(ctx, item, "restore")
	case "ban":
		if author == "" {
			return httpx.Invalid(map[string]string{"action": "无法定位作者"})
		}
		err = s.auth.SetStatusUntil(ctx, author, "banned", until)
	case "suspend":
		if author == "" {
			return httpx.Invalid(map[string]string{"action": "无法定位作者"})
		}
		err = s.auth.SetStatusUntil(ctx, author, "suspended", until)
	default:
		return httpx.Invalid(map[string]string{"action": "未知处理动作"})
	}
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&ReviewItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"status": "decided", "decision": action, "decided_by": moderatorID,
		"decided_at": now, "note": note, "claimed_by": moderatorID,
	}).Error; err != nil {
		return err
	}
	_ = s.db.WithContext(ctx).Model(&Flag{}).
		Where("target_type = ? AND target_id = ?", item.ItemType, item.ItemID).
		Updates(map[string]any{"status": "resolved", "handled_by": moderatorID, "handled_at": now, "resolution": action}).Error

	s.writeAudit(ctx, moderatorID, "moderation."+action, item.ItemType, item.ItemID, note)
	if action != "dismiss" {
		s.notifyDecision(ctx, item, moderatorID, action, note)
	}
	s.notifyReporters(ctx, item, moderatorID, action, note)
	return nil
}

func (s *Service) decisionTarget(ctx context.Context, item ReviewItem) (title, url string) {
	title, _, _ = s.TargetInfo(ctx, item.ItemType, item.ItemID)
	url = "/t/" + item.ItemID
	switch item.ItemType {
	case "post":
		if s.community != nil {
			if meta, err := s.community.PostMeta(ctx, item.ItemID); err == nil {
				url = "/t/" + meta.TopicID + "#post-" + item.ItemID
			}
		}
	case "message":
		if s.messages != nil {
			if meta, err := s.messages.MessageMeta(ctx, item.ItemID); err == nil {
				url = "/messages/" + meta.ConversationID
			}
		}
	}
	return title, url
}

func (s *Service) notifyReporters(ctx context.Context, item ReviewItem, moderatorID, action, note string) {
	if s.bus == nil {
		return
	}
	var reporters []string
	_ = s.db.WithContext(ctx).Model(&Flag{}).
		Where("target_type = ? AND target_id = ?", item.ItemType, item.ItemID).
		Distinct().Pluck("reporter_id", &reporters).Error
	if len(reporters) == 0 {
		return
	}
	title, url := s.decisionTarget(ctx, item)
	for _, reporter := range reporters {
		if reporter == "" || reporter == moderatorID {
			continue
		}
		payload, _ := json.Marshal(map[string]any{
			"recipient_id": reporter,
			"moderator_id": moderatorID,
			"target_type":  item.ItemType,
			"target_id":    item.ItemID,
			"title":        title,
			"url":          url,
			"action":       action,
			"note":         note,
		})
		_ = s.bus.Publish(ctx, events.NewEvent("moderation.report_resolved", "moderation", item.ItemID, payload))
	}
}

func (s *Service) notifyDecision(ctx context.Context, item ReviewItem, moderatorID, action, note string) {
	if s.bus == nil {
		return
	}
	title, author, _ := s.TargetInfo(ctx, item.ItemType, item.ItemID)
	_, url := s.decisionTarget(ctx, item)
	payload, _ := json.Marshal(map[string]any{
		"recipient_id": author,
		"moderator_id": moderatorID,
		"target_type":  item.ItemType,
		"target_id":    item.ItemID,
		"title":        title,
		"url":          url,
		"action":       action,
		"note":         note,
	})
	_ = s.bus.Publish(ctx, events.NewEvent("moderation.decided", "moderation", item.ItemID, payload))
}

func (s *Service) apply(ctx context.Context, item ReviewItem, action string) error {
	if s.moderator == nil {
		return httpx.NewError("moderation.unavailable", 503, "审核服务未就绪")
	}
	switch item.ItemType {
	case "topic":
		switch action {
		case "hide":
			return s.moderator.HideTopic(ctx, item.ItemID)
		case "delete":
			return s.moderator.DeleteTopic(ctx, item.ItemID)
		case "restore":
			return s.moderator.RestoreTopic(ctx, item.ItemID)
		}
	case "post":
		switch action {
		case "hide":
			return s.moderator.HidePost(ctx, item.ItemID)
		case "delete":
			return s.moderator.DeletePost(ctx, item.ItemID)
		case "restore":
			return s.moderator.RestorePost(ctx, item.ItemID)
		}
	case "message":
		return nil // messages are hidden from public view already
	}
	return httpx.Invalid(map[string]string{"action": "该对象不支持此操作"})
}

type AppealRow struct {
	Appeal Appeal
}

func (s *Service) SubmitAppeal(ctx context.Context, username, reason string) error {
	username = strings.TrimSpace(username)
	reason = strings.TrimSpace(reason)
	if username == "" {
		return httpx.Invalid(map[string]string{"username": "请输入用户名"})
	}
	if reason == "" {
		return httpx.Invalid(map[string]string{"reason": "请填写申诉说明"})
	}
	if len([]rune(reason)) > 1000 {
		return httpx.Invalid(map[string]string{"reason": "申诉说明最多 1000 个字符"})
	}
	if s.auth == nil {
		return httpx.NewError("appeal.unavailable", 503, "申诉暂不可用")
	}
	u, err := s.auth.GetByUsername(ctx, username)
	if err != nil {
		// Do not reveal whether the account exists.
		return nil
	}
	if u.Status != "suspended" && u.Status != "banned" {
		return nil
	}
	var n int64
	_ = s.db.WithContext(ctx).Model(&Appeal{}).
		Where("user_id = ? AND status = ?", u.ID, "pending").Count(&n).Error
	if n > 0 {
		return nil
	}
	a := Appeal{UserID: u.ID, Username: u.Username, Reason: reason, Status: "pending", CreatedAt: time.Now().UTC()}
	if err := s.db.WithContext(ctx).Create(&a).Error; err != nil {
		return err
	}
	s.writeAudit(ctx, u.ID, "appeal.create", "user", u.ID, "")
	return nil
}

func (s *Service) AppealStats(ctx context.Context) (AppealStats, error) {
	var out AppealStats
	for _, pair := range []struct {
		status string
		dest   *int64
	}{{"pending", &out.Pending}, {"approved", &out.Approved}, {"rejected", &out.Rejected}} {
		if err := s.db.WithContext(ctx).Model(&Appeal{}).Where("status = ?", pair.status).Count(pair.dest).Error; err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Service) ListAppeals(ctx context.Context, status string, page, perPage int) ([]AppealRow, int64, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	q := s.db.WithContext(ctx).Model(&Appeal{})
	if status == "" {
		q = q.Where("status = ?", "pending")
	} else {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Appeal
	if err := q.Order("created_at ASC").Offset((page - 1) * perPage).Limit(perPage).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]AppealRow, 0, len(rows))
	for _, a := range rows {
		out = append(out, AppealRow{Appeal: a})
	}
	return out, total, nil
}

func (s *Service) DecideAppeal(ctx context.Context, appealID, moderatorID, action, note string) error {
	var a Appeal
	if err := s.db.WithContext(ctx).First(&a, "id = ?", appealID).Error; err != nil {
		return httpx.NotFound("appeal.not_found", "申诉不存在")
	}
	if a.Status != "pending" {
		return httpx.NewError("appeal.decided", 409, "该申诉已处理")
	}
	status := ""
	switch action {
	case "approve":
		status = "approved"
		if err := s.auth.SetStatus(ctx, a.UserID, "active"); err != nil {
			return err
		}
	case "reject":
		status = "rejected"
	default:
		return httpx.Invalid(map[string]string{"action": "未知处理动作"})
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&Appeal{}).Where("id = ?", a.ID).Updates(map[string]any{
		"status": status, "handled_by": moderatorID, "handled_at": now, "note": strings.TrimSpace(note),
	}).Error; err != nil {
		return err
	}
	s.writeAudit(ctx, moderatorID, "appeal."+action, "user", a.UserID, note)
	if s.bus != nil {
		title := "账号申诉"
		url := "/appeal"
		if action == "approve" {
			url = "/"
		}
		payload, _ := json.Marshal(map[string]any{
			"recipient_id": a.UserID,
			"moderator_id": moderatorID,
			"username":     a.Username,
			"title":        title,
			"url":          url,
			"action":       action,
			"note":         note,
		})
		_ = s.bus.Publish(ctx, events.NewEvent("appeal.decided", "appeal", a.ID, payload))
	}
	return nil
}

func (s *Service) Check(ctx context.Context, text string) (action, matched string, err error) {
	var words []BannedWord
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&words).Error; err != nil {
		return "allow", "", err
	}
	lower := strings.ToLower(text)
	for _, w := range words {
		hit := false
		switch w.MatchType {
		case "regex":
			re, err := regexp.Compile(w.Word)
			if err == nil && re.MatchString(text) {
				hit = true
			}
		case "exact":
			hit = strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(w.Word))
		default:
			hit = strings.Contains(lower, strings.ToLower(w.Word))
		}
		if hit {
			action = w.Action
			if action == "" {
				action = "block"
			}
			return action, w.Word, nil
		}
	}
	return "allow", "", nil
}

func (s *Service) ListWords(ctx context.Context) ([]BannedWord, error) {
	var rows []BannedWord
	err := s.db.WithContext(ctx).Order("created_at DESC").Find(&rows).Error
	return rows, err
}

// ListWordsPage 分页返回敏感词及总数。
func (s *Service) ListWordsPage(ctx context.Context, limit, offset int) ([]BannedWord, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	base := s.db.WithContext(ctx).Model(&BannedWord{})
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []BannedWord
	err := base.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *Service) CreateWord(ctx context.Context, word, matchType, action, replacement string) error {
	word = strings.TrimSpace(word)
	if word == "" {
		return httpx.Invalid(map[string]string{"word": "敏感词不能为空"})
	}
	switch matchType {
	case "exact", "contains", "regex":
	default:
		return httpx.Invalid(map[string]string{"match_type": "无效匹配方式"})
	}
	switch action {
	case "block", "review":
	default:
		return httpx.Invalid(map[string]string{"action": "无效动作"})
	}
	if matchType == "regex" {
		if _, err := regexp.Compile(word); err != nil {
			return httpx.Invalid(map[string]string{"word": "正则表达式无效"})
		}
	}
	return s.db.WithContext(ctx).Create(&BannedWord{
		Word: word, MatchType: matchType, Action: action, Replacement: replacement, Enabled: true,
	}).Error
}

func (s *Service) DeleteWord(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&BannedWord{}, "id = ?", id).Error
}

func (s *Service) writeAudit(ctx context.Context, actor, action, targetType, targetID, reason string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Write(ctx, audit.Entry{
		ActorID: actor, Action: action, TargetType: targetType, TargetID: targetID, Reason: reason,
	})
}
