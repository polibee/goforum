package billing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	gcontracts "goforum/internal/modules/gamification/contracts"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/id"
	"goforum/internal/platform/settings"
)

type Service struct {
	db       *gorm.DB
	settings *settings.Store
	points   gcontracts.Points
	audit    audit.Writer
	bus      events.Bus
	log      *slog.Logger
	// jobLease serialises background polling across instances when set.
	jobLease func(ctx context.Context, name string, ttl time.Duration) bool
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Configure(st *settings.Store, points gcontracts.Points, auditWriter audit.Writer, bus events.Bus, log *slog.Logger) {
	s.settings = st
	s.points = points
	s.audit = auditWriter
	s.bus = bus
	s.log = log
}

func (s *Service) EnsureDefaults(ctx context.Context) error {
	channels := []PaymentChannel{
		{Driver: "balance", Name: "站内余额", Enabled: true, Mode: "live", Sort: 1},
		{Driver: "manual", Name: "人工确认", Enabled: true, Mode: "live", Sort: 2},
		{Driver: "alipay", Name: "支付宝（沙箱）", Enabled: true, Mode: "sandbox", Sort: 3},
		{Driver: "wechat", Name: "微信支付（沙箱）", Enabled: true, Mode: "sandbox", Sort: 4},
		{Driver: "nowpayments", Name: "NOWPayments", Enabled: false, Mode: "live", Sort: 5},
		{Driver: "xcash", Name: "XCash", Enabled: false, Mode: "live", Sort: 6},
		{Driver: "deipay", Name: "Deipay", Enabled: false, Mode: "live", Sort: 7},
		{Driver: "creem", Name: "Creem", Enabled: false, Mode: "live", Sort: 8},
		{Driver: "lemonsqueezy", Name: "Lemon Squeezy", Enabled: false, Mode: "live", Sort: 9},
	}
	for i := range channels {
		var existing PaymentChannel
		err := s.db.WithContext(ctx).Where("driver = ?", channels[i].Driver).First(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := s.db.WithContext(ctx).Create(&channels[i]).Error; err != nil {
			return err
		}
		if !channels[i].Enabled {
			// GORM applies the column default (true) for zero-value booleans.
			if err := s.db.WithContext(ctx).Model(&PaymentChannel{}).
				Where("id = ?", channels[i].ID).Update("enabled", false).Error; err != nil {
				return err
			}
		}
	}
	// Never leave a crypto channel enabled without credentials: checkout would
	// fail after the user has already chosen it.
	if s.settings != nil {
		if s.settings.GetString("billing.nowpayments.api_key") == "" {
			_ = s.db.WithContext(ctx).Model(&PaymentChannel{}).
				Where("driver = ?", "nowpayments").Update("enabled", false).Error
		}
		if s.settings.GetString("billing.xcash.appid") == "" || s.settings.GetString("billing.xcash.hmac_key") == "" {
			_ = s.db.WithContext(ctx).Model(&PaymentChannel{}).
				Where("driver = ?", "xcash").Update("enabled", false).Error
		}
		if s.settings.GetString("billing.deipay.session_key") == "" {
			_ = s.db.WithContext(ctx).Model(&PaymentChannel{}).
				Where("driver = ?", "deipay").Update("enabled", false).Error
		}
		if s.settings.GetString("billing.creem.api_key") == "" {
			_ = s.db.WithContext(ctx).Model(&PaymentChannel{}).
				Where("driver = ?", "creem").Update("enabled", false).Error
		}
		if s.settings.GetString("billing.lemonsqueezy.api_key") == "" {
			_ = s.db.WithContext(ctx).Model(&PaymentChannel{}).
				Where("driver = ?", "lemonsqueezy").Update("enabled", false).Error
		}
	}
	return nil
}

func (s *Service) ListProducts(ctx context.Context, activeOnly bool) ([]Product, error) {
	q := s.db.WithContext(ctx).Model(&Product{})
	if activeOnly {
		q = q.Where("active = ?", true)
	}
	var rows []Product
	err := q.Order("sort ASC, created_at ASC").Find(&rows).Error
	return rows, err
}

func (s *Service) GetProduct(ctx context.Context, id string) (*Product, error) {
	var p Product
	if err := s.db.WithContext(ctx).First(&p, "id = ?", id).Error; err != nil {
		return nil, httpx.NotFound("product.not_found", "商品不存在")
	}
	return &p, nil
}

func (s *Service) CreateProduct(ctx context.Context, p *Product) error {
	if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Slug) == "" {
		return httpx.Invalid(map[string]string{"product": "名称与 slug 不能为空"})
	}
	if p.PriceMinor < 0 {
		return httpx.Invalid(map[string]string{"price": "价格不能为负"})
	}
	if p.Currency == "" {
		p.Currency = "CNY"
	}
	return s.db.WithContext(ctx).Create(p).Error
}

func (s *Service) UpdateProduct(ctx context.Context, p *Product, actorID string) error {
	if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Slug) == "" {
		return httpx.Invalid(map[string]string{"product": "名称与 slug 不能为空"})
	}
	if p.PriceMinor < 0 {
		return httpx.Invalid(map[string]string{"price": "价格不能为负"})
	}
	if p.Currency == "" {
		p.Currency = "CNY"
	}
	err := s.db.WithContext(ctx).Model(&Product{}).Where("id = ?", p.ID).Updates(map[string]any{
		"type": p.Type, "name": p.Name, "slug": p.Slug, "description": p.Description,
		"price_minor": p.PriceMinor, "currency": p.Currency, "points_amount": p.PointsAmount,
		"duration_days": p.DurationDays, "active": p.Active, "sort": p.Sort,
	}).Error
	if err == nil {
		s.writeAudit(ctx, actorID, "product.update", "product", p.ID, "")
	}
	return err
}

