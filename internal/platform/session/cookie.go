package session

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/config"
)

func CookieName(cfg *config.Config) string {
	if cfg != nil && cfg.Security.SessionCookie != "" {
		return cfg.Security.SessionCookie
	}
	return "gf_session"
}

// TwoFactorCookie carries the short-lived password-step challenge token. It is
// separate from the session cookie so a half-authenticated browser gets no
// session at all until the TOTP code is verified.
const TwoFactorCookie = "gf_2fa"

func SetTwoFactorCookie(c *gin.Context, cfg *config.Config, token string, ttl time.Duration) {
	secure := c.Request.TLS != nil
	if cfg != nil && strings.HasPrefix(strings.ToLower(cfg.HTTP.BaseURL), "https://") {
		secure = true
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(TwoFactorCookie, token, int(ttl.Seconds()), "/", "", secure, true)
}

func ClearTwoFactorCookie(c *gin.Context, cfg *config.Config) {
	secure := c.Request.TLS != nil
	if cfg != nil && strings.HasPrefix(strings.ToLower(cfg.HTTP.BaseURL), "https://") {
		secure = true
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(TwoFactorCookie, "", -1, "/", "", secure, true)
}

func SetCookie(c *gin.Context, cfg *config.Config, token string, ttl time.Duration) {
	secure := c.Request.TLS != nil
	if cfg != nil && strings.HasPrefix(strings.ToLower(cfg.HTTP.BaseURL), "https://") {
		secure = true
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(CookieName(cfg), token, int(ttl.Seconds()), "/", "", secure, true)
}

func ClearCookie(c *gin.Context, cfg *config.Config) {
	secure := c.Request.TLS != nil
	if cfg != nil && strings.HasPrefix(strings.ToLower(cfg.HTTP.BaseURL), "https://") {
		secure = true
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(CookieName(cfg), "", -1, "/", "", secure, true)
}
