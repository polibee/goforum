package auth

import (
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestOAuthStateSignedRoundTrip(t *testing.T) {
	state, payload, err := NewOAuthState("google", "/settings", true)
	if err != nil {
		t.Fatalf("new state: %v", err)
	}
	if state.Nonce == "" || state.Verifier == "" {
		t.Fatal("state must include nonce and PKCE verifier")
	}
	signed, err := SignOAuthState(payload, "test-app-key")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := VerifyOAuthState(signed, "test-app-key")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != state {
		t.Fatalf("state mismatch: got %#v want %#v", got, state)
	}
}

func TestOAuthStateRejectsTamperAndWrongKey(t *testing.T) {
	_, payload, err := NewOAuthState("github", "/", false)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignOAuthState(payload, "key-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyOAuthState(signed, "key-two"); err == nil {
		t.Fatal("wrong key accepted")
	}
	parts := strings.Split(signed, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected signed format: %q", signed)
	}
	if _, err := VerifyOAuthState(parts[0]+"x."+parts[1], "key-one"); err == nil {
		t.Fatal("tampered payload accepted")
	}
	if _, err := SignOAuthState(payload, ""); err == nil {
		t.Fatal("empty signing key accepted")
	}
}

func TestOAuthStateNonceComparison(t *testing.T) {
	if !oauthStateMatches("abc", "abc") {
		t.Fatal("equal nonce rejected")
	}
	if oauthStateMatches("abc", "abd") || oauthStateMatches("", "abc") {
		t.Fatal("mismatched nonce accepted")
	}
}

func TestAuthCodeURLUsesPKCE(t *testing.T) {
	state, _, err := NewOAuthState("google", "/", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &oauth2.Config{
		ClientID:    "client",
		RedirectURL: "https://forum.example.com/oauth/google/callback",
		Endpoint:    oauth2.Endpoint{AuthURL: "https://accounts.example.com/authorize"},
	}
	raw := AuthCodeURL(cfg, state)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("state") != state.Nonce {
		t.Errorf("state = %q, want nonce", q.Get("state"))
	}
	if q.Get("code_challenge") == "" {
		t.Error("missing code_challenge")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if strings.Contains(raw, state.Verifier) {
		t.Error("PKCE verifier leaked into authorize URL")
	}
}
