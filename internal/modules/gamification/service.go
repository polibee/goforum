package gamification

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	cmod "goforum/internal/modules/community/contracts"
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
	audit     audit.Writer
	bus       events.Bus
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Configure(st *settings.Store, community cmod.Reader, auditWriter audit.Writer, bus events.Bus) {
	s.settings = st
	s.community = community
	s.audit = auditWriter
	s.bus = bus
}

var defaultRules = []PointRule{
	{Code: "topic.create", Name: "发布主题", Points: 10, DailyLimit: 5, Enabled: true},
	{Code: "post.create", Name: "发布回复", Points: 2, DailyLimit: 20, Enabled: true},
	{Code: "like.received", Name: "获得点赞", Points: 1, DailyLimit: 20, Enabled: true},
	{Code: "checkin", Name: "每日签到", Points: 5, DailyLimit: 1, Enabled: true},
	{Code: "invite.success", Name: "邀请成功", Points: 20, DailyLimit: 0, Enabled: true},
}

var defaultLevels = []Level{
	{Level: 0, Name: "Lv0 新手上路", MinPoints: 0, Color: "#94a3b8"},
	{Level: 1, Name: "Lv1 初来乍到", MinPoints: 50, Color: "#22c55e"},
	{Level: 2, Name: "Lv2 活跃成员", MinPoints: 200, Color: "#0ea5e9"},
	{Level: 3, Name: "Lv3 资深玩家", MinPoints: 500, Color: "#6366f1"},
	{Level: 4, Name: "Lv4 社区骨干", MinPoints: 1000, Color: "#a855f7"},
	{Level: 5, Name: "Lv5 荣誉会员", MinPoints: 3000, Color: "#f59e0b"},
	{Level: 6, Name: "Lv6 传奇", MinPoints: 10000, Color: "#ef4444"},
}

var defaultBadges = []Badge{
	{Slug: "first-topic", Name: "首次发帖", Description: "发布了第一个主题"},
	{Slug: "streak-30", Name: "坚持 30 天", Description: "连续签到 30 天"},
}

func (s *Service) EnsureDefaults(ctx context.Context) error {
	for i := range defaultRules {
		var n int64
		_ = s.db.WithContext(ctx).Model(&PointRule{}).Where("code = ?", defaultRules[i].Code).Count(&n).Error
		if n == 0 {
			if err := s.db.WithContext(ctx).Create(&defaultRules[i]).Error; err != nil {
				return err
			}
		}
	}
	names := make([]string, 0, len(defaultLevels))
	for _, d := range defaultLevels {
		names = append(names, d.Name)
	}
	// Older builds let GORM auto-increment the integer primary key, so the
	// level-0 row landed at level 1 and each restart added duplicate Lv0 rows.
	// Drop only those legacy duplicate rows, then upsert the canonical 0..6.
	_ = s.db.WithContext(ctx).Where("level > ? AND name IN ?", 6, names).Delete(&Level{}).Error
	for i := range defaultLevels {
		d := defaultLevels[i]
		var existing Level
		err := s.db.WithContext(ctx).Where("level = ?", d.Level).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := s.db.WithContext(ctx).Create(&d).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if existing.Name != d.Name || existing.MinPoints != d.MinPoints || existing.Color != d.Color {
			if err := s.db.WithContext(ctx).Model(&Level{}).Where("level = ?", d.Level).
				Updates(map[string]any{"name": d.Name, "min_points": d.MinPoints, "color": d.Color}).Error; err != nil {
				return err
			}
		}
	}
	for i := range defaultBadges {
		var n int64
		_ = s.db.WithContext(ctx).Model(&Badge{}).Where("slug = ?", defaultBadges[i].Slug).Count(&n).Error
		if n == 0 {
			if err := s.db.WithContext(ctx).Create(&defaultBadges[i]).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) Subscribe(bus events.Bus) error {
	if bus == nil {
		return nil
	}
	if err := bus.Subscribe("post.created", s.onPostCreated); err != nil {
		return err
	}
	return bus.Subscribe("reaction.added", s.onReaction)
}

type postPayload struct {
	PostID     string `json:"post_id"`
	ActorID    string `json:"actor_id"`
	PostNumber int    `json:"post_number"`
}

func (s *Service) onPostCreated(ctx context.Context, e events.Event) error {
	var p postPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.ActorID == "" || p.PostID == "" {
		return nil
	}
	code := "post.create"
	desc := "发布回复"
	if p.PostNumber == 1 {
		code = "topic.create"
		desc = "发布主题"
	}
	if _, err := s.Award(ctx, p.ActorID, code, "post", p.PostID, desc); err != nil {
		return err
	}
	if p.PostNumber == 1 {
		_ = s.GrantBadgeBySlug(ctx, p.ActorID, "first-topic", "")
	}
	return nil
}

type reactionPayload struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	ActorID    string `json:"actor_id"`
	AuthorID   string `json:"author_id"`
}

func (s *Service) onReaction(ctx context.Context, e events.Event) error {
	var p reactionPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.TargetType != "post" || p.AuthorID == "" || p.AuthorID == p.ActorID {
		return nil
	}
	_, err := s.Award(ctx, p.AuthorID, "like.received", "reaction", e.ID, "获得点赞")
	return err
}

func (s *Service) Award(ctx context.Context, userID, code, refType, refID, description string) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	var rule PointRule
	if err := s.db.WithContext(ctx).Where("code = ? AND enabled = ?", code, true).First(&rule).Error; err != nil {
		return 0, nil
	}
	if rule.Points <= 0 {
		return 0, nil
	}
	if rule.DailyLimit > 0 {
		var count int64
		start := time.Now().UTC().Truncate(24 * time.Hour)
		_ = s.db.WithContext(ctx).Model(&PointTransaction{}).
			Where("user_id = ? AND rule_code = ? AND amount > 0 AND created_at >= ?", userID, code, start).
			Count(&count).Error
		if int(count) >= rule.DailyLimit {
			return 0, nil
		}
	}
	return s.credit(ctx, userID, rule.Points, code, refType, refID, description)
}