func (s *Service) DeleteProduct(ctx context.Context, id, actorID string) error {
	var orders int64
	if err := s.db.WithContext(ctx).Model(&Order{}).Where("product_id = ?", id).Count(&orders).Error; err != nil {
		return err
	}
	if orders > 0 {
		return httpx.Invalid(map[string]string{"product": "该商品已有订单，请使用下架而不是删除"})
	}
	if err := s.db.WithContext(ctx).Delete(&Product{}, "id = ?", id).Error; err != nil {
		return err
	}
	s.writeAudit(ctx, actorID, "product.delete", "product", id, "")
	return nil
}

func (s *Service) ToggleProduct(ctx context.Context, id string, active bool, actorID string) error {
	if err := s.db.WithContext(ctx).Model(&Product{}).Where("id = ?", id).Update("active", active).Error; err != nil {
		return err
	}
	s.writeAudit(ctx, actorID, "product.toggle", "product", id, "")
	return nil
}

func (s *Service) ListChannels(ctx context.Context, enabledOnly bool) ([]PaymentChannel, error) {
	q := s.db.WithContext(ctx).Model(&PaymentChannel{})
	if enabledOnly {
		q = q.Where("enabled = ?", true)
	}
	var rows []PaymentChannel
	err := q.Order("sort ASC, created_at ASC").Find(&rows).Error
	return rows, err
}

func (s *Service) ToggleChannel(ctx context.Context, id string, enabled bool, actorID string) error {
	if err := s.db.WithContext(ctx).Model(&PaymentChannel{}).Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		return err
	}
	s.writeAudit(ctx, actorID, "payment_channel.toggle", "payment_channel", id, "")
	return nil
}

func (s *Service) channel(ctx context.Context, id string) (*PaymentChannel, Gateway, error) {
	var ch PaymentChannel
	if err := s.db.WithContext(ctx).First(&ch, "id = ? AND enabled = ?", id, true).Error; err != nil {
		return nil, nil, httpx.Invalid(map[string]string{"channel": "支付渠道不可用"})
	}
	gw, ok := s.gateway(ch.Driver)
	if !ok {
		return nil, nil, httpx.Invalid(map[string]string{"channel": "支付渠道未实现"})
	}
	return &ch, gw, nil
}

