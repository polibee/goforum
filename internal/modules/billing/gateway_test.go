package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/platform/settings"
)

func testSettings(t *testing.T, values map[string]any) *settings.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "settings.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(settings.Models()...); err != nil {
		t.Fatal(err)
	}
	reg := settings.NewRegistry()
	if err := reg.Register(settings.PlatformDefinitions()...); err != nil {
		t.Fatal(err)
	}
	st, err := settings.NewStore(db, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSecretKey("test-encryption-key"); err != nil {
		t.Fatal(err)
	}
	if len(values) > 0 {
		if err := st.SetMany(context.Background(), values, "test"); err != nil {
			t.Fatalf("set settings: %v", err)
		}
	}
	return st
}

func TestNowPaymentsCreateInvoice(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/invoice" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "np-key" {
			t.Fatalf("api key = %q", r.Header.Get("x-api-key"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"invoice_url":"https://nowpayments.example/i/1"}`))
	}))
	defer srv.Close()

	st := testSettings(t, map[string]any{
		"billing.nowpayments.api_key":  "np-key",
		"billing.nowpayments.base_url": srv.URL,
		"site.general.url":             "https://forum.example.com",
	})
	gw := newNowPaymentsGateway(st)
	params, err := gw.Create(context.Background(), Order{
		OutTradeNo: "GF1", AmountMinor: 1999, ProductName: "VIP",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if params.URL != "https://nowpayments.example/i/1" {
		t.Fatalf("url = %s", params.URL)
	}
	if got["order_id"] != "GF1" || got["price_amount"].(float64) != 19.99 {
		t.Fatalf("payload = %+v", got)
	}
	if got["ipn_callback_url"] != "https://forum.example.com/payments/nowpayments/notify" {
		t.Fatalf("ipn = %v", got["ipn_callback_url"])
	}
}

func TestNowPaymentsVerifyNotify(t *testing.T) {
	st := testSettings(t, map[string]any{
		"billing.nowpayments.ipn_secret": "secret",
	})
	gw := newNowPaymentsGateway(st)

	payload := map[string]any{
		"payment_id":     json.Number("123"),
		"order_id":       "GF1",
		"payment_status": "finished",
		"price_amount":   19.99,
		"pay_currency":   "usdttrc20",
	}
	raw, _ := json.Marshal(payload)
	canonical, err := canonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha512.New, []byte("secret"))
	mac.Write(canonical)
	sig := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/payments/nowpayments/notify", bytes.NewReader(raw))
	req.Header.Set("x-nowpayments-sig", sig)
	ev, err := gw.VerifyNotify(context.Background(), req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ev.OutTradeNo != "GF1" || ev.Status != "finished" || ev.AmountMinor != 1999 {
		t.Fatalf("event = %+v", ev)
	}

	bad := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	bad.Header.Set("x-nowpayments-sig", "deadbeef")
	if _, err := gw.VerifyNotify(context.Background(), bad); err == nil {
		t.Fatal("invalid signature accepted")
	}
}

func TestDeipaySignatureRecoversSessionKey(t *testing.T) {
	key, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	st := testSettings(t, map[string]any{
		"billing.deipay.session_key": hex.EncodeToString(key.Serialize()),
	})
	gw := newDeipayGateway(st)

	nonce, sigHex, err := gw.sign(nil)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(sigHex) != 130 {
		t.Fatalf("signature length = %d", len(sigHex))
	}
	sig, _ := hex.DecodeString(sigHex)
	compact := append([]byte{sig[64]}, sig[:64]...)
	sum := sha256.Sum256(nil)
	digest := eip191Digest([]byte("sess:" + nonce + ":" + hex.EncodeToString(sum[:])))
	pub, _, err := ecdsa.RecoverCompact(compact, digest)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !bytes.Equal(pub.SerializeCompressed(), key.PubKey().SerializeCompressed()) {
		t.Fatal("recovered key mismatch")
	}
}

func TestDeipayCreateInvoice(t *testing.T) {
	key, _ := secp256k1.GeneratePrivateKey()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/invoice" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-session-nonce") == "" || r.Header.Get("x-session-signature") == "" {
			t.Fatal("missing session headers")
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"guid":"g1","paymentUrl":"https://app.depipay.com/?invoice=g1","status":"init"}`))
	}))
	defer srv.Close()

	st := testSettings(t, map[string]any{
		"billing.deipay.session_key": hex.EncodeToString(key.Serialize()),
		"billing.deipay.base_url":    srv.URL,
		"billing.deipay.chain_id":    1,
		"billing.deipay.tokens":      "1:0xdac17f958d2ee523a2206206994597c13d831ec7",
	})
	gw := newDeipayGateway(st)
	params, err := gw.Create(context.Background(), Order{
		ID: "o1", OutTradeNo: "GF1", AmountMinor: 2500, ProductName: "VIP",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if params.URL == "" || got["value"] != "25.00" {
		t.Fatalf("params=%+v body=%+v", params, got)
	}
	data, _ := got["data"].(map[string]any)
	if data["order_no"] != "GF1" {
		t.Fatalf("data = %+v", got["data"])
	}
}

func TestNowPaymentsNotifyMarksOrderPaid(t *testing.T) {
	svc := setup(t)
	st := testSettings(t, map[string]any{
		"billing.nowpayments.ipn_secret": "secret",
	})
	svc.Configure(st, nil, nil, nil, nil)

	order := Order{
		OutTradeNo: "GFN1", UserID: "u1", ProductID: "p1", ProductType: "points_pack",
		ProductName: "积分包", AmountMinor: 1000, Currency: "CNY",
		ChannelCode: "nowpayments", Status: "pending",
	}
	if err := svc.db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"payment_id": 42, "order_id": "GFN1", "payment_status": "finished",
		"price_amount": 10, "pay_currency": "usdttrc20",
	}
	raw, _ := json.Marshal(payload)
	canonical, _ := canonicalJSON(raw)
	mac := hmac.New(sha512.New, []byte("secret"))
	mac.Write(canonical)
	req := httptest.NewRequest(http.MethodPost, "/payments/nowpayments/notify", bytes.NewReader(raw))
	req.Header.Set("x-nowpayments-sig", hex.EncodeToString(mac.Sum(nil)))

	status, body := svc.HandleNotify(context.Background(), "nowpayments", req)
	if status != http.StatusOK || body != "ok" {
		t.Fatalf("notify = %d/%s", status, body)
	}
	var reloaded Order
	if err := svc.db.First(&reloaded, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != "paid" {
		t.Fatalf("status = %s", reloaded.Status)
	}
	bal, _ := svc.UserBalance(context.Background(), "u1")
	if bal != 0 {
		t.Fatalf("balance = %d", bal)
	}
	var revenue LedgerAccount
	_ = svc.db.Where("owner_type = ? AND owner_id = ? AND kind = ?", "system", "revenue", "revenue").First(&revenue).Error
	if revenue.Balance != 1000 {
		t.Fatalf("revenue = %d", revenue.Balance)
	}
}

func TestCreemCreateAndNotify(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "creem-key" {
			t.Fatalf("api key = %q", r.Header.Get("x-api-key"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"checkout_url":"https://creem.example/c/1"}`))
	}))
	defer srv.Close()

	st := testSettings(t, map[string]any{
		"billing.creem.api_key":  "creem-key",
		"billing.creem.base_url": srv.URL,
		"billing.creem.products": map[string]any{"vip-30": "prod_1"},
		"site.general.url":       "https://forum.example.com",
	})
	gw := newCreemGateway(st)
	params, err := gw.Create(context.Background(), Order{
		OutTradeNo: "GF1", ProductSlug: "vip-30", AmountMinor: 1999, ProductName: "VIP",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if params.URL != "https://creem.example/c/1" || got["product_id"] != "prod_1" || got["request_id"] != "GF1" {
		t.Fatalf("params=%+v body=%+v", params, got)
	}

	payload := []byte(`{"id":"evt_1","eventType":"checkout.completed","object":{"request_id":"GF1","order":{"id":"ord_1","status":"paid","amount":1999}}}`)
	mac := hmac.New(sha256.New, []byte("creem-secret"))
	mac.Write(payload)
	st2 := testSettings(t, map[string]any{"billing.creem.webhook_secret": "creem-secret"})
	gw2 := newCreemGateway(st2)
	req := httptest.NewRequest(http.MethodPost, "/payments/creem/notify", bytes.NewReader(payload))
	req.Header.Set("creem-signature", hex.EncodeToString(mac.Sum(nil)))
	ev, err := gw2.VerifyNotify(context.Background(), req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ev.OutTradeNo != "GF1" || ev.Status != "paid" || ev.AmountMinor != 1999 {
		t.Fatalf("event = %+v", ev)
	}
}

func TestLemonSqueezyCreateAndNotify(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ls-key" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"data":{"attributes":{"url":"https://ls.example/c/1"}}}`))
	}))
	defer srv.Close()

	st := testSettings(t, map[string]any{
		"billing.lemonsqueezy.api_key":  "ls-key",
		"billing.lemonsqueezy.base_url": srv.URL,
		"billing.lemonsqueezy.store_id": "store_1",
		"billing.lemonsqueezy.variants": map[string]any{"vip-30": "var_1"},
		"site.general.url":              "https://forum.example.com",
	})
	gw := newLemonSqueezyGateway(st)
	params, err := gw.Create(context.Background(), Order{
		OutTradeNo: "GF1", ProductSlug: "vip-30", AmountMinor: 1999, ProductName: "VIP",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if params.URL != "https://ls.example/c/1" {
		t.Fatalf("url = %s", params.URL)
	}
	data := got["data"].(map[string]any)
	rel := data["relationships"].(map[string]any)
	if rel["store"].(map[string]any)["data"].(map[string]any)["id"] != "store_1" {
		t.Fatalf("store = %+v", rel["store"])
	}
	attrs := data["attributes"].(map[string]any)
	custom := attrs["checkout_data"].(map[string]any)["custom"].(map[string]any)
	if custom["order_no"] != "GF1" {
		t.Fatalf("custom = %+v", custom)
	}

	payload := []byte(`{"meta":{"event_name":"order_created","custom_data":{"order_no":"GF1"}},"data":{"id":"ord_1","attributes":{"status":"paid","total":1999}}}`)
	mac := hmac.New(sha256.New, []byte("ls-secret"))
	mac.Write(payload)
	st2 := testSettings(t, map[string]any{"billing.lemonsqueezy.webhook_secret": "ls-secret"})
	gw2 := newLemonSqueezyGateway(st2)
	req := httptest.NewRequest(http.MethodPost, "/payments/lemonsqueezy/notify", bytes.NewReader(payload))
	req.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	ev, err := gw2.VerifyNotify(context.Background(), req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ev.OutTradeNo != "GF1" || ev.Status != "paid" || ev.AmountMinor != 1999 {
		t.Fatalf("event = %+v", ev)
	}
}
