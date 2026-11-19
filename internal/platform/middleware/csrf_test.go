package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/config"
)

func TestSameOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.HTTP.BaseURL = "https://forum.example.com"
	cases := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"same host", "https://forum.example.com", "forum.example.com", true},
		{"configured base url", "https://forum.example.com", "127.0.0.1:8080", true},
		{"cross site", "https://evil.example", "forum.example.com", false},
		{"null origin", "null", "forum.example.com", false},
		{"referer", "https://forum.example.com/t/1", "forum.example.com", true},
		{"missing headers", "", "forum.example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "http://"+tc.host+"/submit", nil)
			c.Request.Host = tc.host
			if tc.name == "referer" {
				c.Request.Header.Set("Referer", tc.origin)
			} else if tc.origin != "" {
				c.Request.Header.Set("Origin", tc.origin)
			}
			if got := sameOrigin(c, cfg); got != tc.want {
				t.Fatalf("sameOrigin = %v, want %v", got, tc.want)
			}
		})
	}
}
