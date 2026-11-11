package i18n

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/authn"
)

func testBundle(t *testing.T) *Bundle {
	t.Helper()
	fsys := fstest.MapFS{
		"en-US/common.json": &fstest.MapFile{Data: []byte(`{"登录":"Log in","首页":"Home"}`)},
		"en-US/auth.json":   &fstest.MapFile{Data: []byte(`{"密码":"Password"}`)},
	}
	b, err := Load(fsys)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return b
}

func TestTranslateAndFallback(t *testing.T) {
	b := testBundle(t)
	if got := b.Translate("en-US", "登录"); got != "Log in" {
		t.Fatalf("en = %q", got)
	}
	if got := b.Translate("en-US", "密码"); got != "Password" {
		t.Fatalf("auth namespace = %q", got)
	}
	if got := b.Translate("en-US", "未翻译"); got != "未翻译" {
		t.Fatalf("fallback = %q", got)
	}
	if got := b.Translate("zh-CN", "登录"); got != "登录" {
		t.Fatalf("zh = %q", got)
	}
}

func TestMiddlewareResolvesLocale(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := testBundle(t)
	r := gin.New()
	r.Use(b.Middleware(func() string { return "zh-CN" }))
	r.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, FromContext(c.Request.Context()))
	})

	req := httptest.NewRequest(http.MethodGet, "/?lang=en-US", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Body.String() != "en-US" {
		t.Fatalf("query locale = %q", w.Body.String())
	}
	if cookie := w.Result().Cookies(); len(cookie) == 0 || cookie[0].Name != CookieName {
		t.Fatal("locale cookie not set")
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Body.String() != "en-US" {
		t.Fatalf("accept-language = %q", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(authn.WithPrincipal(context.Background(), &authn.Principal{Locale: "en-US"}))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Body.String() != "en-US" {
		t.Fatalf("user preference = %q", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(authn.WithPrincipal(context.Background(), &authn.Principal{}))
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Body.String() != "zh-CN" {
		t.Fatalf("default for authenticated user = %q", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Body.String() != "zh-CN" {
		t.Fatalf("zh = %q", w.Body.String())
	}
}
