package billing

import (
	"context"
	"net/http"
)

type PayParams struct {
	Type         string // balance | manual | redirect | qr
	URL          string
	QRCode       string
	Instructions string
}

type NotifyEvent struct {
	EventID        string
	OutTradeNo     string
	ChannelTradeNo string
	AmountMinor    int64
	Currency       string
	Status         string
	Raw            string
}

type Gateway interface {
	Driver() string
	Create(ctx context.Context, order Order) (*PayParams, error)
	Refund(ctx context.Context, order Order, refund Refund) (string, error)
	VerifyNotify(ctx context.Context, r *http.Request) (*NotifyEvent, error)
	Ack(ev *NotifyEvent) (int, string)
}

// Poller is implemented by gateways without webhooks (e.g. Deipay).
type Poller interface {
	Poll(ctx context.Context) ([]NotifyEvent, error)
}

type balanceGateway struct{}

func (balanceGateway) Driver() string { return "balance" }
func (balanceGateway) Create(context.Context, Order) (*PayParams, error) {
	return &PayParams{Type: "balance", Instructions: "使用站内余额支付"}, nil
}
func (balanceGateway) Refund(context.Context, Order, Refund) (string, error) {
	return "", nil
}
func (balanceGateway) VerifyNotify(context.Context, *http.Request) (*NotifyEvent, error) {
	return nil, http.ErrNotSupported
}
func (balanceGateway) Ack(*NotifyEvent) (int, string) { return http.StatusOK, "ok" }

type manualGateway struct{}

func (manualGateway) Driver() string { return "manual" }
func (manualGateway) Create(context.Context, Order) (*PayParams, error) {
	return &PayParams{Type: "manual", Instructions: "请按站点说明完成转账，管理员确认后到账"}, nil
}
func (manualGateway) Refund(context.Context, Order, Refund) (string, error) {
	return "", nil
}
func (manualGateway) VerifyNotify(context.Context, *http.Request) (*NotifyEvent, error) {
	return nil, http.ErrNotSupported
}
func (manualGateway) Ack(*NotifyEvent) (int, string) { return http.StatusOK, "ok" }

type mockGateway struct {
	driver string
}

func (g mockGateway) Driver() string { return g.driver }
func (g mockGateway) Create(_ context.Context, order Order) (*PayParams, error) {
	return &PayParams{
		Type:         "redirect",
		URL:          "/payments/mock/" + order.OutTradeNo + "?channel=" + g.driver,
		Instructions: "沙箱渠道：点击后模拟支付成功",
	}, nil
}
func (g mockGateway) Refund(context.Context, Order, Refund) (string, error) {
	return "mock-refund", nil
}
func (g mockGateway) VerifyNotify(context.Context, *http.Request) (*NotifyEvent, error) {
	return nil, http.ErrNotSupported
}
func (g mockGateway) Ack(*NotifyEvent) (int, string) { return http.StatusOK, "ok" }

func (s *Service) gateway(driver string) (Gateway, bool) {
	switch driver {
	case "balance":
		return balanceGateway{}, true
	case "manual":
		return manualGateway{}, true
	case "alipay", "wechat":
		return mockGateway{driver: driver}, true
	case "nowpayments":
		return newNowPaymentsGateway(s.settings), true
	case "xcash":
		return newXCashGateway(s.settings), true
	case "deipay":
		return newDeipayGateway(s.settings), true
	case "creem":
		return newCreemGateway(s.settings), true
	case "lemonsqueezy":
		return newLemonSqueezyGateway(s.settings), true
	default:
		return nil, false
	}
}
