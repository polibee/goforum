package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResendSend(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emails" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer re_test" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"mail_1"}`))
	}))
	defer srv.Close()

	m := NewResend(srv.URL, "re_test", "GoForum <no-reply@example.com>")
	err := m.Send(context.Background(), Message{
		To: []string{"user@example.com"}, Subject: "验证邮箱", Text: "点击链接完成验证",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if got["subject"] != "验证邮箱" {
		t.Fatalf("subject = %v", got["subject"])
	}
	if got["html"] == "" {
		t.Fatal("html fallback missing")
	}
	to, ok := got["to"].([]any)
	if !ok || len(to) != 1 || to[0] != "user@example.com" {
		t.Fatalf("to = %v", got["to"])
	}
}

func TestResendErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid key", http.StatusUnauthorized)
	}))
	defer srv.Close()
	m := NewResend(srv.URL, "bad", "a@b.com")
	if err := m.Send(context.Background(), Message{To: []string{"c@d.com"}, Subject: "s"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestResendRequiresKeyAndFrom(t *testing.T) {
	if err := NewResend("http://x", "", "a@b.com").Send(context.Background(), Message{To: []string{"c@d.com"}}); err == nil {
		t.Fatal("expected missing key error")
	}
	if err := NewResend("http://x", "k", "").Send(context.Background(), Message{To: []string{"c@d.com"}}); err == nil {
		t.Fatal("expected missing from error")
	}
}
