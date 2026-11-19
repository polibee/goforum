package billing

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setup(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "billing.db")), &gorm.Config{})
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
	svc.Configure(nil, nil, nil, nil, nil)
	if err := svc.EnsureDefaults(context.Background()); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	return svc
}

func channelID(t *testing.T, svc *Service, driver string) string {
	t.Helper()
	var ch PaymentChannel
	if err := svc.db.Where("driver = ?", driver).First(&ch).Error; err != nil {
		t.Fatalf("channel %s: %v", driver, err)
	}
	return ch.ID
}

func TestPointsPackPaymentIsIdempotent(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	p := &Product{Type: "points_pack", Name: "1000 积分", Slug: "points-1000",
		PriceMinor: 1000, Currency: "CNY", PointsAmount: 1000, Active: true}
	if err := svc.CreateProduct(ctx, p); err != nil {
		t.Fatalf("product: %v", err)
	}
	order, params, err := svc.CreateOrder(ctx, "u1", p.ID, channelID(t, svc, "alipay"), "", "127.0.0.1")
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if order.Status != "pending" || params.Type != "redirect" {
		t.Fatalf("order status/params = %s/%s", order.Status, params.Type)
	}
	for i := 0; i < 2; i++ {
		if err := svc.PayOrder(ctx, order.OutTradeNo, "alipay", "trade-1", order.AmountMinor); err != nil {
			t.Fatalf("pay %d: %v", i, err)
		}
	}
	var txnCount int64
	svc.db.Model(&PaymentTransaction{}).Where("order_id = ?", order.ID).Count(&txnCount)
	if txnCount != 1 {
		t.Fatalf("payment txns = %d, want 1", txnCount)
	}
	var revenue LedgerAccount
	if err := svc.db.Where("owner_type = ? AND owner_id = ? AND kind = ?", "system", "revenue", "revenue").
		First(&revenue).Error; err != nil {
		t.Fatalf("revenue account: %v", err)
	}
	if revenue.Balance != 1000 {
		t.Fatalf("revenue balance = %d, want 1000", revenue.Balance)
	}
	var ledgers int64
	svc.db.Model(&LedgerTransaction{}).Where("ref_id = ?", order.ID).Count(&ledgers)
	if ledgers != 1 {
		t.Fatalf("ledger txns = %d, want 1", ledgers)
	}
}

func TestVIPOrderBalancePaymentSubscriptionAndRefund(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	p := &Product{Type: "vip_plan", Name: "VIP 30 天", Slug: "vip-30",
		PriceMinor: 3000, Currency: "CNY", DurationDays: 30, Active: true}
	if err := svc.CreateProduct(ctx, p); err != nil {
		t.Fatalf("product: %v", err)
	}
	if err := svc.AdjustBalance(ctx, "u1", 5000, "admin", "test topup"); err != nil {
		t.Fatalf("topup: %v", err)
	}
	order, _, err := svc.CreateOrder(ctx, "u1", p.ID, channelID(t, svc, "balance"), "", "")
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if order.Status != "paid" {
		t.Fatalf("balance order status = %s", order.Status)
	}
	bal, _ := svc.UserBalance(ctx, "u1")
	if bal != 2000 {
		t.Fatalf("balance = %d, want 2000", bal)
	}
	var sub Subscription
	if err := svc.db.Where("user_id = ? AND status = ?", "u1", "active").First(&sub).Error; err != nil {
		t.Fatalf("subscription: %v", err)
	}
	if sub.EndsAt.Sub(sub.StartsAt) < 29*24*time.Hour {
		t.Fatalf("subscription duration = %v", sub.EndsAt.Sub(sub.StartsAt))
	}
	var ent Entitlement
	if err := svc.db.Where("user_id = ? AND resource_type = ?", "u1", "vip").First(&ent).Error; err != nil {
		t.Fatalf("entitlement: %v", err)
	}
	if err := svc.RefundOrder(ctx, order.ID, "admin", "test refund"); err != nil {
		t.Fatalf("refund: %v", err)
	}
	bal, _ = svc.UserBalance(ctx, "u1")
	if bal != 5000 {
		t.Fatalf("balance after refund = %d, want 5000", bal)
	}
	if err := svc.db.First(&sub, "id = ?", sub.ID).Error; err != nil {
		t.Fatalf("reload sub: %v", err)
	}
	if sub.Status != "canceled" {
		t.Fatalf("sub status = %s", sub.Status)
	}
	if err := svc.db.First(&ent, "id = ?", ent.ID).Error; err != nil {
		t.Fatalf("reload entitlement: %v", err)
	}
	if ent.RevokedAt == nil {
		t.Fatal("entitlement not revoked")
	}
}