// CreateAdOrder creates an order for a self-serve ad purchase. Ads are not
// catalog products, so the ad ID is carried in ProductID/ProductSlug and the
// ads module activates the ad from the order.paid event.
func (s *Service) CreateAdOrder(ctx context.Context, userID, adID, channelID, subject string, amountMinor int64, currency, clientIP string) (string, string, bool, error) {
	if amountMinor <= 0 {
		return "", "", false, httpx.Invalid(map[string]string{"amount": "金额必须大于 0"})
	}
	ch, gw, err := s.channel(ctx, channelID)
	if err != nil {
		return "", "", false, err
	}
	if currency == "" {
		currency = "USD"
	}
	if ch.Driver == "balance" {
		return "", "", false, httpx.Invalid(map[string]string{"channel": "美元广告暂不支持站内余额，请选择在线支付"})
	}
	if len([]rune(subject)) > 120 {
		subject = string([]rune(subject)[:120])
	}
	now := time.Now().UTC()
	order := &Order{
		OutTradeNo: newTradeNo(), UserID: userID,
		ProductID: adID, ProductSlug: "ad:" + adID, ProductType: "ad_slot", ProductName: subject,
		AmountMinor: amountMinor, Currency: currency, ChannelID: ch.ID, ChannelCode: ch.Driver,
		Status: "created", Subject: subject, ClientIP: clientIP,
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(order).Error; err != nil {
		return "", "", false, err
	}
	if ch.Driver == "balance" {
		if err := s.PayOrder(ctx, order.OutTradeNo, ch.Driver, "balance-"+order.ID, amountMinor); err != nil {
			return "", "", false, err
		}
		return order.OutTradeNo, "", true, nil
	}
	params, err := gw.Create(ctx, *order)
	if err != nil {
		if markErr := s.db.WithContext(ctx).Model(&Order{}).Where("id = ?", order.ID).
			Update("status", "failed").Error; markErr != nil {
			// The order stays pending; the gateway error is the primary failure
			// so it is returned, but this must be visible in the log.
			s.log.Warn("mark order failed after gateway error", "order_id", order.ID, "error", markErr)
		}
		return "", "", false, err
	}
	updates := map[string]any{"status": "pending"}
	payURL := ""
	if params != nil && params.URL != "" {
		updates["pay_url"] = params.URL
		payURL = params.URL
	}
	if err := s.db.WithContext(ctx).Model(&Order{}).Where("id = ? AND status = ?", order.ID, "created").Updates(updates).Error; err != nil {
		return "", "", false, err
	}
	return order.OutTradeNo, payURL, false, nil
}

func (s *Service) CreateOrder(ctx context.Context, userID, productID, channelID, couponCode, clientIP string) (*Order, *PayParams, error) {
	product, err := s.GetProduct(ctx, productID)
	if err != nil {
		return nil, nil, err
	}
	if !product.Active {
		return nil, nil, httpx.Invalid(map[string]string{"product": "商品已下架"})
	}
	ch, gw, err := s.channel(ctx, channelID)
	if err != nil {
		return nil, nil, err
	}
	amount := product.PriceMinor
	var coupon *Coupon
	var discount int64
	if code := strings.TrimSpace(couponCode); code != "" {
		coupon, discount, err = s.validateCoupon(ctx, code, userID, amount)
		if err != nil {
			return nil, nil, err
		}
	}
	amount -= discount
	if amount < 0 {
		amount = 0
	}
	if ch.Driver == "balance" {
		bal, _ := s.UserBalance(ctx, userID)
		if bal < amount {
			return nil, nil, httpx.Invalid(map[string]string{"balance": "站内余额不足"})
		}
	}

	order := &Order{
		OutTradeNo:    newTradeNo(),
		UserID:        userID,
		ProductID:     product.ID,
		ProductSlug:   product.Slug,
		ProductType:   product.Type,
		ProductName:   product.Name,
		AmountMinor:   amount,
		DiscountMinor: discount,
		PointsAmount:  product.PointsAmount,
		DurationDays:  product.DurationDays,
		Currency:      product.Currency,
		ChannelID:     ch.ID,
		ChannelCode:   ch.Driver,
		Status:        "created",
		Subject:       product.Name,
		ClientIP:      clientIP,
		ExpiresAt:     time.Now().UTC().Add(30 * time.Minute),
		CouponCode:    strings.ToUpper(strings.TrimSpace(couponCode)),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(order).Error; err != nil {
			return err
		}
		if coupon != nil {
			// Conditional increment prevents overselling when two orders redeem
			// the last slot at the same time (total = 0 means unlimited).
			res := tx.Model(&Coupon{}).
				Where("id = ? AND (total = 0 OR used < total)", coupon.ID).
				Update("used", gorm.Expr("used + 1"))
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return httpx.Invalid(map[string]string{"coupon": "优惠券已被领完"})
			}
			if err := tx.Create(&CouponRedemption{
				CouponID: coupon.ID, UserID: userID, OrderID: order.ID, DiscountMinor: discount,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	if ch.Driver == "balance" || amount == 0 {
		code := ch.Driver
		tradeNo := "balance-" + order.ID
		if amount == 0 {
			code = "coupon"
			tradeNo = "coupon-" + order.ID
		}
		if err := s.PayOrder(ctx, order.OutTradeNo, code, tradeNo, amount); err != nil {
			return nil, nil, err
		}
		order.Status = "paid"
		return order, &PayParams{Type: "balance"}, nil
	}

	params, err := gw.Create(ctx, *order)
	if err != nil {
		if markErr := s.db.WithContext(ctx).Model(&Order{}).Where("id = ?", order.ID).
			Update("status", "failed").Error; markErr != nil {
			// The order stays pending; the gateway error is the primary failure
			// so it is returned, but this must be visible in the log.
			s.log.Warn("mark order failed after gateway error", "order_id", order.ID, "error", markErr)
		}
		return nil, nil, err
	}
	updates := map[string]any{"status": "pending"}
	if params != nil && params.URL != "" {
		updates["pay_url"] = params.URL
		order.PayURL = params.URL
	}
	if err := s.db.WithContext(ctx).Model(&Order{}).Where("id = ? AND status = ?", order.ID, "created").
		Updates(updates).Error; err != nil {
		return nil, nil, err
	}
	order.Status = "pending"
	return order, params, nil
}

func (s *Service) PayOrder(ctx context.Context, outTradeNo, channelCode, channelTradeNo string, amount int64) error {
	var paidOrder *Order
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order Order
		if err := tx.Where("out_trade_no = ?", outTradeNo).First(&order).Error; err != nil {
			return httpx.NotFound("order.not_found", "订单不存在")
		}
		if order.Status == "paid" {
			paidOrder = &order
			return nil
		}
		if order.Status != "created" && order.Status != "pending" {
			return httpx.Invalid(map[string]string{"order": "订单状态不可支付"})
		}
		if amount != order.AmountMinor {
			return httpx.NewError("payment.amount_mismatch", 400, "支付金额与订单不一致")
		}
		if channelCode != order.ChannelCode && channelCode != "coupon" {
			return httpx.NewError("payment.channel_mismatch", 400, "支付渠道与订单不一致")
		}
		now := time.Now().UTC()
		res := tx.Model(&Order{}).
			Where("id = ? AND status IN ?", order.ID, []string{"created", "pending"}).
			Updates(map[string]any{
				"status": "paid", "paid_at": now, "channel_trade_no": channelTradeNo,
				"channel_code": channelCode, "updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return httpx.Invalid(map[string]string{"order": "订单已处理"})
		}
		txn := PaymentTransaction{
			OrderID: order.ID, ChannelCode: channelCode, Type: "payment",
			ChannelTradeNo: channelTradeNo, AmountMinor: amount, Currency: order.Currency,
			Status: "success", CreatedAt: now,
		}
		if err := tx.Create(&txn).Error; err != nil {
			return err
		}
		lines := []ledgerLine{}
		if channelCode == "balance" {
			lines = append(lines,
				ledgerLine{OwnerType: "user", OwnerID: order.UserID, Currency: order.Currency, Kind: "balance", Direction: "debit", Amount: amount, EntryType: "purchase"},
				ledgerLine{OwnerType: "system", OwnerID: "revenue", Currency: order.Currency, Kind: "revenue", Direction: "credit", Amount: amount, EntryType: "purchase"},
			)
		} else {
			lines = append(lines,
				ledgerLine{OwnerType: "system", OwnerID: "clearing", Currency: order.Currency, Kind: "clearing", Direction: "debit", Amount: amount, EntryType: "payment"},
				ledgerLine{OwnerType: "system", OwnerID: "revenue", Currency: order.Currency, Kind: "revenue", Direction: "credit", Amount: amount, EntryType: "payment"},
			)
		}
		if err := s.postLedger(tx, "payment", "order:"+order.ID+":paid", "order", order.ID, "订单支付", lines); err != nil {
			return err
		}
		if err := s.grant(tx, &order); err != nil {
			return err
		}
		order.Status = "paid"
		order.PaidAt = &now
		paidOrder = &order
		return nil
	})
	if err != nil {
		return err
	}
	if paidOrder != nil && paidOrder.ProductType == "points_pack" && paidOrder.PointsAmount > 0 && s.points != nil {
		if err := s.points.CreditExternal(ctx, paidOrder.UserID, paidOrder.PointsAmount, "order", paidOrder.ID, "购买积分包"); err != nil {
			return err
		}
	}
	if paidOrder != nil && s.bus != nil {
		payload, _ := json.Marshal(map[string]any{
			"order_id": paidOrder.ID, "user_id": paidOrder.UserID,
			"amount_minor": paidOrder.AmountMinor, "product_type": paidOrder.ProductType,
			"product_id": paidOrder.ProductID, "product_slug": paidOrder.ProductSlug,
		})
		_ = s.bus.Publish(ctx, events.NewEvent("order.paid", "order", paidOrder.ID, payload))
	}
	return nil
}

func (s *Service) grant(tx *gorm.DB, order *Order) error {
	switch order.ProductType {
	case "vip_plan":
		now := time.Now().UTC()
		days := order.DurationDays
		if days <= 0 {
			days = 30
		}
		ends := now.AddDate(0, 0, days)
		var sub Subscription
		err := tx.Where("user_id = ? AND product_id = ? AND status = ?", order.UserID, order.ProductID, "active").
			Order("ends_at DESC").First(&sub).Error
		if err == nil && sub.EndsAt.After(now) {
			ends = sub.EndsAt.AddDate(0, 0, days)
			if err := tx.Model(&Subscription{}).Where("id = ?", sub.ID).Update("ends_at", ends).Error; err != nil {
				return err
			}
		} else {
			sub = Subscription{
				UserID: order.UserID, ProductID: order.ProductID, Status: "active",
				StartsAt: now, EndsAt: ends, SourceOrderID: order.ID,
			}
			if err := tx.Create(&sub).Error; err != nil {
				return err
			}
		}
		ent := Entitlement{
			UserID: order.UserID, ResourceType: "vip", ResourceID: order.ProductID,
			SourceType: "order", SourceID: order.ID, StartsAt: now, ExpiresAt: &ends,
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}, {Name: "resource_type"}, {Name: "resource_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"expires_at": ends, "revoked_at": nil, "source_id": order.ID,
			}),
		}).Create(&ent).Error
	case "points_pack", "ad_slot":
		return nil
	case "invite_pack":
		return nil
	}
	return nil
}

