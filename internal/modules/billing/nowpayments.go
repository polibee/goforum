package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"goforum/internal/platform/httpx"
	"goforum/internal/platform/settings"
)

type nowPaymentsGateway struct {
	settings *settings.Store
	client   *http.Client
}

func newNowPaymentsGateway(st *settings.Store) *nowPaymentsGateway {
	return &nowPaymentsGateway{settings: st, client: &http.Client{Timeout: 20 * time.Second}}
}

func (g *nowPaymentsGateway) Driver() string { return "nowpayments" }

func (g *nowPaymentsGateway) apiBase() string {
	if g.settings != nil {
		if v := strings.TrimRight(g.settings.GetString("billing.nowpayments.base_url"), "/"); v != "" {
			return v
		}
	}
	return "https://api.nowpayments.io/v1"
}

func (g *nowPaymentsGateway) apiKey() string {
	if g.settings == nil {
		return ""
	}
	return g.settings.GetString("billing.nowpayments.api_key")
}

func (g *nowPaymentsGateway) ipnSecret() string {
	if g.settings == nil {
		return ""
	}
	return g.settings.GetString("billing.nowpayments.ipn_secret")
}

func (g *nowPaymentsGateway) siteURL() string {
	if g.settings == nil {
		return ""
	}
	return strings.TrimRight(g.settings.GetString("site.general.url"), "/")
}

func (g *nowPaymentsGateway) Create(ctx context.Context, order Order) (*PayParams, error) {
	if g.apiKey() == "" {
		return nil, fmt.Errorf("NOWPayments API Key 未配置")
	}
	base := g.siteURL()
	if base == "" {
		return nil, fmt.Errorf("站点 URL 未配置，无法接收 NOWPayments 回调")
	}
	payload := map[string]any{
		"price_amount":      float64(order.AmountMinor) / 100,
		"price_currency":    "usd",
		"order_id":          order.OutTradeNo,
		"order_description": order.ProductName,
		"ipn_callback_url":  base + "/payments/nowpayments/notify",
		"success_url":       base + "/checkout/" + order.OutTradeNo + "?paid=1",
		"cancel_url":        base + "/checkout/" + order.OutTradeNo,
	}
	if pay := strings.TrimSpace(g.settings.GetString("billing.nowpayments.pay_currency")); pay != "" {
		payload["pay_currency"] = strings.ToLower(pay)
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.apiBase()+"/invoice", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", g.apiKey())
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("NOWPayments 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("NOWPayments 创建发票失败: %d %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		InvoiceURL string `json:"invoice_url"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("NOWPayments 响应解析失败: %w", err)
	}
	if out.InvoiceURL == "" {
		return nil, fmt.Errorf("NOWPayments 未返回支付链接")
	}
	return &PayParams{Type: "redirect", URL: out.InvoiceURL, Instructions: "跳转 NOWPayments 完成加密货币支付"}, nil
}

func (g *nowPaymentsGateway) Refund(context.Context, Order, Refund) (string, error) {
	// No API integration yet. Failing loudly is required: silently returning
	// success would credit the user's site balance while the customer keeps
	// their money at the provider.
	return "", httpx.NewError("refund.not_supported", 501,
		"该支付渠道暂未接入自动退款，请在渠道后台手动退款后再标记订单")
}

func (g *nowPaymentsGateway) VerifyNotify(ctx context.Context, r *http.Request) (*NotifyEvent, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	secret := g.ipnSecret()
	if secret == "" {
		return nil, fmt.Errorf("NOWPayments IPN Secret 未配置")
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("回调 JSON 无效: %w", err)
	}
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(canonical)
	expected := hex.EncodeToString(mac.Sum(nil))
	provided := strings.ToLower(strings.TrimSpace(r.Header.Get("x-nowpayments-sig")))
	if !hmac.Equal([]byte(expected), []byte(provided)) {
		return nil, fmt.Errorf("回调签名校验失败")
	}
	var payload struct {
		PaymentID     json.Number `json:"payment_id"`
		OrderID       string      `json:"order_id"`
		PaymentStatus string      `json:"payment_status"`
		PriceAmount   json.Number `json:"price_amount"`
		PayCurrency   string      `json:"pay_currency"`
		ActuallyPaid  json.Number `json:"actually_paid"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	amount, _ := payload.PriceAmount.Float64()
	paymentID := payload.PaymentID.String()
	return &NotifyEvent{
		EventID:        paymentID,
		OutTradeNo:     payload.OrderID,
		ChannelTradeNo: paymentID,
		AmountMinor:    int64(amount*100 + 0.5),
		Currency:       strings.ToUpper(payload.PayCurrency),
		Status:         payload.PaymentStatus,
		Raw:            string(raw),
	}, nil
}

func (g *nowPaymentsGateway) Ack(*NotifyEvent) (int, string) {
	return http.StatusOK, "ok"
}

// canonicalJSON re-encodes JSON with sorted object keys while preserving number
// literals, which is what NOWPayments signs.
func canonicalJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
