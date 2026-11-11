package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
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

type creemGateway struct {
	settings *settings.Store
	client   *http.Client
}

func newCreemGateway(st *settings.Store) *creemGateway {
	return &creemGateway{settings: st, client: &http.Client{Timeout: 20 * time.Second}}
}

func (g *creemGateway) Driver() string { return "creem" }

func (g *creemGateway) baseURL() string {
	if g.settings != nil {
		if v := strings.TrimRight(g.settings.GetString("billing.creem.base_url"), "/"); v != "" {
			return v
		}
	}
	return "https://api.creem.io/v1"
}

func (g *creemGateway) productID(slug string) string {
	if g.settings == nil || slug == "" {
		return ""
	}
	var mapping map[string]any
	if !g.settings.GetJSON("billing.creem.products", &mapping) {
		return ""
	}
	if v, ok := mapping[slug]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func (g *creemGateway) Create(ctx context.Context, order Order) (*PayParams, error) {
	if g.settings == nil || g.settings.GetString("billing.creem.api_key") == "" {
		return nil, fmt.Errorf("Creem API Key 未配置")
	}
	productID := g.productID(order.ProductSlug)
	if productID == "" {
		return nil, fmt.Errorf("商品 %s 未配置 Creem Product ID", order.ProductSlug)
	}
	payload := map[string]any{
		"product_id": productID,
		"request_id": order.OutTradeNo,
	}
	if base := strings.TrimRight(g.settings.GetString("site.general.url"), "/"); base != "" {
		payload["success_url"] = base + "/checkout/" + order.OutTradeNo + "?paid=1"
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL()+"/checkouts", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", g.settings.GetString("billing.creem.api_key"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Creem 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Creem 创建结账失败: %d %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		CheckoutURL string `json:"checkout_url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("Creem 响应解析失败: %w", err)
	}
	if out.CheckoutURL == "" {
		return nil, fmt.Errorf("Creem 未返回结账链接")
	}
	return &PayParams{Type: "redirect", URL: out.CheckoutURL, Instructions: "跳转 Creem 完成支付"}, nil
}

func (g *creemGateway) Refund(context.Context, Order, Refund) (string, error) {
	// No API integration yet. Failing loudly is required: silently returning
	// success would credit the user's site balance while the customer keeps
	// their money at the provider.
	return "", httpx.NewError("refund.not_supported", 501,
		"该支付渠道暂未接入自动退款，请在渠道后台手动退款后再标记订单")
}

func (g *creemGateway) VerifyNotify(ctx context.Context, r *http.Request) (*NotifyEvent, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	secret := g.settings.GetString("billing.creem.webhook_secret")
	if secret == "" {
		return nil, fmt.Errorf("Creem Webhook Secret 未配置")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	expected := hex.EncodeToString(mac.Sum(nil))
	provided := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("creem-signature"))), "sha256=")
	if !hmac.Equal([]byte(expected), []byte(provided)) {
		return nil, fmt.Errorf("Creem 回调签名校验失败")
	}
	var payload struct {
		ID        string `json:"id"`
		EventType string `json:"eventType"`
		Object    struct {
			RequestID string `json:"request_id"`
			Status    string `json:"status"`
			Metadata  struct {
				OrderNo string `json:"order_no"`
			} `json:"metadata"`
			Order struct {
				ID     string      `json:"id"`
				Status string      `json:"status"`
				Amount json.Number `json:"amount"`
			} `json:"order"`
		} `json:"object"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	orderNo := payload.Object.RequestID
	if orderNo == "" {
		orderNo = payload.Object.Metadata.OrderNo
	}
	status := payload.Object.Order.Status
	if status == "" {
		status = payload.Object.Status
	}
	amount, _ := payload.Object.Order.Amount.Int64()
	event := &NotifyEvent{
		EventID:        payload.ID,
		OutTradeNo:     orderNo,
		ChannelTradeNo: payload.Object.Order.ID,
		AmountMinor:    amount,
		Currency:       "USD",
		Status:         status,
		Raw:            string(raw),
	}
	if payload.EventType == "checkout.completed" && event.Status == "" {
		event.Status = "paid"
	}
	return event, nil
}

func (g *creemGateway) Ack(*NotifyEvent) (int, string) {
	return http.StatusOK, "ok"
}
