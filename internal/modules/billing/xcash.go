package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goforum/internal/platform/httpx"
	"goforum/internal/platform/settings"
)

type xcashGateway struct {
	settings *settings.Store
	client   *http.Client
}

func newXCashGateway(st *settings.Store) *xcashGateway {
	return &xcashGateway{settings: st, client: &http.Client{Timeout: 20 * time.Second}}
}

func (g *xcashGateway) Driver() string { return "xcash" }

func (g *xcashGateway) appid() string {
	if g.settings == nil {
		return ""
	}
	return strings.TrimSpace(g.settings.GetString("billing.xcash.appid"))
}

func (g *xcashGateway) hmacKey() string {
	if g.settings == nil {
		return ""
	}
	return g.settings.GetString("billing.xcash.hmac_key")
}

func (g *xcashGateway) apiBase() string {
	if g.settings != nil {
		if v := strings.TrimRight(g.settings.GetString("billing.xcash.base_url"), "/"); v != "" {
			return v
		}
	}
	return "https://pay.xca.sh"
}

func (g *xcashGateway) siteBase() string {
	if g.settings == nil {
		return ""
	}
	return strings.TrimRight(g.settings.GetString("site.general.url"), "/")
}

func (g *xcashGateway) duration() int {
	if g.settings != nil {
		if v := int(g.settings.GetInt("billing.xcash.duration")); v >= 5 && v <= 30 {
			return v
		}
	}
	return 15
}

func (g *xcashGateway) Create(ctx context.Context, order Order) (*PayParams, error) {
	appid, key := g.appid(), g.hmacKey()
	if appid == "" || key == "" {
		return nil, fmt.Errorf("XCash AppID 或 HMAC Key 未配置")
	}
	base := g.siteBase()
	if base == "" {
		return nil, fmt.Errorf("站点 URL 未配置，无法接收 XCash 回调")
	}
	if order.AmountMinor <= 0 {
		return nil, fmt.Errorf("XCash 不支持零金额订单")
	}
	if order.AmountMinor > 100_000_000 {
		return nil, fmt.Errorf("XCash 单笔金额超过上限")
	}
	payload := map[string]any{
		"out_no":     order.OutTradeNo,
		"title":      truncateRunes(order.ProductName, 32),
		"currency":   strings.ToUpper(order.Currency),
		"amount":     formatMinorAmount(order.AmountMinor),
		"duration":   g.duration(),
		"notify_url": base + "/payments/xcash/notify",
		"return_url": base + "/checkout/" + order.OutTradeNo + "?paid=1",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	nonce, err := xcashNonce()
	if err != nil {
		return nil, err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.apiBase()+"/v1/invoice", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("XC-Appid", appid)
	req.Header.Set("XC-Timestamp", timestamp)
	req.Header.Set("XC-Nonce", nonce)
	req.Header.Set("XC-Signature", xcashSignature(nonce, timestamp, body, key))
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("XCash 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("XCash 创建账单失败: %d %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		PayURL string `json:"pay_url"`
		SysNo  string `json:"sys_no"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("XCash 响应解析失败: %w", err)
	}
	if out.PayURL == "" {
		return nil, fmt.Errorf("XCash 未返回支付链接")
	}
	return &PayParams{Type: "redirect", URL: out.PayURL, Instructions: "跳转 XCash 完成加密货币支付"}, nil
}

func (g *xcashGateway) Refund(context.Context, Order, Refund) (string, error) {
	return "", httpx.NewError("refund.not_supported", 501,
		"该支付渠道暂未接入自动退款，请在 XCash 后台手动退款后再标记订单")
}

func (g *xcashGateway) VerifyNotify(_ context.Context, r *http.Request) (*NotifyEvent, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	appid, key := g.appid(), g.hmacKey()
	if appid == "" || key == "" {
		return nil, fmt.Errorf("XCash AppID 或 HMAC Key 未配置")
	}
	headerAppid := strings.TrimSpace(r.Header.Get("XC-Appid"))
	if headerAppid != appid {
		return nil, fmt.Errorf("XCash AppID 不匹配")
	}
	nonce := strings.TrimSpace(r.Header.Get("XC-Nonce"))
	timestamp := strings.TrimSpace(r.Header.Get("XC-Timestamp"))
	provided := strings.ToLower(strings.TrimSpace(r.Header.Get("XC-Signature")))
	if nonce == "" || timestamp == "" || provided == "" {
		return nil, fmt.Errorf("XCash 回调缺少签名头")
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("XCash 时间戳无效")
	}
	skew := time.Now().Unix() - ts
	if skew < -300 || skew > 300 {
		return nil, fmt.Errorf("XCash 回调时间戳已过期")
	}
	expected := xcashSignature(nonce, timestamp, raw, key)
	if !hmac.Equal([]byte(expected), []byte(provided)) {
		return nil, fmt.Errorf("回调签名校验失败")
	}
	var payload struct {
		Type string `json:"type"`
		Data struct {
			SysNo     string `json:"sys_no"`
			OutNo     string `json:"out_no"`
			Crypto    string `json:"crypto"`
			Chain     string `json:"chain"`
			Hash      string `json:"hash"`
			Confirmed bool   `json:"confirmed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.Type != "invoice" {
		// Deposits and other event types are acknowledged but do not map to a
		// product order.
		return &NotifyEvent{EventID: payload.Data.SysNo, Status: "ignored", Raw: string(raw)}, nil
	}
	eventID := payload.Data.SysNo
	if eventID == "" {
		eventID = payload.Data.Hash
	}
	if eventID == "" {
		eventID = nonce
	}
	status := "waiting"
	if payload.Data.Confirmed {
		status = "confirmed"
	}
	return &NotifyEvent{
		EventID:        eventID,
		OutTradeNo:     payload.Data.OutNo,
		ChannelTradeNo: eventID,
		Currency:       strings.ToUpper(payload.Data.Crypto),
		Status:         status,
		Raw:            string(raw),
	}, nil
}

func (g *xcashGateway) Ack(*NotifyEvent) (int, string) {
	return http.StatusOK, "ok"
}

func xcashSignature(nonce, timestamp string, body []byte, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(nonce))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func xcashNonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func truncateRunes(s string, n int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) > n {
		runes = runes[:n]
	}
	return string(runes)
}

func formatMinorAmount(minor int64) string {
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}
