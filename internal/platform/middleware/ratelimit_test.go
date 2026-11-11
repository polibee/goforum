package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRateLimitBlocksAfterLimit(t *testing.T) {
	ResetRateLimits()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", RateLimit(Deps{}, "test_login", 2, time.Minute), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(""))
		req.RemoteAddr = "203.0.113.9:1234"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := send(); w.Code != http.StatusOK {
		t.Fatalf("first status = %d", w.Code)
	}
	if w := send(); w.Code != http.StatusOK {
		t.Fatalf("second status = %d", w.Code)
	}
	w := send()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("third status = %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

func TestRateLimitSeparatesClients(t *testing.T) {
	ResetRateLimits()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", RateLimit(Deps{}, "sep", 1, time.Minute), func(c *gin.Context) { c.Status(http.StatusOK) })

	send := func(ip string) int {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = ip + ":1000"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if send("198.51.100.1") != http.StatusOK || send("198.51.100.2") != http.StatusOK {
		t.Fatal("distinct clients shared a bucket")
	}
	if send("198.51.100.1") != http.StatusTooManyRequests {
		t.Fatal("first client not limited")
	}
}

func TestBodyLimitRejectsOversized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(4))
	r.POST("/upload", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("0123456789"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", w.Code)
	}
}