func (s *Service) credit(ctx context.Context, userID string, amount int64, ruleCode, refType, refID, description string) (int64, error) {
	if amount <= 0 {
		return 0, nil
	}
	var after int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var bal PointBalance
		if err := tx.Where("user_id = ?", userID).First(&bal).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			bal = PointBalance{UserID: userID}
			if err := tx.Create(&bal).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		txn := PointTransaction{
			ID: id.New(), UserID: userID, Amount: amount, RuleCode: ruleCode,
			RefType: refType, RefID: refID, Description: description,
			CreatedAt: time.Now().UTC(),
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&txn)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // duplicate
		}
		now := time.Now().UTC()
		if err := tx.Model(&PointBalance{}).Where("user_id = ?", userID).Updates(map[string]any{
			"balance":      gorm.Expr("balance + ?", amount),
			"total_earned": gorm.Expr("total_earned + ?", amount),
			"updated_at":   now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).First(&bal).Error; err != nil {
			return err
		}
		after = bal.Balance
		if err := tx.Model(&PointTransaction{}).Where("id = ?", txn.ID).Update("balance_after", after).Error; err != nil {
			return err
		}
		return tx.Model(&PointBalance{}).Where("user_id = ?", userID).
			Update("level", s.levelForPoints(tx, after)).Error
	})
	return after, err
}

func (s *Service) Spend(ctx context.Context, userID string, amount int64, ruleCode, refType, refID, description string) (int64, error) {
	if amount <= 0 {
		return 0, nil
	}
	var after int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var bal PointBalance
		if err := tx.Where("user_id = ?", userID).First(&bal).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return httpx.Invalid(map[string]string{"points": "积分不足"})
		} else if err != nil {
			return err
		}
		txn := PointTransaction{
			ID: id.New(), UserID: userID, Amount: -amount, RuleCode: ruleCode,
			RefType: refType, RefID: refID, Description: description,
			CreatedAt: time.Now().UTC(),
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&txn)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // duplicate
		}
		now := time.Now().UTC()
		upd := tx.Model(&PointBalance{}).Where("user_id = ? AND balance >= ?", userID, amount).Updates(map[string]any{
			"balance":     gorm.Expr("balance - ?", amount),
			"total_spent": gorm.Expr("total_spent + ?", amount),
			"updated_at":  now,
		})
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected == 0 {
			return httpx.Invalid(map[string]string{"points": "积分不足"})
		}
		if err := tx.Where("user_id = ?", userID).First(&bal).Error; err != nil {
			return err
		}
		after = bal.Balance
		if err := tx.Model(&PointTransaction{}).Where("id = ?", txn.ID).Update("balance_after", after).Error; err != nil {
			return err
		}
		return tx.Model(&PointBalance{}).Where("user_id = ?", userID).
			Update("level", s.levelForPoints(tx, after)).Error
	})
	return after, err
}

