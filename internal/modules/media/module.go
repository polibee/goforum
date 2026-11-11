package media

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/config"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/storage"
	"goforum/internal/platform/view"
)

type Module struct {
	db       *gorm.DB
	svc      *Service
	settings *settings.Store
	auth     contracts.Authenticator
	sessions *session.Store
	cfg      *config.Config
	log      *slog.Logger
	renderer *view.Renderer
	version  string
}

func New(db *gorm.DB, svc *Service) *Module { return &Module{db: db, svc: svc} }

func (m *Module) Configure(st *settings.Store, stg storage.Storage, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, renderer *view.Renderer, version string) {
	m.settings = st
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.renderer = renderer
	m.version = version
	m.svc.Configure(st, stg)
}

func (m *Module) Name() string               { return "media" }
func (m *Module) DependsOn() []string        { return []string{"auth"} }
func (m *Module) Models() []any              { return Models() }
func (m *Module) Subscribe(events.Bus) error { return nil }
func (m *Module) Settings() []settings.Definition {
	return []settings.Definition{
		{Namespace: "media", Key: "upload.max_size_mb", Group: "media", Label: "单文件上限（MB）", Type: settings.TypeInt, Default: 20, Min: 1, Max: 200, Permission: "settings.write"},
		{Namespace: "media", Key: "upload.quota_per_user_mb", Group: "media", Label: "个人存储配额（MB）", Type: settings.TypeInt, Default: 500, Min: 10, Max: 100000, Permission: "settings.write"},
	}
}

func (m *Module) Admin() adminui.Contribution { return adminui.Contribution{} }

func (m *Module) Permissions() []permission.Definition {
	return []permission.Definition{
		{Key: "upload.create", Label: "上传附件", Group: "内容"},
	}
}

func (m *Module) md() middleware.Deps {
	return middleware.Deps{
		Config: m.cfg, Sessions: m.sessions, Users: contracts.AsLoader(m.auth),
		Log: m.log, Renderer: m.renderer, Settings: m.settings, Version: m.version,
	}
}

func (m *Module) Routes(web, api *gin.RouterGroup) {
	md := m.md()
	login := middleware.RequireLogin(md)
	canUpload := middleware.RequirePermission(md, "upload.create")
	uploadLimit := middleware.RateLimit(md, "upload", 60, time.Hour)
	api.POST("/uploads", login, canUpload, uploadLimit, m.upload)
	web.POST("/uploads", login, canUpload, uploadLimit, m.upload)
}

func (m *Module) upload(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"file": "请选择要上传的图片"}))
		return
	}
	defer file.Close()

	principal := authn.FromContext(c.Request.Context())
	u, url, err := m.svc.Upload(c.Request.Context(), principal.ID, header.Filename, header.Header.Get("Content-Type"), file)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"data": gin.H{
			"id": u.ID, "url": url, "thumb_url": m.svc.ThumbURL(c.Request.Context(), u.ID),
			"mime": u.MIME, "width": u.Width, "height": u.Height, "size_bytes": u.SizeBytes,
		},
	})
}
