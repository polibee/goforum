package admin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/config"
)

type twoFactorAuth struct {
	contracts.Authenticator
	user *contracts.UserView
	mode string
}

func (f twoFactorAuth) Authenticate(context.Context, string, string) (*contracts.UserView, error) {
	return f.user, nil
}

func (f twoFactorAuth) TwoFactorMode(context.Context, *contracts.UserView) string { return f.mode }

func (f twoFactorAuth) CreateLoginChallenge(context.Context, string, string) (string, error) {
	return "challenge-token", nil
}

func TestAdminLoginRedirectsToTOTPChallenge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := &Module{deps: Deps{
		Config: &config.Config{},
		Auth: twoFactorAuth{
			user: &contracts.UserView{ID: "u1", Username: "root", Status: "active"},
			mode: "verify",
		},
		Log: slog.Default(),
	}}
	r := gin.New()
	r.POST("/admin/login", m.loginPost)

	form := url.Values{"login": {"root"}, "password": {"secret12345"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login/2fa" {
		t.Fatalf("login response = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	found := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "gf_2fa" && cookie.Value == "challenge-token" {
			found = true
		}
		if cookie.Name == "gf_session" {
			t.Fatal("admin session must not be created before TOTP verification")
		}
	}
	if !found {
		t.Fatal("2FA challenge cookie missing")
	}
}