func TestCouponAndZeroAmountOrder(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	p := &Product{Type: "vip_plan", Name: "VIP", Slug: "vip-coupon",
		PriceMinor: 1000, Currency: "CNY", DurationDays: 7, Active: true}
	_ = svc.CreateProduct(ctx, p)
	if err := svc.CreateCoupon(ctx, &Coupon{
		Code: "free", Type: "fixed", Value: 1000, Total: 10, PerUserLimit: 1,
		StartsAt: time.Now().UTC().Add(-time.Hour), EndsAt: time.Now().UTC().Add(time.Hour), Enabled: true,
	}); err != nil {
		t.Fatalf("coupon: %v", err)
	}
	order, _, err := svc.CreateOrder(ctx, "u1", p.ID, channelID(t, svc, "alipay"), "free", "")
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if order.AmountMinor != 0 || order.DiscountMinor != 1000 {
		t.Fatalf("amount/discount = %d/%d", order.AmountMinor, order.DiscountMinor)
	}
	if order.Status != "paid" {
		t.Fatalf("zero order status = %s", order.Status)
	}
	if _, _, err := svc.CreateOrder(ctx, "u1", p.ID, channelID(t, svc, "alipay"), "free", ""); err == nil {
		t.Fatal("expected per-user coupon limit error")
	}
}

