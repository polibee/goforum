package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/config"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/id"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
)

type Module struct {
	db       *gorm.DB
	svc      *Service
	hub      *hub
	renderer *view.Renderer
	settings *settings.Store
	auth     contracts.Authenticator
	sessions *session.Store
	cfg      *config.Config
	log      *slog.Logger
	version  string
	// sseRedis lets the hub fan pushes out to other instances.
	sseRedis *redis.Client
	sseDone  context.CancelFunc
}

func New(db *gorm.DB, svc *Service) *Module {
	return &Module{db: db, svc: svc, hub: newHub()}
}

func (m *Module) Configure(renderer *view.Renderer, st *settings.Store, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, version string) {
	m.renderer = renderer
	m.settings = st
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.version = version
	m.svc.Configure(auth)
	m.svc.SetNotifier(m.onNotification)
}

// SetSSERedis enables cross-instance SSE delivery. Call before Routes.
func (m *Module) SetSSERedis(client *redis.Client) { m.sseRedis = client }

// CloseSSE stops the cross-instance SSE subscriber.
func (m *Module) CloseSSE() {
	if m.sseDone != nil {
		m.sseDone()
		m.sseDone = nil
	}
	if m.hub != nil {
		m.hub.close()
	}
}

func (m *Module) Name() string                   { return "notification" }
func (m *Module) DependsOn() []string            { return []string{"auth", "community"} }
func (m *Module) Models() []any                  { return Models() }
func (m *Module) Subscribe(bus events.Bus) error { return m.svc.Subscribe(bus) }

func (m *Module) Admin() adminui.Contribution { return adminui.Contribution{} }

func (m *Module) Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "notification.preferences", Label: "通知偏好", Group: "通知"},
	}
}

func (m *Module) Settings() []settings.Definition {
	return []settings.Definition{
		{Namespace: "notification", Key: "digest.frequency", Group: "notification", Label: "摘要频率", Type: settings.TypeEnum, Default: "weekly", Enum: []string{"off", "daily", "weekly"}, Permission: "settings.write"},
	}
}

func (m *Module) md() middleware.Deps {
	return middleware.Deps{
		Config: m.cfg, Sessions: m.sessions, Users: contracts.AsLoader(m.auth),
		Log: m.log, Renderer: m.renderer, Settings: m.settings, Version: m.version,
	}
}

func (m *Module) Routes(web, api *gin.RouterGroup) {
	if m.renderer == nil {
		return
	}
	// Enable cross-instance SSE delivery when Redis is configured.
	if m.sseRedis != nil && m.hub.client == nil {
		ctx, cancel := context.WithCancel(context.Background())
		if err := m.hub.startRedis(ctx, m.sseRedis, id.New(), m.log); err != nil {
			if m.log != nil {
				m.log.Warn("sse redis disabled; pushes stay local", "error", err)
			}
			cancel()
		} else {
			m.sseDone = cancel
			if m.log != nil {
				m.log.Info("sse cross-instance delivery enabled")
			}
		}
	}
	login := middleware.RequireLogin(m.md())

	web.GET("/notifications", login, m.page)
	web.POST("/notifications/read", login, m.markAllRead)
	web.POST("/notifications/:id/read", login, m.markRead)
	web.GET("/settings/notifications", login, m.prefsPage)
	web.POST("/settings/notifications", login, m.prefsSave)

	api.GET("/notifications", login, m.apiList)
	api.GET("/notifications/stream", login, m.stream)
	api.POST("/notifications/read", login, m.apiMarkAllRead)
}

type pageData struct {
	Rows       []Row
	Types      []TypeInfo
	Filter     string
	Total      int64
	Unread     int64
	Page       int
	TotalPages int
}

type prefsData struct {
	Types   []TypeInfo
	Enabled map[string]bool
	Saved   bool
}

func (m *Module) prefsPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.Title = "通知设置 - " + data.Site.Name
	data.Robots = "noindex,nofollow"
	data.Data = prefsData{
		Types:   Types,
		Enabled: m.svc.Preferences(c.Request.Context(), user.ID),
		Saved:   c.Query("saved") == "1",
	}
	m.renderer.Render(c, http.StatusOK, "notification_prefs", data)
}

func (m *Module) prefsSave(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	for _, t := range Types {
		enabled := c.PostForm("type."+t.Key) != ""
		if err := m.svc.SetPreference(c.Request.Context(), user.ID, t.Key, enabled); err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	c.Redirect(http.StatusFound, "/settings/notifications?saved=1")
}

func (m *Module) page(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const perPage = 20
	filter := c.Query("type")
	validFilter := ""
	for _, t := range Types {
		if filter == t.Key {
			validFilter = filter
			break
		}
	}
	rows, total, err := m.svc.List(c.Request.Context(), user.ID, page, perPage, validFilter)
	if err != nil {
		m.log.Error("list notifications failed", "error", err)
	}
	unread, _ := m.svc.UnreadCount(c.Request.Context(), user.ID, validFilter)

	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.Title = "通知 - " + data.Site.Name
	data.Robots = "noindex,nofollow"
	pages := int((total + perPage - 1) / perPage)
	if pages < 1 {
		pages = 1
	}
	data.Data = pageData{Rows: rows, Types: Types, Filter: validFilter, Total: total, Unread: unread, Page: page, TotalPages: pages}
	m.renderer.Render(c, http.StatusOK, "notifications", data)
}

func (m *Module) markAllRead(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.MarkAllRead(c.Request.Context(), user.ID); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Redirect(http.StatusFound, "/notifications")
}

func (m *Module) markRead(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.MarkRead(c.Request.Context(), user.ID, c.Param("id")); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Redirect(http.StatusFound, "/notifications")
}

func (m *Module) apiList(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	rows, total, err := m.svc.List(c.Request.Context(), user.ID, page, 20, "")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	unread, _ := m.svc.UnreadCount(c.Request.Context(), user.ID)
	httpx.OK(c, gin.H{"notifications": rows, "total": total, "unread": unread, "page": page})
}

func (m *Module) apiMarkAllRead(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.MarkAllRead(c.Request.Context(), user.ID); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (m *Module) notificationPayload(ctx context.Context, userID string) []byte {
	n, err := m.svc.UnreadCount(ctx, userID)
	if err != nil {
		return nil
	}
	payload := map[string]any{"unread": n}
	if n > 0 {
		if latest, err := m.svc.LatestUnread(ctx, userID); err == nil && latest != nil {
			payload["latest"] = latest
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return body
}

func (m *Module) onNotification(userID string) {
	if m.hub == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if body := m.notificationPayload(ctx, userID); body != nil {
			m.hub.publish(userID, body)
		}
	}()
}

func (m *Module) stream(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if user == nil {
		c.Status(http.StatusUnauthorized)
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.Status(http.StatusInternalServerError)
		return
	}
	ch, unsubscribe := m.hub.subscribe(user.ID)
	defer unsubscribe()

	write := func(body []byte) {
		if len(body) == 0 {
			return
		}
		fmt.Fprintf(c.Writer, "data: %s\n\n", body)
		flusher.Flush()
	}
	write(m.notificationPayload(c.Request.Context(), user.ID))

	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case body := <-ch:
			write(body)
		case <-keepAlive.C:
			fmt.Fprint(c.Writer, ": ping\n\n")
			flusher.Flush()
		}
	}
}