func (s *Service) levelForPoints(tx *gorm.DB, points int64) int {
	var level Level
	if err := tx.Where("min_points <= ?", points).Order("min_points DESC").First(&level).Error; err == nil {
		return level.Level
	}
	return 0
}

func (s *Service) Balance(ctx context.Context, userID string) (*PointBalance, error) {
	var bal PointBalance
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&bal).Error
	if err == nil {
		return &bal, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// Two concurrent requests can both miss the row and then race on create.
	// Insert with ON CONFLICT DO NOTHING and read back, so the losing caller
	// gets the winner's row instead of an error (or a zero-value balance).
	bal = PointBalance{UserID: userID}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&bal).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&bal).Error; err != nil {
		return nil, err
	}
	return &bal, nil
}

func (s *Service) BalanceValue(ctx context.Context, userID string) (int64, error) {
	b, err := s.Balance(ctx, userID)
	if err != nil {
		return 0, err
	}
	return b.Balance, nil
}

func (s *Service) Transactions(ctx context.Context, userID string, limit, offset int) ([]PointTransaction, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var total int64
	if err := s.db.WithContext(ctx).Model(&PointTransaction{}).Where("user_id = ?", userID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []PointTransaction
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

type CheckinStatus struct {
	Today    bool
	Streak   int
	Points   int64
	Balance  *PointBalance
	LastDate string
}

func (s *Service) CheckinStatus(ctx context.Context, userID string) (*CheckinStatus, error) {
	date := time.Now().UTC().Format("2006-01-02")
	var last Checkin
	_ = s.db.WithContext(ctx).Where("user_id = ?", userID).Order("checkin_date DESC").First(&last).Error
	bal, err := s.Balance(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &CheckinStatus{
		Today: last.CheckinDate == date, Streak: last.Streak,
		Points: last.Points, Balance: bal, LastDate: last.CheckinDate,
	}, nil
}

func (s *Service) Checkin(ctx context.Context, userID string) (int64, int, error) {
	now := time.Now().UTC()
	date := now.Format("2006-01-02")
	var existing Checkin
	if err := s.db.WithContext(ctx).Where("user_id = ? AND checkin_date = ?", userID, date).First(&existing).Error; err == nil {
		return 0, existing.Streak, httpx.NewError("checkin.done", 409, "今天已经签到过了")
	}
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	var prev Checkin
	streak := 1
	if err := s.db.WithContext(ctx).Where("user_id = ? AND checkin_date = ?", userID, yesterday).First(&prev).Error; err == nil {
		streak = prev.Streak + 1
	}
	points, err := s.Award(ctx, userID, "checkin", "checkin", date, "每日签到")
	if err != nil {
		return 0, streak, err
	}
	ci := &Checkin{ID: id.New(), UserID: userID, CheckinDate: date, Streak: streak, Points: points, CreatedAt: now}
	if err := s.db.WithContext(ctx).Create(ci).Error; err != nil {
		return 0, streak, err
	}
	if streak >= 30 {
		_ = s.GrantBadgeBySlug(ctx, userID, "streak-30", "")
	}
	return points, streak, nil
}

func (s *Service) ListBadges(ctx context.Context) ([]Badge, error) {
	var rows []Badge
	err := s.db.WithContext(ctx).Order("created_at ASC").Find(&rows).Error
	return rows, err
}

func (s *Service) BadgesOf(ctx context.Context, userID string) ([]Badge, error) {
	var rows []Badge
	err := s.db.WithContext(ctx).Table("badges AS b").
		Select("b.*").Joins("JOIN user_badges ub ON ub.badge_id = b.id").
		Where("ub.user_id = ?", userID).Order("ub.awarded_at DESC").Find(&rows).Error
	return rows, err
}

func (s *Service) UserBadges(ctx context.Context, userID string) ([]cmod.BadgeView, error) {
	out := make([]cmod.BadgeView, 0)
	err := s.db.WithContext(ctx).Table("user_badges AS ub").
		Select("b.name, b.description, b.icon, ub.awarded_at").
		Joins("JOIN badges b ON b.id = ub.badge_id").
		Where("ub.user_id = ? AND b.hidden = ?", userID, false).
		Order("ub.awarded_at DESC").Scan(&out).Error
	return out, err
}

func (s *Service) GrantBadgeBySlug(ctx context.Context, userID, slug, actorID string) error {
	var badge Badge
	if err := s.db.WithContext(ctx).Where("slug = ?", slug).First(&badge).Error; err != nil {
		return nil
	}
	return s.GrantBadge(ctx, userID, badge.ID, actorID)
}

func (s *Service) GrantBadge(ctx context.Context, userID, badgeID, actorID string) error {
	ub := UserBadge{ID: id.New(), UserID: userID, BadgeID: badgeID, AwardedBy: actorID, AwardedAt: time.Now().UTC()}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&ub).Error
}

func (s *Service) CreateBadge(ctx context.Context, badge *Badge) error {
	if strings.TrimSpace(badge.Name) == "" || strings.TrimSpace(badge.Slug) == "" {
		return httpx.Invalid(map[string]string{"badge": "名称与 slug 不能为空"})
	}
	return s.db.WithContext(ctx).Create(badge).Error
}

func (s *Service) CreateInvite(ctx context.Context, inviterID, email string) (*Invite, error) {
	quota := 3
	if s.settings != nil {
		if v := s.settings.GetInt("site.registration.invite_quota"); v > 0 {
			quota = int(v)
		}
	}
	var active int64
	_ = s.db.WithContext(ctx).Model(&Invite{}).
		Where("inviter_id = ? AND status = ? AND expires_at > ?", inviterID, "active", time.Now().UTC()).
		Count(&active).Error
	if int(active) >= quota {
		return nil, httpx.Invalid(map[string]string{"invite": "邀请名额已用完"})
	}
	code := randomCode()
	inv := &Invite{
		Code: code, InviterID: inviterID, Email: strings.TrimSpace(email),
		Status: "active", ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour), CreatedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(inv).Error; err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *Service) ValidateInvite(ctx context.Context, code, email string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return httpx.Invalid(map[string]string{"invite_code": "请输入邀请码"})
	}
	var inv Invite
	if err := s.db.WithContext(ctx).Where("code = ?", code).First(&inv).Error; err != nil {
		return httpx.Invalid(map[string]string{"invite_code": "邀请码无效"})
	}
	if inv.Status != "active" || time.Now().UTC().After(inv.ExpiresAt) {
		return httpx.Invalid(map[string]string{"invite_code": "邀请码已失效"})
	}
	if inv.Email != "" && !strings.EqualFold(inv.Email, strings.TrimSpace(email)) {
		return httpx.Invalid(map[string]string{"invite_code": "邀请码与邮箱不匹配"})
	}
	return nil
}

func (s *Service) ConsumeInvite(ctx context.Context, code, userID, email string) error {
	if err := s.ValidateInvite(ctx, code, email); err != nil {
		return err
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	var inv Invite
	if err := s.db.WithContext(ctx).Where("code = ?", code).First(&inv).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&Invite{}).
		Where("id = ? AND status = ?", inv.ID, "active").
		Updates(map[string]any{"status": "used", "used_by": userID, "used_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return httpx.Invalid(map[string]string{"invite_code": "邀请码已被使用"})
	}
	_, _ = s.Award(ctx, inv.InviterID, "invite.success", "invite", inv.ID, "邀请成功")
	return nil
}

func (s *Service) InvitesBy(ctx context.Context, inviterID string) ([]Invite, error) {
	var rows []Invite
	err := s.db.WithContext(ctx).Where("inviter_id = ?", inviterID).Order("created_at DESC").Find(&rows).Error
	return rows, err
}

// InvitesByPage 分页返回某用户发出的邀请码及总数。
func (s *Service) InvitesByPage(ctx context.Context, inviterID string, limit, offset int) ([]Invite, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	base := s.db.WithContext(ctx).Model(&Invite{}).Where("inviter_id = ?", inviterID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Invite
	err := base.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *Service) AllInvites(ctx context.Context) ([]Invite, error) {
	var rows []Invite
	err := s.db.WithContext(ctx).Order("created_at DESC").Limit(200).Find(&rows).Error
	return rows, err
}

type LotteryView struct {
	Lottery     Lottery
	EntryCount  int64
	UserEntered bool
	Winners     []string
}

func (s *Service) CreateLottery(ctx context.Context, title, prize, description string, winners int, cost int64, minLevel int, endsAt time.Time) (*Lottery, error) {
	if strings.TrimSpace(title) == "" || strings.TrimSpace(prize) == "" {
		return nil, httpx.Invalid(map[string]string{"lottery": "标题与奖品不能为空"})
	}
	if winners < 1 {
		winners = 1
	}
	l := &Lottery{
		Title: strings.TrimSpace(title), Prize: strings.TrimSpace(prize),
		Description: strings.TrimSpace(description), WinnerCount: winners,
		EntryCost: cost, MinLevel: minLevel, StartsAt: time.Now().UTC(),
		EndsAt: endsAt, Status: "open", CreatedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(l).Error; err != nil {
		return nil, err
	}
	return l, nil
}

func (s *Service) ListLotteries(ctx context.Context) ([]Lottery, error) {
	var rows []Lottery
	err := s.db.WithContext(ctx).Order("created_at DESC").Limit(100).Find(&rows).Error
	return rows, err
}

func (s *Service) GetLottery(ctx context.Context, id, userID string) (*LotteryView, error) {
	var l Lottery
	if err := s.db.WithContext(ctx).First(&l, "id = ?", id).Error; err != nil {
		return nil, httpx.NotFound("lottery.not_found", "抽奖不存在")
	}
	v := &LotteryView{Lottery: l}
	_ = s.db.WithContext(ctx).Model(&LotteryEntry{}).Where("lottery_id = ?", id).Count(&v.EntryCount).Error
	if userID != "" {
		var n int64
		_ = s.db.WithContext(ctx).Model(&LotteryEntry{}).Where("lottery_id = ? AND user_id = ?", id, userID).Count(&n).Error
		v.UserEntered = n > 0
	}
	if l.WinnersJSON != "" {
		_ = json.Unmarshal([]byte(l.WinnersJSON), &v.Winners)
	}
	return v, nil
}

func (s *Service) JoinLottery(ctx context.Context, userID, lotteryID string) error {
	var l Lottery
	if err := s.db.WithContext(ctx).First(&l, "id = ?", lotteryID).Error; err != nil {
		return httpx.NotFound("lottery.not_found", "抽奖不存在")
	}
	if l.Status != "open" || time.Now().UTC().After(l.EndsAt) {
		return httpx.Invalid(map[string]string{"lottery": "抽奖已结束"})
	}
	bal, _ := s.Balance(ctx, userID)
	if bal.Level < l.MinLevel {
		return httpx.Forbidden("lottery.level", "等级不足")
	}
	var n int64
	_ = s.db.WithContext(ctx).Model(&LotteryEntry{}).Where("lottery_id = ? AND user_id = ?", lotteryID, userID).Count(&n).Error
	if n > 0 {
		return httpx.NewError("lottery.joined", 409, "已经参与过了")
	}
	if l.EntryCost > 0 {
		if _, err := s.Spend(ctx, userID, l.EntryCost, "lottery.entry", "lottery", lotteryID, "参与抽奖"); err != nil {
			return err
		}
	}
	entry := LotteryEntry{ID: id.New(), LotteryID: lotteryID, UserID: userID, CreatedAt: time.Now().UTC()}
	if err := s.db.WithContext(ctx).Create(&entry).Error; err != nil {
		if l.EntryCost > 0 {
			_, _ = s.credit(ctx, userID, l.EntryCost, "lottery.refund", "lottery", lotteryID, "抽奖参与失败退回")
		}
		return err
	}
	return nil
}

func (s *Service) DrawLottery(ctx context.Context, lotteryID, actorID string) ([]string, error) {
	var l Lottery
	if err := s.db.WithContext(ctx).First(&l, "id = ?", lotteryID).Error; err != nil {
		return nil, httpx.NotFound("lottery.not_found", "抽奖不存在")
	}
	if l.Status == "drawn" {
		return nil, httpx.NewError("lottery.drawn", 409, "已经开奖")
	}
	var entries []LotteryEntry
	if err := s.db.WithContext(ctx).Where("lottery_id = ?", lotteryID).Find(&entries).Error; err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, httpx.Invalid(map[string]string{"lottery": "没有参与者"})
	}
	seedBytes := make([]byte, 16)
	_, _ = rand.Read(seedBytes)
	seed := hex.EncodeToString(seedBytes)
	seedInt, _ := new(big.Int).SetString(seed, 16)
	r := newRand(seedInt)
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	for i := len(order) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		order[i], order[j] = order[j], order[i]
	}
	count := l.WinnerCount
	if count > len(entries) {
		count = len(entries)
	}
	winners := make([]string, 0, count)
	for i := 0; i < count; i++ {
		winners = append(winners, entries[order[i]].UserID)
	}
	payload, _ := json.Marshal(winners)
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&Lottery{}).Where("id = ?", lotteryID).Updates(map[string]any{
		"status": "drawn", "seed": seed, "winners_json": string(payload), "drawn_at": now,
	}).Error; err != nil {
		return nil, err
	}
	s.writeAudit(ctx, actorID, "lottery.draw", "lottery", lotteryID, seed)
	return winners, nil
}

func (s *Service) CreateBounty(ctx context.Context, ownerID, topicID string, amount int64) (*Bounty, error) {
	if amount <= 0 {
		return nil, httpx.Invalid(map[string]string{"amount": "悬赏金额必须大于 0"})
	}
	if s.community == nil {
		return nil, httpx.NewError("bounty.unavailable", 503, "悬赏服务未就绪")
	}
	meta, err := s.community.TopicMeta(ctx, topicID)
	if err != nil {
		return nil, err
	}
	if meta.AuthorID != ownerID {
		return nil, httpx.Forbidden("bounty.owner", "只有主题作者可以发布悬赏")
	}
	var n int64
	_ = s.db.WithContext(ctx).Model(&Bounty{}).Where("topic_id = ?", topicID).Count(&n).Error
	if n > 0 {
		return nil, httpx.NewError("bounty.exists", 409, "该主题已有悬赏")
	}
	b := &Bounty{ID: id.New(), TopicID: topicID, UserID: ownerID, Amount: amount, Status: "pending", CreatedAt: time.Now().UTC()}
	if err := s.db.WithContext(ctx).Create(b).Error; err != nil {
		return nil, err
	}
	if _, err := s.Spend(ctx, ownerID, amount, "bounty.freeze", "bounty", b.ID, "悬赏冻结"); err != nil {
		_ = s.db.WithContext(ctx).Delete(&Bounty{}, "id = ?", b.ID).Error
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(&Bounty{}).Where("id = ?", b.ID).Update("status", "funded").Error; err != nil {
		return nil, err
	}
	b.Status = "funded"
	return b, nil
}

func (s *Service) SettleBounty(ctx context.Context, ownerID, bountyID, postID string) error {
	var b Bounty
	if err := s.db.WithContext(ctx).First(&b, "id = ?", bountyID).Error; err != nil {
		return httpx.NotFound("bounty.not_found", "悬赏不存在")
	}
	if b.UserID != ownerID {
		return httpx.Forbidden("bounty.owner", "只有发布者可以结算")
	}
	if b.Status != "funded" {
		return httpx.Invalid(map[string]string{"bounty": "悬赏状态不可结算"})
	}
	meta, err := s.community.PostMeta(ctx, postID)
	if err != nil {
		return err
	}
	if meta.TopicID != b.TopicID {
		return httpx.Invalid(map[string]string{"post": "回复不属于该主题"})
	}
	// Claim the bounty first (compare-and-set). Two concurrent settle/refund
	// calls would otherwise both credit points, since they use different
	// rule codes and therefore bypass the points idempotency key.
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&Bounty{}).
		Where("id = ? AND status = ?", b.ID, "funded").
		Updates(map[string]any{"status": "settled", "accepted_post_id": postID, "settled_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return httpx.Invalid(map[string]string{"bounty": "悬赏状态已变化"})
	}
	if _, err := s.credit(ctx, meta.AuthorID, b.Amount, "bounty.reward", "bounty", b.ID, "悬赏奖励"); err != nil {
		// Roll the claim back so the owner can retry.
		_ = s.db.WithContext(ctx).Model(&Bounty{}).Where("id = ? AND status = ?", b.ID, "settled").
			Updates(map[string]any{"status": "funded", "accepted_post_id": "", "settled_at": nil}).Error
		return err
	}
	s.writeAudit(ctx, ownerID, "bounty.settle", "bounty", b.ID, "")
	return nil
}

func (s *Service) RefundBounty(ctx context.Context, actorID, bountyID string, isAdmin bool) error {
	var b Bounty
	if err := s.db.WithContext(ctx).First(&b, "id = ?", bountyID).Error; err != nil {
		return httpx.NotFound("bounty.not_found", "悬赏不存在")
	}
	if !isAdmin && b.UserID != actorID {
		return httpx.Forbidden("bounty.owner", "只有发布者或管理员可以退款")
	}
	if b.Status != "funded" {
		return httpx.Invalid(map[string]string{"bounty": "悬赏状态不可退款"})
	}
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&Bounty{}).
		Where("id = ? AND status = ?", b.ID, "funded").
		Updates(map[string]any{"status": "refunded", "refunded_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return httpx.Invalid(map[string]string{"bounty": "悬赏状态已变化"})
	}
	if _, err := s.credit(ctx, b.UserID, b.Amount, "bounty.refund", "bounty", b.ID, "悬赏退款"); err != nil {
		_ = s.db.WithContext(ctx).Model(&Bounty{}).Where("id = ? AND status = ?", b.ID, "refunded").
			Updates(map[string]any{"status": "funded", "refunded_at": nil}).Error
		return err
	}
	s.writeAudit(ctx, actorID, "bounty.refund", "bounty", b.ID, "")
	return nil
}

func (s *Service) GetBounty(ctx context.Context, id string) (*Bounty, error) {
	var b Bounty
	if err := s.db.WithContext(ctx).First(&b, "id = ?", id).Error; err != nil {
		return nil, httpx.NotFound("bounty.not_found", "悬赏不存在")
	}
	return &b, nil
}

func (s *Service) ListBounties(ctx context.Context, userID string) ([]Bounty, error) {
	q := s.db.WithContext(ctx).Model(&Bounty{})
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	var rows []Bounty
	err := q.Order("created_at DESC").Limit(100).Find(&rows).Error
	return rows, err
}

func (s *Service) CreditExternal(ctx context.Context, userID string, amount int64, refType, refID, description string) error {
	_, err := s.credit(ctx, userID, amount, "billing.credit", refType, refID, description)
	return err
}

func (s *Service) SpendExternal(ctx context.Context, userID string, amount int64, refType, refID, description string) error {
	_, err := s.Spend(ctx, userID, amount, "billing.spend", refType, refID, description)
	return err
}

func (s *Service) ListRules(ctx context.Context) ([]PointRule, error) {
	var rows []PointRule
	err := s.db.WithContext(ctx).Order("created_at ASC").Find(&rows).Error
	return rows, err
}

func (s *Service) UpdateRule(ctx context.Context, id string, points int64, dailyLimit int, enabled bool, actorID string) error {
	if points < 0 || dailyLimit < 0 {
		return httpx.Invalid(map[string]string{"points": "数值不能为负"})
	}
	if err := s.db.WithContext(ctx).Model(&PointRule{}).Where("id = ?", id).Updates(map[string]any{
		"points": points, "daily_limit": dailyLimit, "enabled": enabled,
	}).Error; err != nil {
		return err
	}
	s.writeAudit(ctx, actorID, "points.rule_update", "point_rule", id, "")
	return nil
}

func (s *Service) ListLevels(ctx context.Context) ([]Level, error) {
	var rows []Level
	err := s.db.WithContext(ctx).Order("level ASC").Find(&rows).Error
	return rows, err
}

func (s *Service) UpdateLevel(ctx context.Context, level int, minPoints int64, name, color, actorID string) error {
	if minPoints < 0 {
		return httpx.Invalid(map[string]string{"min_points": "不能为负"})
	}
	if err := s.db.WithContext(ctx).Model(&Level{}).Where("level = ?", level).Updates(map[string]any{
		"min_points": minPoints, "name": name, "color": color,
	}).Error; err != nil {
		return err
	}
	s.writeAudit(ctx, actorID, "points.level_update", "level", "", "")
	return nil
}

func randomCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

// newRand keeps lottery drawing reproducible from the recorded seed.
type seededRand struct{ state *big.Int }

func newRand(seed *big.Int) *seededRand {
	if seed == nil {
		seed = big.NewInt(1)
	}
	return &seededRand{state: seed}
}

func (r *seededRand) Intn(n int) int {
	r.state.Mul(r.state, big.NewInt(6364136223846793005))
	r.state.Add(r.state, big.NewInt(1442695040888963407))
	r.state.Mod(r.state, big.NewInt(1<<62))
	return int(new(big.Int).Mod(r.state, big.NewInt(int64(n))).Int64())
}

func (s *Service) writeAudit(ctx context.Context, actor, action, targetType, targetID, reason string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Write(ctx, audit.Entry{
		ActorID: actor, Action: action, TargetType: targetType, TargetID: targetID, Reason: reason,
	})
}

// Tip transfers points from one user to another and records the donation.
func (s *Service) Tip(ctx context.Context, fromUserID, toUserID, topicID string, points int64) error {
	if fromUserID == "" || toUserID == "" || fromUserID == toUserID {
		return httpx.Invalid(map[string]string{"user": "不能捐赠给自己"})
	}
	if points <= 0 || points > 10000 {
		return httpx.Invalid(map[string]string{"points": "捐赠积分需在 1–10000 之间"})
	}
	tipID := id.New()
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var fromBal PointBalance
		if err := tx.Where("user_id = ?", fromUserID).First(&fromBal).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return httpx.Invalid(map[string]string{"points": "积分不足"})
		} else if err != nil {
			return err
		}
		upd := tx.Model(&PointBalance{}).Where("user_id = ? AND balance >= ?", fromUserID, points).Updates(map[string]any{
			"balance":     gorm.Expr("balance - ?", points),
			"total_spent": gorm.Expr("total_spent + ?", points),
			"updated_at":  now,
		})
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected == 0 {
			return httpx.Invalid(map[string]string{"points": "积分不足"})
		}
		if err := tx.Create(&PointTransaction{
			ID: id.New(), UserID: fromUserID, Amount: -points, RuleCode: "tip.spend",
			RefType: "tip", RefID: tipID, Description: "捐赠积分", CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", fromUserID).First(&fromBal).Error; err != nil {
			return err
		}
		if err := tx.Model(&PointTransaction{}).Where("user_id = ? AND rule_code = ? AND ref_id = ?", fromUserID, "tip.spend", tipID).
			Update("balance_after", fromBal.Balance).Error; err != nil {
			return err
		}
		if err := tx.Model(&PointBalance{}).Where("user_id = ?", fromUserID).
			Update("level", s.levelForPoints(tx, fromBal.Balance)).Error; err != nil {
			return err
		}

		var toBal PointBalance
		if err := tx.Where("user_id = ?", toUserID).First(&toBal).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			toBal = PointBalance{UserID: toUserID}
			if err := tx.Create(&toBal).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := tx.Model(&PointBalance{}).Where("user_id = ?", toUserID).Updates(map[string]any{
			"balance":      gorm.Expr("balance + ?", points),
			"total_earned": gorm.Expr("total_earned + ?", points),
			"updated_at":   now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&PointTransaction{
			ID: id.New(), UserID: toUserID, Amount: points, RuleCode: "tip.receive",
			RefType: "tip", RefID: tipID, Description: "收到捐赠", CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", toUserID).First(&toBal).Error; err != nil {
			return err
		}
		if err := tx.Model(&PointTransaction{}).Where("user_id = ? AND rule_code = ? AND ref_id = ?", toUserID, "tip.receive", tipID).
			Update("balance_after", toBal.Balance).Error; err != nil {
			return err
		}
		if err := tx.Model(&PointBalance{}).Where("user_id = ?", toUserID).
			Update("level", s.levelForPoints(tx, toBal.Balance)).Error; err != nil {
			return err
		}
		return tx.Create(&Tip{ID: tipID, FromUserID: fromUserID, ToUserID: toUserID, TopicID: topicID, Points: points, CreatedAt: now}).Error
	})
	if err != nil {
		return err
	}
	if s.bus != nil {
		_ = s.bus.Publish(ctx, events.NewEvent("tip.created", "tip", tipID, []byte(`{"from":"`+fromUserID+`","to":"`+toUserID+`","points":`+strconv.FormatInt(points, 10)+`}`)))
	}
	return nil
}