func TestReconciliationRecordsDiff(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	p := &Product{Type: "points_pack", Name: "P", Slug: "p-rec", PriceMinor: 500, Active: true}
	_ = svc.CreateProduct(ctx, p)
	order, _, err := svc.CreateOrder(ctx, "u1", p.ID, channelID(t, svc, "wechat"), "", "")
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if err := svc.PayOrder(ctx, order.OutTradeNo, "wechat", "wx-1", order.AmountMinor); err != nil {
		t.Fatalf("pay: %v", err)
	}
	run, err := svc.RunReconciliation(ctx, "wechat", time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if run.ID == "" || run.Status != "completed" {
		t.Fatalf("run = %+v", run)
	}
	if run.LocalAmount != 500 || run.ChannelAmount != 500 || run.DiffCount != 0 {
		t.Fatalf("run amounts = %+v", run)
	}
	var diffs int64
	svc.db.Model(&ReconciliationDiff{}).Where("run_id = ?", run.ID).Count(&diffs)
	if diffs != 0 {
		t.Fatalf("diffs = %d", diffs)
	}
}

func TestExternalRefundDoesNotCreditWallet(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	p := &Product{Type: "vip_plan", Name: "VIP", Slug: "vip-refund",
		PriceMinor: 1200, Currency: "CNY", DurationDays: 30, Active: true}
	if err := svc.CreateProduct(ctx, p); err != nil {
		t.Fatal(err)
	}
	order, _, err := svc.CreateOrder(ctx, "u1", p.ID, channelID(t, svc, "alipay"), "", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PayOrder(ctx, order.OutTradeNo, "alipay", "ali-1", order.AmountMinor); err != nil {
		t.Fatal(err)
	}
	if err := svc.RefundOrder(ctx, order.ID, "admin", "test"); err != nil {
		t.Fatalf("refund: %v", err)
	}

	var userAcc LedgerAccount
	err = svc.db.Where("owner_type = ? AND owner_id = ? AND kind = ?", "user", "u1", "balance").
		First(&userAcc).Error
	if err == nil && userAcc.Balance != 0 {
		t.Fatalf("wallet balance = %d, external refund must not credit the wallet", userAcc.Balance)
	}
	var refund Refund
	if err := svc.db.Where("order_id = ?", order.ID).First(&refund).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != "succeeded" || refund.ChannelRefundNo == "" {
		t.Fatalf("refund = %+v", refund)
	}
	var reloaded Order
	if err := svc.db.First(&reloaded, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != "refunded" {
		t.Fatalf("order status = %s", reloaded.Status)
	}
	revenue, _ := svc.accountBalance(ctx, "system", "revenue", "CNY", "revenue")
	clearing, _ := svc.accountBalance(ctx, "system", "clearing", "CNY", "clearing")
	if revenue != 0 || clearing != 0 {
		t.Fatalf("revenue/clearing = %d/%d, want both zero after refund", revenue, clearing)
	}

	// A second refund click must not create another refund or call the gateway.
	if err := svc.RefundOrder(ctx, order.ID, "admin", "again"); err == nil {
		t.Fatal("second refund accepted")
	}
	var count int64
	svc.db.Model(&Refund{}).Where("order_id = ?", order.ID).Count(&count)
	if count != 1 {
		t.Fatalf("refund rows = %d, want 1", count)
	}
}

func (s *Service) accountBalance(ctx context.Context, ownerType, ownerID, currency, kind string) (int64, error) {
	var acc LedgerAccount
	err := s.db.WithContext(ctx).
		Where("owner_type = ? AND owner_id = ? AND currency = ? AND kind = ?", ownerType, ownerID, currency, kind).
		First(&acc).Error
	return acc.Balance, err
}

func TestParseMinorAmount(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"29.99", 2999, true},
		{"0.01", 1, true},
		{"100", 10000, true},
		{"1.999", 200, true},
		{"-2.50", -250, true},
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseMinorAmount(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseMinorAmount(%q) = %d,%v want %d,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestApplyEventRejectsAmountAndCurrencyMismatch(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	p := &Product{Type: "vip_plan", Name: "VIP", Slug: "vip-callback",
		PriceMinor: 1000, Currency: "USD", DurationDays: 30, Active: true}
	if err := svc.CreateProduct(ctx, p); err != nil {
		t.Fatal(err)
	}
	order, _, err := svc.CreateOrder(ctx, "u1", p.ID, channelID(t, svc, "alipay"), "", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	badAmount := &NotifyEvent{OutTradeNo: order.OutTradeNo, ChannelTradeNo: "x",
		AmountMinor: 999, Currency: "USD", AmountVerified: true, Status: "paid"}
	if err := svc.applyEvent(ctx, "alipay", badAmount); err == nil {
		t.Fatal("amount mismatch accepted")
	}
	badCurrency := &NotifyEvent{OutTradeNo: order.OutTradeNo, ChannelTradeNo: "x",
		AmountMinor: 1000, Currency: "CNY", AmountVerified: true, Status: "paid"}
	if err := svc.applyEvent(ctx, "alipay", badCurrency); err == nil {
		t.Fatal("currency mismatch accepted")
	}
	good := &NotifyEvent{OutTradeNo: order.OutTradeNo, ChannelTradeNo: "x",
		AmountMinor: 1000, Currency: "USD", AmountVerified: true, Status: "paid"}
	if err := svc.applyEvent(ctx, "alipay", good); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
}

func TestRefundRetryWaitsForGatewayStep(t *testing.T) {
	svc := setup(t)
	ctx := context.Background()
	order := Order{
		ID: "o-refunding", OutTradeNo: "GFRETRY", UserID: "u1", ProductID: "p1",
		ProductType: "vip_plan", ProductName: "VIP", AmountMinor: 1000,
		Currency: "USD", ChannelCode: "alipay", Status: "refunding",
	}
	if err := svc.db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	refund := Refund{ID: "r-pending", OrderID: order.ID, UserID: "u1", AmountMinor: 1000,
		Status: "pending", CreatedAt: time.Now().UTC()}
	if err := svc.db.Create(&refund).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RefundOrder(ctx, order.ID, "admin", "retry"); err == nil {
		t.Fatal("retry finalized before the gateway step was recorded")
	}
	var reloaded Order
	if err := svc.db.First(&reloaded, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != "refunding" {
		t.Fatalf("order status = %s, want refunding", reloaded.Status)
	}
}
