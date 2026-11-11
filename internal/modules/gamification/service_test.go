package gamification

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	cmod "goforum/internal/modules/community/contracts"
)

type fakeContent struct{}

func (fakeContent) TopicMeta(_ context.Context, id string) (*cmod.TopicMeta, error) {
	return &cmod.TopicMeta{ID: id, Title: "Topic " + id, AuthorID: "u1"}, nil
}

func (fakeContent) PostMeta(_ context.Context, id string) (*cmod.PostMeta, error) {
	return &cmod.PostMeta{ID: id, TopicID: "t1", TopicTitle: "Topic t1", AuthorID: "u2", PostNumber: 2}, nil
}

func (fakeContent) TopicMetaBatch(_ context.Context, ids []string) (map[string]cmod.TopicMeta, error) {
	out := make(map[string]cmod.TopicMeta, len(ids))
	for _, id := range ids {
		out[id] = cmod.TopicMeta{ID: id, Title: "Topic " + id, AuthorID: "u1"}
	}
	return out, nil
}

func (fakeContent) PostMetaBatch(_ context.Context, ids []string) (map[string]cmod.PostMeta, error) {
	out := make(map[string]cmod.PostMeta, len(ids))
	for _, id := range ids {
		out[id] = cmod.PostMeta{ID: id, TopicID: "t1", TopicTitle: "Topic t1", AuthorID: "u2", PostNumber: 2}
	}
	return out, nil
}

func setup(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "gamification.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := NewService(db)
	svc.Configure(nil, fakeContent{}, nil, nil)
	ctx := context.Background()
	if err := svc.EnsureDefaults(ctx); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	return svc
}

func TestPointsAwardSpendAndLevel(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()

	after, err := svc.Award(ctx, "u1", "topic.create", "post", "p1", "topic")
	if err != nil || after != 10 {
		t.Fatalf("award = %d/%v", after, err)
	}
	if _, err := svc.Award(ctx, "u1", "topic.create", "post", "p1", "topic"); err != nil {
		t.Fatalf("duplicate award: %v", err)
	}
	bal, _ := svc.Balance(ctx, "u1")
	if bal.Balance != 10 {
		t.Fatalf("duplicate changed balance: %d", bal.Balance)
	}
	for i := 2; i <= 5; i++ {
		_, _ = svc.Award(ctx, "u1", "topic.create", "post", "p"+string(rune('0'+i)), "topic")
	}
	bal, _ = svc.Balance(ctx, "u1")
	if bal.Balance != 50 || bal.Level != 1 {
		t.Fatalf("balance/level = %d/%d", bal.Balance, bal.Level)
	}
	if _, err := svc.Award(ctx, "u1", "topic.create", "post", "p6", "topic"); err != nil {
		t.Fatalf("limit award: %v", err)
	}
	bal, _ = svc.Balance(ctx, "u1")
	if bal.Balance != 50 {
		t.Fatalf("daily limit not enforced: %d", bal.Balance)
	}

	after, err = svc.Spend(ctx, "u1", 10, "test.spend", "test", "s1", "spend")
	if err != nil || after != 40 {
		t.Fatalf("spend = %d/%v", after, err)
	}
	if _, err := svc.Spend(ctx, "u1", 1000, "test.spend", "test", "s2", "too much"); err == nil {
		t.Fatal("overspend accepted")
	}
}

func TestCheckinAndBadge(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()

	points, streak, err := svc.Checkin(ctx, "u1")
	if err != nil || points != 5 || streak != 1 {
		t.Fatalf("checkin = %d/%d/%v", points, streak, err)
	}
	if _, _, err := svc.Checkin(ctx, "u1"); err == nil {
		t.Fatal("double checkin accepted")
	}
	status, _ := svc.CheckinStatus(ctx, "u1")
	if !status.Today || status.Streak != 1 {
		t.Fatalf("status = %+v", status)
	}
	if err := svc.GrantBadgeBySlug(ctx, "u1", "first-topic", ""); err != nil {
		t.Fatalf("grant badge: %v", err)
	}
	badges, _ := svc.BadgesOf(ctx, "u1")
	if len(badges) != 1 {
		t.Fatalf("badges = %d", len(badges))
	}
}

func TestInvites(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()

	inv, err := svc.CreateInvite(ctx, "u1", "")
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if err := svc.ValidateInvite(ctx, inv.Code, ""); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := svc.ConsumeInvite(ctx, inv.Code, "u2", ""); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if err := svc.ValidateInvite(ctx, inv.Code, ""); err == nil {
		t.Fatal("used invite still valid")
	}
	bal, _ := svc.Balance(ctx, "u1")
	if bal.Balance != 20 {
		t.Fatalf("invite reward = %d", bal.Balance)
	}
}

func TestLotteryAndBounty(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()

	if _, err := svc.Award(ctx, "u1", "invite.success", "invite", "i0", "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	l, err := svc.CreateLottery(ctx, "Test lottery", "Badge", "", 1, 5, 0, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create lottery: %v", err)
	}
	if err := svc.JoinLottery(ctx, "u1", l.ID); err != nil {
		t.Fatalf("join: %v", err)
	}
	if err := svc.JoinLottery(ctx, "u1", l.ID); err == nil {
		t.Fatal("double join accepted")
	}
	winners, err := svc.DrawLottery(ctx, l.ID, "admin")
	if err != nil || len(winners) != 1 || winners[0] != "u1" {
		t.Fatalf("draw = %v/%v", winners, err)
	}

	b, err := svc.CreateBounty(ctx, "u1", "t1", 10)
	if err != nil {
		t.Fatalf("create bounty: %v", err)
	}
	if b.Status != "funded" {
		t.Fatalf("bounty status = %s", b.Status)
	}
	if err := svc.SettleBounty(ctx, "u1", b.ID, "post1"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	u2bal, _ := svc.Balance(ctx, "u2")
	if u2bal.Balance != 10 {
		t.Fatalf("winner balance = %d", u2bal.Balance)
	}
}

func TestTipTransfersPoints(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	if err := svc.CreditExternal(ctx, "u1", 50, "test", "seed", "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.Tip(ctx, "u1", "u2", "topic-1", 20); err != nil {
		t.Fatalf("tip: %v", err)
	}
	b1, _ := svc.Balance(ctx, "u1")
	b2, _ := svc.Balance(ctx, "u2")
	if b1.Balance != 30 || b2.Balance != 20 {
		t.Fatalf("balances = %d/%d", b1.Balance, b2.Balance)
	}
	var tips int64
	svc.db.Model(&Tip{}).Where("from_user_id = ? AND to_user_id = ?", "u1", "u2").Count(&tips)
	if tips != 1 {
		t.Fatalf("tips = %d", tips)
	}
	if err := svc.Tip(ctx, "u1", "u1", "topic-1", 5); err == nil {
		t.Fatal("self tip accepted")
	}
	if err := svc.Tip(ctx, "u1", "u2", "topic-1", 1000); err == nil {
		t.Fatal("insufficient tip accepted")
	}
}
