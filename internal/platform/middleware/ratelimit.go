package middleware

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/cache"
)

var (
	// limiterCache is set once at startup from the configured cache driver.
	limiterCache cache.Cache
	limiterMu    sync.RWMutex
)

// SetRateLimitCache installs the cache used by RateLimit. Passing nil falls
// back to a process-local memory cache.
func SetRateLimitCache(c cache.Cache) {
	limiterMu.Lock()
	limiterCache = c
	limiterMu.Unlock()
}

func limiter() cache.Cache {
	limiterMu.RLock()
	c := limiterCache
	limiterMu.RUnlock()
	if c != nil {
		return c
	}
	limiterMu.Lock()
	defer limiterMu.Unlock()
	if limiterCache == nil {
		limiterCache = cache.NewMemory(10 * time.Minute)
	}
	return limiterCache
}

// allowRate is the fixed-window check. With a shared cache (Redis) the counter
// is global, so limits hold across instances.
func allowRate(key string, limit int, window time.Duration) (bool, time.Duration) {
	if limit <= 0 {
		return true, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	n, err := limiter().Incr(ctx, "rl:"+key, 1, window)
	if err != nil {
		// A cache outage must not take the site down: fail open.
		return true, 0
	}
	if int(n) > limit {
		return false, window
	}
	return true, 0
}

// RateLimit limits requests per client IP within a fixed window.
func RateLimit(d Deps, name string, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.ClientIP() + "|" + name
		ok, retry := allowRate(key, limit, window)
		if ok {
			c.Next()
			return
		}
		if retry < time.Second {
			retry = time.Second
		}
		c.Header("Retry-After", strconv.Itoa(int(retry.Seconds())))
		if d.Log != nil {
			d.Log.Warn("rate limit exceeded", "bucket", name, "ip", c.ClientIP(), "path", c.Request.URL.Path)
		}
		if wantsJSON(c) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{"code": "rate_limited", "message": "请求过于频繁，请稍后再试"},
			})
			return
		}
		d.RenderError(c, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
		c.Abort()
	}
}

// BodyLimit caps the request body size. A non-positive value disables the cap.
func BodyLimit(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if max > 0 && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		}
		c.Next()
	}
}

// ResetRateLimits clears counters. Intended for tests.
func ResetRateLimits() {
	limiterMu.Lock()
	if c, ok := limiterCache.(*cache.Memory); ok && c != nil {
		_ = c.DeletePrefix(context.Background(), "rl:")
	} else {
		limiterCache = cache.NewMemory(10 * time.Minute)
	}
	limiterMu.Unlock()
}