func (s *Service) RefundOrder(ctx context.Context, orderID, operatorID, reason string) error {
	var order Order
	if err := s.db.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		return httpx.NotFound("order.not_found", "订单不存在")
	}
	if order.Status != "paid" {
		return httpx.Invalid(map[string]string{"order": "订单状态不可退款"})
	}
	var refunded int64
	_ = s.db.WithContext(ctx).Model(&Refund{}).Where("order_id = ? AND status = ?", order.ID, "succeeded").
		Select("COALESCE(SUM(amount_minor), 0)").Scan(&refunded).Error
	amount := order.AmountMinor - refunded
	if amount <= 0 {
		return httpx.Invalid(map[string]string{"order": "已无可退金额"})
	}
	refund := &Refund{
		ID: id.New(), OrderID: order.ID, UserID: order.UserID, AmountMinor: amount,
		Reason: reason, Status: "pending", OperatorID: operatorID, CreatedAt: time.Now().UTC(),
	}

	// Ask the payment provider first. Crediting the in-app balance while the
	// customer still has their money is a real loss, so a gateway failure must
	// abort before any local ledger entry is written.
	channelGW, ok := s.gateway(order.ChannelCode)
	if !ok {
		return httpx.Invalid(map[string]string{"refund": "该支付渠道不支持退款"})
	}
	gatewayRefundNo, gwErr := channelGW.Refund(ctx, order, *refund)
	if gwErr != nil {
		return httpx.NewError("refund.gateway_failed", 502,
			"支付渠道退款失败："+httpx.ErrorOf(gwErr).Message)
	}
	refund.ChannelRefundNo = gatewayRefundNo
	if order.ProductType == "points_pack" && order.PointsAmount > 0 && s.points != nil {
		if err := s.points.SpendExternal(ctx, order.UserID, order.PointsAmount, "refund", refund.ID, "退款扣回积分"); err != nil {
			return httpx.Invalid(map[string]string{"refund": "积分已消费，无法退款"})
		}
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(refund).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		res := tx.Model(&Order{}).Where("id = ? AND status = ?", order.ID, "paid").
			Updates(map[string]any{"status": "refunded", "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return httpx.Invalid(map[string]string{"order": "订单状态已变化"})
		}
		lines := []ledgerLine{
			{OwnerType: "system", OwnerID: "refund", Currency: order.Currency, Kind: "refund", Direction: "debit", Amount: amount, EntryType: "refund"},
			{OwnerType: "user", OwnerID: order.UserID, Currency: order.Currency, Kind: "balance", Direction: "credit", Amount: amount, EntryType: "refund"},
		}
		if err := s.postLedger(tx, "refund", "refund:"+refund.ID, "refund", refund.ID, "订单退款", lines); err != nil {
			return err
		}
		if err := tx.Model(&Refund{}).Where("id = ?", refund.ID).Updates(map[string]any{
			"status": "succeeded", "completed_at": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&Entitlement{}).Where("source_type = ? AND source_id = ?", "order", order.ID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		return tx.Model(&Subscription{}).Where("source_order_id = ?", order.ID).
			Updates(map[string]any{"status": "canceled", "canceled_at": now}).Error
	})
	if err != nil {
		if order.ProductType == "points_pack" && s.points != nil {
			_ = s.points.CreditExternal(ctx, order.UserID, order.PointsAmount, "refund", refund.ID+":rollback", "退款失败退回积分")
		}
		return err
	}
	if s.bus != nil {
		_ = s.bus.Publish(ctx, events.NewEvent("order.refunded", "order", order.ID, []byte(`{"order_id":"`+order.ID+`"}`)))
	}
	s.writeAudit(ctx, operatorID, "order.refund", "order", order.ID, reason)
	return nil
}

type ledgerLine struct {
	OwnerType string
	OwnerID   string
	Currency  string
	Kind      string
	Direction string
	Amount    int64
	EntryType string
}

func (s *Service) postLedger(tx *gorm.DB, typ, key, refType, refID, description string, lines []ledgerLine) error {
	var debit, credit int64
	for _, l := range lines {
		if l.Direction == "debit" {
			debit += l.Amount
		} else {
			credit += l.Amount
		}
	}
	if debit != credit {
		return errors.New("ledger entries are not balanced")
	}
	lt := LedgerTransaction{
		ID: id.New(), Type: typ, Status: "posted", IdempotencyKey: key,
		RefType: refType, RefID: refID, Description: description, CreatedAt: time.Now().UTC(),
	}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&lt)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}
	for _, l := range lines {
		acc, err := s.ensureAccount(tx, l.OwnerType, l.OwnerID, l.Currency, l.Kind)
		if err != nil {
			return err
		}
		delta := l.Amount
		if l.Direction == "debit" {
			delta = -l.Amount
		}
		q := tx.Model(&LedgerAccount{}).Where("id = ?", acc.ID)
		if delta < 0 && l.OwnerType == "user" && l.Kind == "balance" {
			q = q.Where("balance >= ?", -delta)
		}
		upd := q.Update("balance", gorm.Expr("balance + ?", delta))
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected == 0 {
			return httpx.Invalid(map[string]string{"balance": "余额不足"})
		}
		if err := tx.Where("id = ?", acc.ID).First(&acc).Error; err != nil {
			return err
		}
		entry := LedgerEntry{
			TransactionID: lt.ID, AccountID: acc.ID, Direction: l.Direction,
			Amount: l.Amount, BalanceAfter: acc.Balance, EntryType: l.EntryType, CreatedAt: time.Now().UTC(),
		}
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ensureAccount(tx *gorm.DB, ownerType, ownerID, currency, kind string) (*LedgerAccount, error) {
	var acc LedgerAccount
	err := tx.Where("owner_type = ? AND owner_id = ? AND currency = ? AND kind = ?", ownerType, ownerID, currency, kind).
		First(&acc).Error
	if err == nil {
		return &acc, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	acc = LedgerAccount{OwnerType: ownerType, OwnerID: ownerID, Currency: currency, Kind: kind}
	if err := tx.Create(&acc).Error; err != nil {
		return nil, err
	}
	return &acc, nil
}

func (s *Service) UserBalance(ctx context.Context, userID string) (int64, error) {
	var acc LedgerAccount
	err := s.db.WithContext(ctx).
		Where("owner_type = ? AND owner_id = ? AND kind = ?", "user", userID, "balance").First(&acc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return acc.Balance, nil
}

func (s *Service) AdjustBalance(ctx context.Context, userID string, amount int64, operatorID, reason string) error {
	if amount == 0 {
		return httpx.Invalid(map[string]string{"amount": "金额不能为 0"})
	}
	var lines []ledgerLine
	if amount > 0 {
		lines = []ledgerLine{
			{OwnerType: "system", OwnerID: "adjust", Currency: "CNY", Kind: "adjust", Direction: "debit", Amount: amount, EntryType: "adjust"},
			{OwnerType: "user", OwnerID: userID, Currency: "CNY", Kind: "balance", Direction: "credit", Amount: amount, EntryType: "adjust"},
		}
	} else {
		amt := -amount
		lines = []ledgerLine{
			{OwnerType: "user", OwnerID: userID, Currency: "CNY", Kind: "balance", Direction: "debit", Amount: amt, EntryType: "adjust"},
			{OwnerType: "system", OwnerID: "adjust", Currency: "CNY", Kind: "adjust", Direction: "credit", Amount: amt, EntryType: "adjust"},
		}
	}
	key := "adjust:" + id.New()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.postLedger(tx, "adjust", key, "user", userID, reason, lines)
	})
	if err != nil {
		return err
	}
	s.writeAudit(ctx, operatorID, "balance.adjust", "user", userID, reason)
	return nil
}

func (s *Service) validateCoupon(ctx context.Context, code, userID string, amount int64) (*Coupon, int64, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	var c Coupon
	if err := s.db.WithContext(ctx).Where("code = ? AND enabled = ?", code, true).First(&c).Error; err != nil {
		return nil, 0, httpx.Invalid(map[string]string{"coupon": "优惠券无效"})
	}
	now := time.Now().UTC()
	if now.Before(c.StartsAt) || now.After(c.EndsAt) {
		return nil, 0, httpx.Invalid(map[string]string{"coupon": "优惠券不在有效期"})
	}
	if c.Total > 0 && c.Used >= c.Total {
		return nil, 0, httpx.Invalid(map[string]string{"coupon": "优惠券已领完"})
	}
	if amount < c.MinAmountMinor {
		return nil, 0, httpx.Invalid(map[string]string{"coupon": "未达到最低消费"})
	}
	var used int64
	_ = s.db.WithContext(ctx).Model(&CouponRedemption{}).
		Where("coupon_id = ? AND user_id = ?", c.ID, userID).Count(&used).Error
	if c.PerUserLimit > 0 && int(used) >= c.PerUserLimit {
		return nil, 0, httpx.Invalid(map[string]string{"coupon": "该优惠券已使用"})
	}
	discount := c.Value
	if c.Type == "percent" {
		discount = amount * c.Value / 100
	}
	if discount > amount {
		discount = amount
	}
	if discount < 0 {
		discount = 0
	}
	return &c, discount, nil
}

func (s *Service) CreateCoupon(ctx context.Context, c *Coupon) error {
	c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
	if c.Code == "" || (c.Type != "fixed" && c.Type != "percent") {
		return httpx.Invalid(map[string]string{"coupon": "优惠券参数无效"})
	}
	if c.Type == "percent" && (c.Value < 1 || c.Value > 100) {
		return httpx.Invalid(map[string]string{"value": "百分比需在 1–100"})
	}
	if c.Currency == "" {
		c.Currency = "CNY"
	}
	if c.StartsAt.IsZero() {
		c.StartsAt = time.Now().UTC()
	}
	if c.EndsAt.IsZero() {
		c.EndsAt = time.Now().UTC().AddDate(0, 1, 0)
	}
	return s.db.WithContext(ctx).Create(c).Error
}

func (s *Service) ListCoupons(ctx context.Context) ([]Coupon, error) {
	var rows []Coupon
	err := s.db.WithContext(ctx).Order("created_at DESC").Find(&rows).Error
	return rows, err
}

func (s *Service) ListOrders(ctx context.Context, userID string, limit int) ([]Order, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := s.db.WithContext(ctx).Model(&Order{})
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	var rows []Order
	err := q.Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListOrdersPage 分页返回订单及符合条件的总数；userID 为空时返回全部订单（后台用）。
func (s *Service) ListOrdersPage(ctx context.Context, userID string, limit, offset int) ([]Order, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	base := s.db.WithContext(ctx).Model(&Order{})
	if userID != "" {
		base = base.Where("user_id = ?", userID)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Order
	err := base.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *Service) GetOrderByNo(ctx context.Context, outTradeNo string) (*Order, error) {
	var o Order
	if err := s.db.WithContext(ctx).Where("out_trade_no = ?", outTradeNo).First(&o).Error; err != nil {
		return nil, httpx.NotFound("order.not_found", "订单不存在")
	}
	return &o, nil
}

func (s *Service) ConfirmManual(ctx context.Context, orderID, operatorID string) error {
	var o Order
	if err := s.db.WithContext(ctx).First(&o, "id = ?", orderID).Error; err != nil {
		return httpx.NotFound("order.not_found", "订单不存在")
	}
	if o.ChannelCode != "manual" {
		return httpx.Invalid(map[string]string{"order": "不是人工确认订单"})
	}
	if err := s.PayOrder(ctx, o.OutTradeNo, "manual", "manual-"+o.ID, o.AmountMinor); err != nil {
		return err
	}
	s.writeAudit(ctx, operatorID, "order.confirm_manual", "order", o.ID, "")
	return nil
}

func (s *Service) ListSubscriptions(ctx context.Context, userID string) ([]Subscription, error) {
	q := s.db.WithContext(ctx).Model(&Subscription{})
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	var rows []Subscription
	err := q.Order("created_at DESC").Limit(200).Find(&rows).Error
	return rows, err
}

// ListSubscriptionsPage 分页返回订阅记录及总数；userID 为空时返回全部订阅（后台用）。
func (s *Service) ListSubscriptionsPage(ctx context.Context, userID string, limit, offset int) ([]Subscription, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	base := s.db.WithContext(ctx).Model(&Subscription{})
	if userID != "" {
		base = base.Where("user_id = ?", userID)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Subscription
	err := base.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *Service) ActiveEntitlements(ctx context.Context, userID string) ([]Entitlement, error) {
	now := time.Now().UTC()
	var rows []Entitlement
	err := s.db.WithContext(ctx).Where("user_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", userID, now).
		Order("created_at DESC").Find(&rows).Error
	return rows, err
}

func (s *Service) RunReconciliation(ctx context.Context, channelCode, bizDate string) (*ReconciliationRun, error) {
	day, err := time.Parse("2006-01-02", bizDate)
	if err != nil {
		return nil, httpx.Invalid(map[string]string{"biz_date": "日期格式应为 YYYY-MM-DD"})
	}
	start := day.UTC()
	end := start.Add(24 * time.Hour)
	run := &ReconciliationRun{
		ChannelCode: channelCode, BizDate: bizDate, Status: "running",
		StartedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
		return nil, err
	}
	var orders []Order
	_ = s.db.WithContext(ctx).Where("channel_code = ? AND status IN ? AND paid_at >= ? AND paid_at < ?",
		channelCode, []string{"paid", "refunded"}, start, end).Find(&orders).Error
	var txs []PaymentTransaction
	_ = s.db.WithContext(ctx).Where("channel_code = ? AND type = ? AND status = ? AND created_at >= ? AND created_at < ?",
		channelCode, "payment", "success", start, end).Find(&txs).Error
	txByOrder := make(map[string]int64, len(txs))
	for _, t := range txs {
		run.ChannelAmount += t.AmountMinor
		txByOrder[t.OrderID] += t.AmountMinor
	}
	diffs := make([]ReconciliationDiff, 0)
	for _, o := range orders {
		run.LocalAmount += o.AmountMinor
		if txByOrder[o.ID] != o.AmountMinor {
			run.DiffCount++
			diffs = append(diffs, ReconciliationDiff{
				RunID: run.ID, OutTradeNo: o.OutTradeNo, DiffType: "missing_or_mismatch",
				LocalAmount: o.AmountMinor, ChannelAmount: txByOrder[o.ID],
			})
		}
		delete(txByOrder, o.ID)
	}
	for orderID, amt := range txByOrder {
		run.DiffCount++
		diffs = append(diffs, ReconciliationDiff{
			RunID: run.ID, OutTradeNo: orderID, DiffType: "channel_only", ChannelAmount: amt,
		})
	}
	now := time.Now().UTC()
	run.Status = "completed"
	run.FinishedAt = &now
	if err := s.db.WithContext(ctx).Model(&ReconciliationRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": run.Status, "local_amount": run.LocalAmount, "channel_amount": run.ChannelAmount,
		"diff_count": run.DiffCount, "finished_at": now,
	}).Error; err != nil {
		return nil, err
	}
	for i := range diffs {
		if err := s.db.WithContext(ctx).Create(&diffs[i]).Error; err != nil {
			return nil, err
		}
	}
	return run, nil
}

func (s *Service) ListReconciliation(ctx context.Context) ([]ReconciliationRun, error) {
	var rows []ReconciliationRun
	err := s.db.WithContext(ctx).Order("started_at DESC").Limit(50).Find(&rows).Error
	return rows, err
}

type ReportRow struct {
	Date       string
	Revenue    int64
	Refunded   int64
	Orders     int64
	RevenueUSD int64
	OrdersUSD  int64
}

func (s *Service) Report(ctx context.Context, days int) ([]ReportRow, error) {
	if days <= 0 || days > 30 {
		days = 7
	}
	rows := make([]ReportRow, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := time.Now().UTC().AddDate(0, 0, -i)
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
		end := start.Add(24 * time.Hour)
		var revenue, refunded, orders int64
		_ = s.db.WithContext(ctx).Model(&Order{}).
			Where("status IN ? AND paid_at >= ? AND paid_at < ? AND currency = ?", []string{"paid", "refunded"}, start, end, "CNY").
			Select("COALESCE(SUM(amount_minor),0)").Scan(&revenue).Error
		_ = s.db.WithContext(ctx).Model(&Order{}).
			Where("status IN ? AND paid_at >= ? AND paid_at < ? AND currency = ?", []string{"paid", "refunded"}, start, end, "CNY").
			Count(&orders).Error
		_ = s.db.WithContext(ctx).Model(&Refund{}).
			Where("status = ? AND created_at >= ? AND created_at < ?", "succeeded", start, end).
			Select("COALESCE(SUM(amount_minor),0)").Scan(&refunded).Error
		var revenueUSD, ordersUSD int64
		_ = s.db.WithContext(ctx).Model(&Order{}).
			Where("status IN ? AND paid_at >= ? AND paid_at < ? AND currency = ?", []string{"paid", "refunded"}, start, end, "USD").
			Select("COALESCE(SUM(amount_minor),0)").Scan(&revenueUSD).Error
		_ = s.db.WithContext(ctx).Model(&Order{}).
			Where("status IN ? AND paid_at >= ? AND paid_at < ? AND currency = ?", []string{"paid", "refunded"}, start, end, "USD").
			Count(&ordersUSD).Error
		rows = append(rows, ReportRow{
			Date: start.Format("01-02"), Revenue: revenue, Refunded: refunded, Orders: orders,
			RevenueUSD: revenueUSD, OrdersUSD: ordersUSD,
		})
	}
	return rows, nil
}

// HandleNotify verifies and applies a gateway callback.
func (s *Service) HandleNotify(ctx context.Context, driver string, r *http.Request) (int, string) {
	gw, ok := s.gateway(driver)
	if !ok {
		return http.StatusNotFound, "unknown gateway"
	}
	ev, err := gw.VerifyNotify(ctx, r)
	if err != nil {
		if s.log != nil {
			s.log.Warn("payment notify rejected", "driver", driver, "error", err)
		}
		return http.StatusBadRequest, "invalid notify"
	}
	if err := s.applyEvent(ctx, driver, ev); err != nil {
		if s.log != nil {
			s.log.Warn("payment notify apply failed", "driver", driver, "order", ev.OutTradeNo, "error", err)
		}
		return http.StatusBadRequest, "failed"
	}
	return gw.Ack(ev)
}

func (s *Service) applyEvent(ctx context.Context, driver string, ev *NotifyEvent) error {
	if ev == nil || ev.OutTradeNo == "" {
		return nil
	}
	order, err := s.GetOrderByNo(ctx, ev.OutTradeNo)
	if err != nil {
		return nil // unknown order: acknowledge to stop retries
	}
	if order.ChannelCode != driver {
		return httpx.Invalid(map[string]string{"channel": "支付渠道与订单不一致"})
	}
	switch strings.ToLower(ev.Status) {
	case "finished", "confirmed", "paid":
		if ev.AmountMinor > 0 && ev.AmountMinor != order.AmountMinor {
			return httpx.NewError("payment.amount_mismatch", 400, "支付金额与订单不一致")
		}
		return s.PayOrder(ctx, order.OutTradeNo, driver, ev.ChannelTradeNo, order.AmountMinor)
	case "expired", "failed":
		return s.closeOrder(ctx, order.ID)
	default:
		return nil
	}
}

// SetJobLease installs a cross-instance lease so only one instance polls
// gateways per tick when the cache is shared.
func (s *Service) SetJobLease(fn func(ctx context.Context, name string, ttl time.Duration) bool) {
	s.jobLease = fn
}

// StartBackground polls gateways without webhooks (Deipay) every 5 seconds.
func (s *Service) StartBackground(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.jobLease != nil && !s.jobLease(ctx, "deipay-poll", 4*time.Second) {
					continue
				}
				s.pollDeipay(ctx)
			}
		}
	}()
}

