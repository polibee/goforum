package app

import (
	"context"
	"crypto/subtle"
	"io/fs"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/db"
	"goforum/internal/platform/metrics"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/view"
	webfs "goforum/web"
)

type homeData struct {
	Modules []adminui.ModuleInfo
	Version string
}

func (a *App) middlewareDeps() middleware.Deps {
	return middleware.Deps{
		Config:   a.Cfg,
		Sessions: a.Sessions,
		Users:    contracts.AsLoader(a.Auth),
		Log:      a.Log,
		Renderer: a.Renderer,
		Settings: a.Settings,
		Version:  a.Version,
	}
}

// Router builds the HTTP handler. It is safe to call once per App.
func (a *App) Router() (*gin.Engine, error) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	md := a.middlewareDeps()

	r.Use(
		middleware.RequestID(),
		middleware.Recover(md),
		middleware.BodyLimit(a.Cfg.HTTP.MaxBodyBytes),
		middleware.AccessLog(a.Log),
		middleware.SecurityHeaders(md),
		middleware.ThemePreset(),
		middleware.StaticCache(),
		middleware.Session(md),
		a.I18n.Middleware(func() string { return a.Settings.GetString("site.general.locale") }),
		a.unreadCount(),
		metrics.Middleware(a.Metrics),
		middleware.CSRF(md),
	)

	if len(a.Cfg.HTTP.TrustedProxies) > 0 {
		_ = r.SetTrustedProxies(a.Cfg.HTTP.TrustedProxies)
	} else {
		_ = r.SetTrustedProxies(nil)
	}

	staticFS, err := fs.Sub(webfs.FS, "static")
	if err != nil {
		return nil, err
	}
	r.StaticFS("/static", http.FS(staticFS))
	if a.Cfg.Storage.Driver == "local" && a.Cfg.Storage.LocalPath != "" {
		r.Static("/uploads", a.Cfg.Storage.LocalPath)
	}

	r.GET("/healthz", a.health)
	r.GET("/readyz", a.ready)
	if a.Cfg.HTTP.MetricsEnabled {
		r.GET("/metrics", a.metrics)
	}

	api := r.Group("/api/v1")
	api.GET("/health", a.health)

	web := r.Group("/")
	for _, m := range a.Registry.Modules() {
		m.Routes(web, api)
	}

	r.HandleMethodNotAllowed = true
	r.NoRoute(func(c *gin.Context) { a.renderError(c, http.StatusNotFound, "页面不存在") })
	r.NoMethod(func(c *gin.Context) { a.renderError(c, http.StatusMethodNotAllowed, "请求方法不被允许") })
	return r, nil
}

func (a *App) home(c *gin.Context) {
	data := a.pageData(c, "")
	data.Title = data.Site.Name + " - " + data.Site.Tagline
	data.Data = homeData{Modules: a.Registry.ModuleInfos(), Version: a.Version}
	a.Renderer.Render(c, http.StatusOK, "home", data)
}

func (a *App) pageData(c *gin.Context, title string) view.Data {
	data := view.BaseData(c, a.Settings.SiteInfo(), a.Version)
	if title == "" {
		data.Title = data.Site.Name
	} else {
		data.Title = title + " - " + data.Site.Name
	}
	return data
}

func (a *App) renderError(c *gin.Context, status int, message string) {
	data := a.pageData(c, "")
	data.Title = data.Site.Name
	data.Data = view.ErrorData{Code: status, Message: message, RequestID: c.GetString("request_id")}
	a.Renderer.Render(c, status, "error", data)
}

func (a *App) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "version": a.Version})
}

func (a *App) metrics(c *gin.Context) {
	if token := a.Cfg.HTTP.MetricsToken; token != "" {
		auth := c.GetHeader("Authorization")
		expected := "Bearer " + token
		if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
			c.Header("WWW-Authenticate", "Bearer")
			c.String(http.StatusUnauthorized, "unauthorized")
			return
		}
	}
	a.Metrics.Handler(func() metrics.DBStats {
		sqlDB, err := a.DB.DB()
		if err != nil {
			return metrics.DBStats{}
		}
		stats := sqlDB.Stats()
		return metrics.DBStats{
			OpenConnections: stats.OpenConnections,
			InUse:           stats.InUse,
			WaitCount:       stats.WaitCount,
		}
	}, a.Version)(c)
}

func (a *App) ready(c *gin.Context) {
	sqlDB, err := a.DB.DB()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "error": "database handle"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "error": "database ping"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":   "ready",
		"version":  a.Version,
		"dialect":  a.DB.Dialector.Name(),
		"settings": a.Settings.Version(),
		"modules":  len(a.Registry.Modules()),
	})
}

func (a *App) Installed(ctx context.Context) bool {
	n, err := a.Auth.Count(ctx)
	return err == nil && n > 0
}

// StartBackground runs periodic maintenance for this app instance.
//
// With a shared cache (Redis) the tick acquires a lease first, so only one
// instance performs the work per interval; memory deployments run it locally.
// WORKER_ENABLED=false disables the loops entirely.
func (a *App) StartBackground(ctx context.Context) {
	if a.Auth == nil {
		return
	}
	if a.Cfg != nil && !a.Cfg.Worker.Enabled {
		a.Log.Info("background worker disabled by configuration")
		return
	}
	if a.Billing != nil {
		a.Billing.SetJobLease(a.acquireJobLease)
		a.Billing.StartBackground(ctx)
	}
	// Settings changed on another instance are picked up within this interval.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if changed, err := a.Settings.RefreshIfStale(ctx); err != nil {
					a.Log.Debug("settings refresh check failed", "error", err)
				} else if changed {
					a.Log.Info("settings reloaded from another instance")
				}
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !a.acquireJobLease(ctx, "trust-levels", 55*time.Minute) {
					continue
				}
				if n, err := a.Auth.RecomputeTrustLevels(ctx); err != nil {
					a.Log.Warn("recompute trust levels failed", "error", err)
				} else if n > 0 {
					a.Log.Info("trust levels updated", "count", n)
				}
			}
		}
	}()
}

// acquireJobLease claims a named background job for this interval. It returns
// true when this instance should run the work. A memory cache always grants
// the lease (single instance), while Redis serialises across instances.
func (a *App) acquireJobLease(ctx context.Context, name string, ttl time.Duration) bool {
	if a.Cache == nil {
		return true
	}
	key := "job:" + name
	ok, err := a.Cache.Add(ctx, key, []byte("1"), ttl)
	if err != nil {
		// On cache errors run locally rather than silently skipping work.
		return true
	}
	return ok
}

func (a *App) unreadCount() gin.HandlerFunc {
	return func(c *gin.Context) {
		if p := authn.FromContext(c.Request.Context()); p != nil {
			if a.Notifications != nil {
				if n, err := a.Notifications.UnreadCount(c.Request.Context(), p.ID); err == nil {
					c.Set("unread", n)
				}
			}
			if a.Messaging != nil {
				c.Set("msg_unread", a.Messaging.UnreadTotal(c.Request.Context(), p.ID))
			}
		}
		c.Next()
	}
}

func (a *App) Close() error {
	// Stop subscribers before closing the cache client they depend on.
	if a.notifyModule != nil {
		a.notifyModule.CloseSSE()
	}
	if a.RedisBus != nil {
		_ = a.RedisBus.Close()
	}
	if a.Cache != nil {
		_ = a.Cache.Close()
	}
	return db.Close(a.DB)
}
