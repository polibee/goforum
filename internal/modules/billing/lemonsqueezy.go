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

type lemonSqueezyGateway struct {
	settings *settings.Store
	client   *http.Client
}

func newLemonSqueezyGateway(st *settings.Store) *lemonSqueezyGateway {
	return &lemonSqueezyGateway{settings: st, client: &http.Client{Timeout: 20 * time.Second}}
}

func (g *lemonSqueezyGateway) Driver() string { return "lemonsqueezy" }

func (g *lemonSqueezyGateway) baseURL() string {
	if g.settings != nil {
		if v := strings.TrimRight(g.settings.GetString("billing.lemonsqueezy.base_url"), "/"); v != "" {
			return v
		}
	}
	return "https://api.lemonsqueezy.com/v1"
}

func (g *lemonSqueezyGateway) variantID(slug string) string {
	if g.settings == nil || slug == "" {
		return ""
	}
	var mapping map[string]any
	if !g.settings.GetJSON("billing.lemonsqueezy.variants", &mapping) {
		return ""
	}
	if v, ok := mapping[slug]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func (g *lemonSqueezyGateway) Create(ctx context.Context, order Order) (*PayParams, error) {
	if g.settings == nil || g.settings.GetString("billing.lemonsqueezy.api_key") == "" {
		return nil, fmt.Errorf("Lemon Squeezy API Key 未配置")
	}
	storeID := strings.TrimSpace(g.settings.GetString("billing.lemonsqueezy.store_id"))
	if storeID == "" {
		return nil, fmt.Errorf("Lemon Squeezy Store ID 未配置")
	}
	variantID := g.variantID(order.ProductSlug)
	if variantID == "" {
		return nil, fmt.Errorf("商品 %s 未配置 Lemon Squeezy Variant ID", order.ProductSlug)
	}
	attributes := map[string]any{
		"checkout_data": map[string]any{
			"custom": map[string]string{"order_no": order.OutTradeNo},
		},
	}
	if base := strings.TrimRight(g.settings.GetString("site.general.url"), "/"); base != "" {
		attributes["product_options"] = map[string]any{
			"redirect_url": base + "/checkout/" + order.OutTradeNo + "?paid=1",
		}
	}
	payload := map[string]any{
		"data": map[string]any{
			"type":       "checkouts",
			"attributes": attributes,
			"relationships": map[string]any{
				"store": map[string]any{
					"data": map[string]any{"type": "stores", "id": storeID},
				},
				"variant": map[string]any{
					"data": map[string]any{"type": "variants", "id": variantID},
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL()+"/checkouts", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.settings.GetString("billing.lemonsqueezy.api_key"))
	req.Header.Set("Accept", "application/vnd.api+json")
	req.Header.Set("Content-Type", "application/vnd.api+json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Lemon Squeezy 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Lemon Squeezy 创建结账失败: %d %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Data struct {
			Attributes struct {
				URL string `json:"url"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("Lemon Squeezy 响应解析失败: %w", err)
	}
	if out.Data.Attributes.URL == "" {
		return nil, fmt.Errorf("Lemon Squeezy 未返回结账链接")
	}
	return &PayParams{Type: "redirect", URL: out.Data.Attributes.URL, Instructions: "跳转 Lemon Squeezy 完成支付"}, nil
}

func (g *lemonSqueezyGateway) Refund(context.Context, Order, Refund) (string, error) {
	// No API integration yet. Failing loudly is required: silently returning
	// success would credit the user's site balance while the customer keeps
	// their money at the provider.
	return "", httpx.NewError("refund.not_supported", 501,
		"该支付渠道暂未接入自动退款，请在渠道后台手动退款后再标记订单")
}

func (g *lemonSqueezyGateway) VerifyNotify(ctx context.Context, r *http.Request) (*NotifyEvent, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	secret := g.settings.GetString("billing.lemonsqueezy.webhook_secret")
	if secret == "" {
		return nil, fmt.Errorf("Lemon Squeezy Webhook Secret 未配置")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	expected := hex.EncodeToString(mac.Sum(nil))
	provided := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Signature")))
	if !hmac.Equal([]byte(expected), []byte(provided)) {
		return nil, fmt.Errorf("Lemon Squeezy 回调签名校验失败")
	}
	var payload struct {
		Meta struct {
			EventName  string            `json:"event_name"`
			CustomData map[string]string `json:"custom_data"`
		} `json:"meta"`
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				Status      string      `json:"status"`
				Total       json.Number `json:"total"`
				OrderNumber int64       `json:"order_number"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	total, _ := payload.Data.Attributes.Total.Int64()
	status := payload.Data.Attributes.Status
	if status == "" && payload.Meta.EventName == "order_created" {
		status = "paid"
	}
	return &NotifyEvent{
		EventID:        payload.Data.ID,
		OutTradeNo:     payload.Meta.CustomData["order_no"],
		ChannelTradeNo: payload.Data.ID,
		AmountMinor:    total,
		Currency:       "USD",
		Status:         status,
		Raw:            string(raw),
	}, nil
}

func (g *lemonSqueezyGateway) Ack(*NotifyEvent) (int, string) {
	return http.StatusOK, "ok"
}
