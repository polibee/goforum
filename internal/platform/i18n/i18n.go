// Package i18n provides locale negotiation and per-feature translation catalogs.
package i18n

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/authn"
)

const (
	DefaultLocale = "zh-CN"
	CookieName    = "gf_lang"
	contextKey    = "gf_locale"
)

// Bundle holds translations keyed by locale and source phrase.
type Bundle struct {
	mu       sync.RWMutex
	catalogs map[string]map[string]string
	locales  []string
}

// Load reads locales/<locale>/<feature>.json from fsys. zh-CN is the source
// language and needs no catalog; other locales fall back to the source phrase.
func Load(fsys fs.FS) (*Bundle, error) {
	b := &Bundle{catalogs: make(map[string]map[string]string)}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		locale := entry.Name()
		b.locales = append(b.locales, locale)
		catalog := make(map[string]string)
		files, err := fs.ReadDir(fsys, locale)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			raw, err := fs.ReadFile(fsys, path.Join(locale, file.Name()))
			if err != nil {
				return nil, err
			}
			var entries map[string]string
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, fmt.Errorf("i18n %s/%s: %w", locale, file.Name(), err)
			}
			for k, v := range entries {
				if v == "" {
					continue
				}
				catalog[k] = v
			}
		}
		b.catalogs[locale] = catalog
	}
	sort.Strings(b.locales)
	if _, ok := b.catalogs[DefaultLocale]; !ok {
		b.catalogs[DefaultLocale] = map[string]string{}
	}
	return b, nil
}

func (b *Bundle) Supported() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]string(nil), b.locales...)
}

func (b *Bundle) IsSupported(locale string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.catalogs[locale]
	return ok || locale == DefaultLocale
}

func (b *Bundle) Translate(locale, text string) string {
	if text == "" || locale == "" || locale == DefaultLocale {
		return text
	}
	b.mu.RLock()
	catalog := b.catalogs[locale]
	translated, ok := catalog[text]
	b.mu.RUnlock()
	if ok {
		return translated
	}
	return text
}

// Middleware resolves the request locale from ?lang, cookie, Accept-Language
// and the site default, then stores it on the request context.
func (b *Bundle) Middleware(fallback func() string) gin.HandlerFunc {
	return func(c *gin.Context) {
		locale := b.resolve(c, fallback())
		if q := strings.TrimSpace(c.Query("lang")); q != "" && b.IsSupported(q) {
			c.SetSameSite(http.SameSiteLaxMode)
			c.SetCookie(CookieName, q, 365*24*3600, "/", "", c.Request.TLS != nil, true)
		}
		ctx := context.WithValue(c.Request.Context(), contextKey, locale)
		c.Request = c.Request.WithContext(ctx)
		c.Set("locale", locale)
		c.Header("Content-Language", locale)
		c.Next()
	}
}

func (b *Bundle) resolve(c *gin.Context, fallback string) string {
	if q := strings.TrimSpace(c.Query("lang")); q != "" && b.IsSupported(q) {
		return q
	}
	if v, err := c.Cookie(CookieName); err == nil && b.IsSupported(v) {
		return v
	}
	if p := authn.FromContext(c.Request.Context()); p != nil {
		if p.Locale != "" && b.IsSupported(p.Locale) {
			return p.Locale
		}
		// Authenticated users who have not chosen a language follow the site
		// default rather than their browser headers.
		if b.IsSupported(fallback) {
			return fallback
		}
	}
	if header := c.GetHeader("Accept-Language"); header != "" {
		for _, part := range strings.Split(header, ",") {
			tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
			if tag == "" {
				continue
			}
			if b.IsSupported(tag) {
				return tag
			}
			lower := strings.ToLower(tag)
			switch {
			case strings.HasPrefix(lower, "zh"):
				if b.IsSupported("zh-CN") {
					return "zh-CN"
				}
			case strings.HasPrefix(lower, "en"):
				if b.IsSupported("en-US") {
					return "en-US"
				}
			}
		}
	}
	if b.IsSupported(fallback) {
		return fallback
	}
	return DefaultLocale
}

// FromContext returns the resolved locale, defaulting to zh-CN.
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(contextKey).(string); ok && v != "" {
		return v
	}
	return DefaultLocale
}
