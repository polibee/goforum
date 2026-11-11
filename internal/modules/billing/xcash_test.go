package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestXCashCreateInvoice(t *testing.T) {
	var got map[string]any
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/invoice" {
			t.Errorf("path = %s", r.URL.Path)
		}
		headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("payload: %v", err)
		}
		want := xcashSignature(headers.Get("XC-Nonce"), headers.Get("XC-Timestamp"), raw, "hmac-secret")
		if headers.Get("XC-Signature") != want {
			t.Errorf("signature = %q, want %q", headers.Get("XC-Signature"), want)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"pay_url":"https://pay.xca.sh/pay/INV1","sys_no":"INV1"}`))
	}))
	defer srv.Close()

	st := testSettings(t, map[string]any{
		"billing.xcash.appid":    "XC-TEST",
		"billing.xcash.hmac_key": "hmac-secret",
		"billing.xcash.base_url": srv.URL,
		"billing.xcash.duration": 20,
		"site.general.url":       "https://forum.test",
	})
	gw := newXCashGateway(st)
	params, err := gw.Create(context.Background(), Order{
		OutTradeNo: "ORDER1", ProductName: "Premium Plan", AmountMinor: 2999, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if params.URL != "https://pay.xca.sh/pay/INV1" || params.Type != "redirect" {
		t.Fatalf("params = %+v", params)
	}
	if headers.Get("XC-Appid") != "XC-TEST" || headers.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %+v", headers)
	}
	if got["out_no"] != "ORDER1" || got["amount"] != "29.99" || got["currency"] != "USD" {
		t.Fatalf("payload = %+v", got)
	}
	if got["notify_url"] != "https://forum.test/payments/xcash/notify" {
		t.Fatalf("notify_url = %v", got["notify_url"])
	}
	if got["return_url"] != "https://forum.test/checkout/ORDER1?paid=1" {
		t.Fatalf("return_url = %v", got["return_url"])
	}
	if got["duration"] != float64(20) {
		t.Fatalf("duration = %v", got["duration"])
	}
}

func TestXCashVerifyNotify(t *testing.T) {
	st := testSettings(t, map[string]any{
		"billing.xcash.appid":    "XC-TEST",
		"billing.xcash.hmac_key": "hmac-secret",
	})
	gw := newXCashGateway(st)
	body := []byte(`{"type":"invoice","data":{"sys_no":"INV1","out_no":"ORDER1","crypto":"USDT","chain":"ethereum","hash":"0xabc","confirmed":true}}`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "nonce-1"
	req := httptest.NewRequest(http.MethodPost, "/payments/xcash/notify", bytes.NewReader(body))
	req.Header.Set("XC-Appid", "XC-TEST")
	req.Header.Set("XC-Timestamp", timestamp)
	req.Header.Set("XC-Nonce", nonce)
	req.Header.Set("XC-Signature", xcashSignature(nonce, timestamp, body, "hmac-secret"))
	ev, err := gw.VerifyNotify(context.Background(), req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ev.OutTradeNo != "ORDER1" || ev.ChannelTradeNo != "INV1" || ev.Status != "confirmed" || ev.Currency != "USDT" {
		t.Fatalf("event = %+v", ev)
	}
	if code, ack := gw.Ack(ev); code != http.StatusOK || ack != "ok" {
		t.Fatalf("ack = %d %q", code, ack)
	}
}

func TestXCashRejectsBadSignatureAndStaleTimestamp(t *testing.T) {
	st := testSettings(t, map[string]any{
		"billing.xcash.appid":    "XC-TEST",
		"billing.xcash.hmac_key": "hmac-secret",
	})
	gw := newXCashGateway(st)
	body := []byte(`{"type":"invoice","data":{"out_no":"ORDER1","confirmed":true}}`)

	bad := httptest.NewRequest(http.MethodPost, "/payments/xcash/notify", bytes.NewReader(body))
	bad.Header.Set("XC-Appid", "XC-TEST")
	bad.Header.Set("XC-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	bad.Header.Set("XC-Nonce", "nonce")
	bad.Header.Set("XC-Signature", "bad")
	if _, err := gw.VerifyNotify(context.Background(), bad); err == nil {
		t.Fatal("bad signature accepted")
	}

	staleTS := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	stale := httptest.NewRequest(http.MethodPost, "/payments/xcash/notify", bytes.NewReader(body))
	stale.Header.Set("XC-Appid", "XC-TEST")
	stale.Header.Set("XC-Timestamp", staleTS)
	stale.Header.Set("XC-Nonce", "nonce")
	stale.Header.Set("XC-Signature", xcashSignature("nonce", staleTS, body, "hmac-secret"))
	if _, err := gw.VerifyNotify(context.Background(), stale); err == nil {
		t.Fatal("stale timestamp accepted")
	}
}

func TestXCashIgnoresDepositEvents(t *testing.T) {
	st := testSettings(t, map[string]any{
		"billing.xcash.appid":    "XC-TEST",
		"billing.xcash.hmac_key": "hmac-secret",
	})
	gw := newXCashGateway(st)
	body := []byte(`{"type":"deposit","data":{"sys_no":"DXC1","uid":"user-1","crypto":"USDT","confirmed":true}}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/payments/xcash/notify", bytes.NewReader(body))
	req.Header.Set("XC-Appid", "XC-TEST")
	req.Header.Set("XC-Timestamp", ts)
	req.Header.Set("XC-Nonce", "nonce")
	req.Header.Set("XC-Signature", xcashSignature("nonce", ts, body, "hmac-secret"))
	ev, err := gw.VerifyNotify(context.Background(), req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ev.OutTradeNo != "" || ev.Status != "ignored" {
		t.Fatalf("deposit event = %+v", ev)
	}
}