func (s *Service) pollDeipay(ctx context.Context) {
	if s.settings == nil || s.settings.GetString("billing.deipay.session_key") == "" {
		return
	}
	var enabled int64
	_ = s.db.WithContext(ctx).Model(&PaymentChannel{}).
		Where("driver = ? AND enabled = ?", "deipay", true).Count(&enabled).Error
	if enabled == 0 {
		return
	}
	gw, ok := s.gateway("deipay")
	if !ok {
		return
	}
	poller, ok := gw.(Poller)
	if !ok {
		return
	}
	events, err := poller.Poll(ctx)
	if err != nil {
		if s.log != nil {
			s.log.Warn("deipay poll failed", "error", err)
		}
		return
	}
	for i := range events {
		if err := s.applyEvent(ctx, "deipay", &events[i]); err != nil && s.log != nil {
			s.log.Warn("deipay event apply failed", "order", events[i].OutTradeNo, "error", err)
		}
	}
}

func (s *Service) writeAudit(ctx context.Context, actor, action, targetType, targetID, reason string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Write(ctx, audit.Entry{
		ActorID: actor, Action: action, TargetType: targetType, TargetID: targetID, Reason: reason,
	})
}

func newTradeNo() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "GF" + time.Now().UTC().Format("20060102150405") + strings.ToUpper(hex.EncodeToString(b))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// closeOrder closes an unpaid order and releases any coupon slot it reserved.
// Reservations are taken at order creation, so abandoning checkout must not
// burn the user's coupon.
func (s *Service) closeOrder(ctx context.Context, orderID string) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Order{}).
			Where("id = ? AND status IN ?", orderID, []string{"created", "pending"}).
			Updates(map[string]any{"status": "closed", "closed_at": now, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		var red CouponRedemption
		if err := tx.Where("order_id = ?", orderID).First(&red).Error; err == nil {
			_ = tx.Model(&Coupon{}).Where("id = ? AND used > 0", red.CouponID).
				Update("used", gorm.Expr("used - 1")).Error
			_ = tx.Delete(&CouponRedemption{}, "id = ?", red.ID).Error
		}
		return nil
	})
}
