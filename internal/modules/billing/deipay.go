package billing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"

	"goforum/internal/platform/httpx"
	"goforum/internal/platform/settings"
)

type deipayGateway struct {
	settings *settings.Store
	client   *http.Client
}

func newDeipayGateway(st *settings.Store) *deipayGateway {
	return &deipayGateway{settings: st, client: &http.Client{Timeout: 20 * time.Second}}
}

func (g *deipayGateway) Driver() string { return "deipay" }

func (g *deipayGateway) baseURL() string {
	if g.settings != nil {
		if v := strings.TrimRight(g.settings.GetString("billing.deipay.base_url"), "/"); v != "" {
			return v
		}
	}
	return "https://api.depipay.com"
}

func (g *deipayGateway) sessionKey() (*secp256k1.PrivateKey, error) {
	if g.settings == nil {
		return nil, fmt.Errorf("Deipay Session Key 未配置")
	}
	raw := strings.TrimPrefix(strings.TrimSpace(g.settings.GetString("billing.deipay.session_key")), "0x")
	if raw == "" {
		return nil, fmt.Errorf("Deipay Session Key 未配置")
	}
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("Deipay Session Key 必须是 32 字节十六进制私钥")
	}
	return secp256k1.PrivKeyFromBytes(b), nil
}

// sign returns the millisecond nonce and EIP-191 signature over
// sess:{nonce}:{sha256(body)}.
func (g *deipayGateway) sign(body []byte) (string, string, error) {
	key, err := g.sessionKey()
	if err != nil {
		return "", "", err
	}
	nonce := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sum := sha256.Sum256(body)
	message := []byte("sess:" + nonce + ":" + hex.EncodeToString(sum[:]))
	digest := eip191Digest(message)
	compact := ecdsa.SignCompact(key, digest, false)
	if len(compact) != 65 {
		return "", "", fmt.Errorf("Deipay 签名失败")
	}
	sig := hex.EncodeToString(compact[1:]) + hex.EncodeToString(compact[:1])
	return nonce, sig, nil
}

func eip191Digest(message []byte) []byte {
	prefix := fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len(message))
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(prefix))
	h.Write(message)
	return h.Sum(nil)
}

func (g *deipayGateway) do(ctx context.Context, path string, body []byte) ([]byte, int, error) {
	nonce, sig, err := g.sign(body)
	if err != nil {
		return nil, 0, err
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL()+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("x-session-nonce", nonce)
	req.Header.Set("x-session-signature", sig)
	req.Header.Set("x-encryption", "none")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("Deipay 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return raw, resp.StatusCode, fmt.Errorf("Deipay 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, resp.StatusCode, nil
}

func (g *deipayGateway) Create(ctx context.Context, order Order) (*PayParams, error) {
	if _, err := g.sessionKey(); err != nil {
		return nil, err
	}
	chainID := int64(1)
	tokens := ""
	deadline := int64(86400)
	if g.settings != nil {
		if v := g.settings.GetInt("billing.deipay.chain_id"); v > 0 {
			chainID = v
		}
		tokens = g.settings.GetString("billing.deipay.tokens")
		if v := g.settings.GetInt("billing.deipay.deadline_secs"); v >= 300 {
			deadline = v
		}
	}
	accepted := make([]string, 0)
	for _, token := range strings.Split(tokens, ",") {
		if token = strings.TrimSpace(token); token != "" {
			accepted = append(accepted, token)
		}
	}
	if len(accepted) == 0 {
		return nil, fmt.Errorf("Deipay 接收代币未配置")
	}
	payload := map[string]any{
		"value":          fmt.Sprintf("%.2f", float64(order.AmountMinor)/100),
		"chainId":        chainID,
		"description":    order.ProductName,
		"acceptedTokens": accepted,
		"deadlineSecs":   deadline,
		"data": map[string]string{
			"order_no": order.OutTradeNo,
			"order_id": order.ID,
		},
	}
	body, _ := json.Marshal(payload)
	raw, _, err := g.do(ctx, "/invoice", body)
	if err != nil {
		return nil, err
	}
	var out struct {
		PaymentURL string `json:"paymentUrl"`
		GUID       string `json:"guid"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("Deipay 响应解析失败: %w", err)
	}
	if out.PaymentURL == "" {
		return nil, fmt.Errorf("Deipay 未返回支付链接")
	}
	return &PayParams{Type: "redirect", URL: out.PaymentURL, Instructions: "跳转 Deipay 完成加密货币支付"}, nil
}

func (g *deipayGateway) Refund(context.Context, Order, Refund) (string, error) {
	// No API integration yet. Failing loudly is required: silently returning
	// success would credit the user's site balance while the customer keeps
	// their money at the provider.
	return "", httpx.NewError("refund.not_supported", 501,
		"该支付渠道暂未接入自动退款，请在渠道后台手动退款后再标记订单")
}

func (g *deipayGateway) VerifyNotify(context.Context, *http.Request) (*NotifyEvent, error) {
	return nil, http.ErrNotSupported
}

func (g *deipayGateway) Ack(*NotifyEvent) (int, string) {
	return http.StatusOK, "ok"
}

type deipayEvent struct {
	GUID       string         `json:"guid"`
	Status     string         `json:"status"`
	Value      string         `json:"value"`
	PaidAmount string         `json:"paidAmount"`
	Data       map[string]any `json:"data"`
	Payments   []struct {
		TxHash string `json:"txHash"`
	} `json:"payments"`
}

func (g *deipayGateway) Poll(ctx context.Context) ([]NotifyEvent, error) {
	raw, _, err := g.do(ctx, "/poll/events", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Events []deipayEvent `json:"events"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("Deipay 事件解析失败: %w", err)
	}
	events := make([]NotifyEvent, 0, len(out.Events))
	for _, ev := range out.Events {
		orderNo := ""
		if ev.Data != nil {
			if s, ok := ev.Data["order_no"].(string); ok {
				orderNo = s
			}
		}
		if orderNo == "" {
			continue
		}
		tradeNo := ev.GUID
		if len(ev.Payments) > 0 && ev.Payments[0].TxHash != "" {
			tradeNo = ev.Payments[0].TxHash
		}
		events = append(events, NotifyEvent{
			EventID:        ev.GUID,
			OutTradeNo:     orderNo,
			ChannelTradeNo: tradeNo,
			Currency:       "USD",
			Status:         ev.Status,
		})
	}
	return events, nil
}
