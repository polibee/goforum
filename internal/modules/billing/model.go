package billing

import (
	"time"

	"gorm.io/gorm"

	"goforum/internal/platform/id"
)

type Product struct {
	ID           string `gorm:"primaryKey;size:26"`
	Type         string `gorm:"size:24;not null;index"` // points_pack | vip_plan | invite_pack
	Name         string `gorm:"size:120;not null"`
	Slug         string `gorm:"size:120;uniqueIndex;not null"`
	Description  string `gorm:"size:500"`
	PriceMinor   int64  `gorm:"not null"`
	Currency     string `gorm:"size:3;not null;default:CNY"`
	PointsAmount int64  `gorm:"not null;default:0"`
	DurationDays int    `gorm:"not null;default:0"`
	Active       bool   `gorm:"not null;default:true;index"`
	Sort         int    `gorm:"not null;default:0"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Product) TableName() string { return "products" }

type PaymentChannel struct {
	ID         string `gorm:"primaryKey;size:26"`
	Driver     string `gorm:"size:24;not null"`
	Name       string `gorm:"size:80;not null"`
	Enabled    bool   `gorm:"not null;default:true;index"`
	Mode       string `gorm:"size:16;not null;default:sandbox"`
	Sort       int    `gorm:"not null;default:0"`
	ConfigJSON string `gorm:"type:text"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (PaymentChannel) TableName() string { return "payment_channels" }

type Order struct {
	ID             string    `gorm:"primaryKey;size:26"`
	OutTradeNo     string    `gorm:"size:40;uniqueIndex;not null"`
	UserID         string    `gorm:"size:26;index;not null"`
	ProductID      string    `gorm:"size:26;index;not null"`
	ProductSlug    string    `gorm:"size:120"`
	ProductType    string    `gorm:"size:24;not null"`
	ProductName    string    `gorm:"size:120;not null"`
	AmountMinor    int64     `gorm:"not null"`
	DiscountMinor  int64     `gorm:"not null;default:0"`
	PointsAmount   int64     `gorm:"not null;default:0"`
	DurationDays   int       `gorm:"not null;default:0"`
	Currency       string    `gorm:"size:3;not null;default:CNY"`
	ChannelID      string    `gorm:"size:26"`
	ChannelCode    string    `gorm:"size:24"`
	Status         string    `gorm:"size:16;not null;default:created;index"`
	Subject        string    `gorm:"size:200"`
	ClientIP       string    `gorm:"size:45"`
	ExpiresAt      time.Time `gorm:"index"`
	PaidAt         *time.Time
	ClosedAt       *time.Time
	ChannelTradeNo string    `gorm:"size:80"`
	CouponCode     string    `gorm:"size:40"`
	PayURL         string    `gorm:"size:500"`
	CreatedAt      time.Time `gorm:"index"`
	UpdatedAt      time.Time
}

func (Order) TableName() string { return "orders" }

type PaymentTransaction struct {
	ID             string    `gorm:"primaryKey;size:26"`
	OrderID        string    `gorm:"size:26;index;not null"`
	ChannelCode    string    `gorm:"size:24;not null;uniqueIndex:uk_payment_tx"`
	Type           string    `gorm:"size:16;not null;uniqueIndex:uk_payment_tx"`
	ChannelTradeNo string    `gorm:"size:80;not null;uniqueIndex:uk_payment_tx"`
	AmountMinor    int64     `gorm:"not null"`
	Currency       string    `gorm:"size:3;not null"`
	Status         string    `gorm:"size:16;not null;default:success"`
	RawJSON        string    `gorm:"type:text"`
	CreatedAt      time.Time `gorm:"index"`
}

func (PaymentTransaction) TableName() string { return "payment_transactions" }

type NotifyLog struct {
	ID          string `gorm:"primaryKey;size:26"`
	ChannelCode string `gorm:"size:24;not null;uniqueIndex:uk_notify"`
	EventID     string `gorm:"size:80;not null;uniqueIndex:uk_notify"`
	OutTradeNo  string `gorm:"size:40;index"`
	Verified    bool
	Processed   bool
	RawBody     string    `gorm:"type:text"`
	Error       string    `gorm:"size:500"`
	CreatedAt   time.Time `gorm:"index"`
}

func (NotifyLog) TableName() string { return "payment_notify_logs" }

type Refund struct {
	ID              string `gorm:"primaryKey;size:26"`
	OrderID         string `gorm:"size:26;index;not null"`
	UserID          string `gorm:"size:26;index;not null"`
	AmountMinor     int64  `gorm:"not null"`
	Reason          string `gorm:"size:300"`
	Status          string `gorm:"size:16;not null;default:succeeded"`
	ChannelRefundNo string `gorm:"size:80"`
	OperatorID      string `gorm:"size:26"`
	CreatedAt       time.Time
	CompletedAt     *time.Time
}

func (Refund) TableName() string { return "refunds" }

type Coupon struct {
	ID             string `gorm:"primaryKey;size:26"`
	Code           string `gorm:"size:40;uniqueIndex;not null"`
	Type           string `gorm:"size:16;not null"` // fixed | percent
	Value          int64  `gorm:"not null"`
	MinAmountMinor int64  `gorm:"not null;default:0"`
	Currency       string `gorm:"size:3;not null;default:CNY"`
	Total          int    `gorm:"not null;default:0"`
	Used           int    `gorm:"not null;default:0"`
	PerUserLimit   int    `gorm:"not null;default:1"`
	StartsAt       time.Time
	EndsAt         time.Time
	Enabled        bool `gorm:"not null;default:true"`
	CreatedAt      time.Time
}

func (Coupon) TableName() string { return "coupons" }

type CouponRedemption struct {
	ID            string `gorm:"primaryKey;size:26"`
	CouponID      string `gorm:"size:26;not null;uniqueIndex:uk_coupon_order"`
	UserID        string `gorm:"size:26;index;not null"`
	OrderID       string `gorm:"size:26;not null;uniqueIndex:uk_coupon_order"`
	DiscountMinor int64  `gorm:"not null"`
	CreatedAt     time.Time
}

func (CouponRedemption) TableName() string { return "coupon_redemptions" }

type Subscription struct {
	ID            string `gorm:"primaryKey;size:26"`
	UserID        string `gorm:"size:26;index;not null"`
	ProductID     string `gorm:"size:26;index;not null"`
	Status        string `gorm:"size:16;not null;default:active;index"`
	StartsAt      time.Time
	EndsAt        time.Time `gorm:"index"`
	CanceledAt    *time.Time
	SourceOrderID string `gorm:"size:26"`
	CreatedAt     time.Time
}

func (Subscription) TableName() string { return "billing_subscriptions" }

type Entitlement struct {
	ID           string `gorm:"primaryKey;size:26"`
	UserID       string `gorm:"size:26;not null;uniqueIndex:uk_entitlement"`
	ResourceType string `gorm:"size:24;not null;uniqueIndex:uk_entitlement"`
	ResourceID   string `gorm:"size:26;not null;uniqueIndex:uk_entitlement"`
	SourceType   string `gorm:"size:24"`
	SourceID     string `gorm:"size:26"`
	StartsAt     time.Time
	ExpiresAt    *time.Time `gorm:"index"`
	RevokedAt    *time.Time
	CreatedAt    time.Time
}

func (Entitlement) TableName() string { return "entitlements" }

type LedgerAccount struct {
	ID        string `gorm:"primaryKey;size:26"`
	OwnerType string `gorm:"size:16;not null;uniqueIndex:uk_ledger_account"`
	OwnerID   string `gorm:"size:26;not null;uniqueIndex:uk_ledger_account"`
	Currency  string `gorm:"size:3;not null;uniqueIndex:uk_ledger_account"`
	Kind      string `gorm:"size:16;not null;uniqueIndex:uk_ledger_account"`
	Balance   int64  `gorm:"not null;default:0"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (LedgerAccount) TableName() string { return "ledger_accounts" }

type LedgerTransaction struct {
	ID             string    `gorm:"primaryKey;size:26"`
	Type           string    `gorm:"size:24;not null"`
	Status         string    `gorm:"size:16;not null;default:posted"`
	IdempotencyKey string    `gorm:"size:80;uniqueIndex;not null"`
	RefType        string    `gorm:"size:24;index"`
	RefID          string    `gorm:"size:26;index"`
	Description    string    `gorm:"size:255"`
	CreatedAt      time.Time `gorm:"index"`
}

func (LedgerTransaction) TableName() string { return "ledger_transactions" }

type LedgerEntry struct {
	ID            string    `gorm:"primaryKey;size:26"`
	TransactionID string    `gorm:"size:26;index;not null"`
	AccountID     string    `gorm:"size:26;index;not null"`
	Direction     string    `gorm:"size:8;not null"`
	Amount        int64     `gorm:"not null"`
	BalanceAfter  int64     `gorm:"not null"`
	EntryType     string    `gorm:"size:24"`
	CreatedAt     time.Time `gorm:"index"`
}

func (LedgerEntry) TableName() string { return "ledger_entries" }

type ReconciliationRun struct {
	ID            string `gorm:"primaryKey;size:26"`
	ChannelCode   string `gorm:"size:24;index;not null"`
	BizDate       string `gorm:"size:10;index;not null"`
	Status        string `gorm:"size:16;not null;default:completed"`
	LocalAmount   int64  `gorm:"not null;default:0"`
	ChannelAmount int64  `gorm:"not null;default:0"`
	DiffCount     int    `gorm:"not null;default:0"`
	StartedAt     time.Time
	FinishedAt    *time.Time
}

func (ReconciliationRun) TableName() string { return "reconciliation_runs" }

type ReconciliationDiff struct {
	ID            string `gorm:"primaryKey;size:26"`
	RunID         string `gorm:"size:26;index;not null"`
	OutTradeNo    string `gorm:"size:40;index"`
	DiffType      string `gorm:"size:32;not null"`
	LocalAmount   int64  `gorm:"not null;default:0"`
	ChannelAmount int64  `gorm:"not null;default:0"`
	Resolved      bool   `gorm:"not null;default:false"`
	Note          string `gorm:"size:255"`
}

func (ReconciliationDiff) TableName() string { return "reconciliation_diffs" }

func (p *Product) BeforeCreate(*gorm.DB) error {
	if p.ID == "" {
		p.ID = id.New()
	}
	return nil
}
func (c *PaymentChannel) BeforeCreate(*gorm.DB) error {
	if c.ID == "" {
		c.ID = id.New()
	}
	return nil
}
func (o *Order) BeforeCreate(*gorm.DB) error {
	if o.ID == "" {
		o.ID = id.New()
	}
	return nil
}
func (t *PaymentTransaction) BeforeCreate(*gorm.DB) error {
	if t.ID == "" {
		t.ID = id.New()
	}
	return nil
}
func (n *NotifyLog) BeforeCreate(*gorm.DB) error {
	if n.ID == "" {
		n.ID = id.New()
	}
	return nil
}
func (r *Refund) BeforeCreate(*gorm.DB) error {
	if r.ID == "" {
		r.ID = id.New()
	}
	return nil
}
func (c *Coupon) BeforeCreate(*gorm.DB) error {
	if c.ID == "" {
		c.ID = id.New()
	}
	return nil
}
func (c *CouponRedemption) BeforeCreate(*gorm.DB) error {
	if c.ID == "" {
		c.ID = id.New()
	}
	return nil
}
func (s *Subscription) BeforeCreate(*gorm.DB) error {
	if s.ID == "" {
		s.ID = id.New()
	}
	return nil
}
func (e *Entitlement) BeforeCreate(*gorm.DB) error {
	if e.ID == "" {
		e.ID = id.New()
	}
	return nil
}
func (a *LedgerAccount) BeforeCreate(*gorm.DB) error {
	if a.ID == "" {
		a.ID = id.New()
	}
	return nil
}
func (t *LedgerTransaction) BeforeCreate(*gorm.DB) error {
	if t.ID == "" {
		t.ID = id.New()
	}
	return nil
}
func (e *LedgerEntry) BeforeCreate(*gorm.DB) error {
	if e.ID == "" {
		e.ID = id.New()
	}
	return nil
}
func (r *ReconciliationRun) BeforeCreate(*gorm.DB) error {
	if r.ID == "" {
		r.ID = id.New()
	}
	return nil
}
func (d *ReconciliationDiff) BeforeCreate(*gorm.DB) error {
	if d.ID == "" {
		d.ID = id.New()
	}
	return nil
}

func Models() []any {
	return []any{
		&Product{}, &PaymentChannel{}, &Order{}, &PaymentTransaction{}, &NotifyLog{},
		&Refund{}, &Coupon{}, &CouponRedemption{}, &Subscription{}, &Entitlement{},
		&LedgerAccount{}, &LedgerTransaction{}, &LedgerEntry{},
		&ReconciliationRun{}, &ReconciliationDiff{},
	}
}
