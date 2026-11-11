package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const ThemeCookie = "gf_theme"

func validTheme(v string) bool {
	return v == "default" || v == "wechat" || v == "douyin" || v == "minimal"
}

// ThemePreset resolves the per-visitor visual preset from ?theme or cookie.
func ThemePreset() gin.HandlerFunc {
	return func(c *gin.Context) {
		preset := ""
		if q := c.Query("theme"); validTheme(q) {
			preset = q
			c.SetSameSite(http.SameSiteLaxMode)
			c.SetCookie(ThemeCookie, q, 365*24*3600, "/", "", c.Request.TLS != nil, true)
		} else if v, err := c.Cookie(ThemeCookie); err == nil && validTheme(v) {
			preset = v
		}
		if preset != "" {
			c.Set("theme_preset", preset)
		}
		c.Next()
	}
}
