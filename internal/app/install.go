package app

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/config"
	"goforum/internal/platform/db/dialect"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/i18n"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
	webfs "goforum/web"
)

type installData struct {
	Drivers  []string
	Driver   string
	DSN      string
	SiteName string
	BaseURL  string
	Username string
	Email    string
	Error    string
}

type Installer struct {
	BaseCfg    *config.Config
	ConfigPath string
	Version    string
	Log        *slog.Logger
	Current    *App
	Renderer   *view.Renderer
	I18n       *i18n.Bundle
	OnReady    func(*App) error
}

func NewInstaller(current *App, baseCfg *config.Config, configPath, version string, log *slog.Logger, onReady func(*App) error) (*Installer, error) {
	renderer := (*view.Renderer)(nil)
	if current != nil {
		renderer = current.Renderer
	} else {
		templateFS, err := fs.Sub(webfs.FS, "templates")
		if err != nil {
			return nil, err
		}
		renderer, err = view.New(templateFS, baseCfg.IsDevelopment(), log)
		if err != nil {
			return nil, err
		}
	}
	localeFS, err := fs.Sub(webfs.FS, "locales")
	if err != nil {
		return nil, err
	}
	bundle, err := i18n.Load(localeFS)
	if err != nil {
		return nil, err
	}
	renderer.SetTranslator(bundle.Translate)
	renderer.SetAssetVersion(view.AssetVersion(webfs.FS))
	view.SetGlobalTranslator(bundle.Translate)
	return &Installer{
		BaseCfg:    baseCfg,
		ConfigPath: configPath,
		Version:    version,
		Log:        log,
		Current:    current,
		Renderer:   renderer,
		I18n:       bundle,
		OnReady:    onReady,
	}, nil
}

func (i *Installer) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	md := middleware.Deps{
		Config:   i.BaseCfg,
		Log:      i.Log,
		Renderer: i.Renderer,
		Version:  i.Version,
	}
	if i.Current != nil {
		md.Sessions = i.Current.Sessions
		md.Settings = i.Current.Settings
	}

	r.Use(
		middleware.RequestID(),
		middleware.Recover(md),
		i.I18n.Middleware(func() string { return "zh-CN" }),
		middleware.AccessLog(i.Log),
		middleware.SecurityHeaders(md),
		middleware.StaticCache(),
		middleware.CSRF(md),
	)

	staticFS, err := fs.Sub(webfs.FS, "static")
	if err == nil {
		r.StaticFS("/static", http.FS(staticFS))
	}

	r.GET("/install", i.get)
	r.POST("/install", i.post)
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "install", "version": i.Version}) })
	r.NoRoute(func(c *gin.Context) { c.Redirect(http.StatusFound, "/install") })
	return r
}

func (i *Installer) site() settings.Site {
	if i.Current != nil {
		return i.Current.Settings.SiteInfo()
	}
	return settings.Site{
		Name:        "GoForum",
		Tagline:     "一个模块化社区",
		Locale:      "zh-CN",
		ColorMode:   "light",
		BrandColor:  "#2563eb",
		AccentColor: "#0ea5e9",
	}
}

func (i *Installer) get(c *gin.Context) {
	if i.Current != nil && i.Current.Installed(c.Request.Context()) {
		c.Status(http.StatusNotFound)
		_, _ = c.Writer.WriteString("已安装")
		return
	}
	base := i.BaseCfg
	if i.Current != nil {
		base = i.Current.Cfg
	}
	data := installData{
		Drivers:  dialect.Names(),
		Driver:   base.Database.Driver,
		DSN:      base.Database.DSN,
		SiteName: i.site().Name,
		BaseURL:  base.HTTP.BaseURL,
		Username: "admin",
	}
	i.render(c, http.StatusOK, data)
}

func (i *Installer) post(c *gin.Context) {
	driver := strings.TrimSpace(c.PostForm("driver"))
	dsn := strings.TrimSpace(c.PostForm("dsn"))
	siteName := strings.TrimSpace(c.PostForm("site_name"))
	baseURL := strings.TrimSpace(c.PostForm("base_url"))
	username := strings.TrimSpace(c.PostForm("username"))
	email := strings.TrimSpace(c.PostForm("email"))
	plain := c.PostForm("password")

	data := installData{
		Drivers:  dialect.Names(),
		Driver:   driver,
		DSN:      dsn,
		SiteName: siteName,
		BaseURL:  baseURL,
		Username: username,
		Email:    email,
	}

	cfg := *i.BaseCfg
	if i.Current != nil {
		cfg = *i.Current.Cfg
	}
	cfg.Database.Driver = driver
	cfg.Database.DSN = dsn
	cfg.HTTP.BaseURL = baseURL
	if err := cfg.Validate(); err != nil {
		data.Error = err.Error()
		i.render(c, http.StatusUnprocessableEntity, data)
		return
	}

	// Generate the site key before opening the app: the auth service receives
	// it during Open and uses it to encrypt TOTP secrets at rest.
	if cfg.Security.SecretKey == "" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			i.Log.Error("install: generate secret key failed", "error", err)
			data.Error = "生成密钥失败，请重试"
			i.render(c, http.StatusInternalServerError, data)
			return
		}
		cfg.Security.SecretKey = base64.StdEncoding.EncodeToString(key)
	}

	// A .env file wins over YAML. Record the installer's database choice before
	// creating any data, so a restart cannot fall back to the previous database.
	if envPath := config.EnvPath(); envPath != "" {
		if _, statErr := os.Stat(envPath); statErr == nil {
			if err := config.AppendDatabaseEnv(envPath, cfg.Database.Driver, cfg.Database.DSN); err != nil {
				i.Log.Error("install: update env database failed", "error", err)
				data.Error = "写入 .env 数据库配置失败：" + err.Error()
				i.render(c, http.StatusInternalServerError, data)
				return
			}
		}
	}

	// Production mode deliberately skips AutoMigrate at startup, so a fresh
	// install must create the schema explicitly before Open validates it. The
	// fresh-only guard prevents an existing installation from being altered by
	// a stray /install request.
	if err := MigrateFresh(&cfg, i.Log); err != nil {
		if errors.Is(err, ErrDatabaseInitialized) {
			data.Error = "该数据库已初始化，请直接登录。"
			i.render(c, http.StatusConflict, data)
			return
		}
		i.Log.Error("install: create schema failed", "error", err)
		data.Error = "初始化数据库失败：" + err.Error()
		i.render(c, http.StatusUnprocessableEntity, data)
		return
	}

	newApp, err := Open(&cfg, i.ConfigPath, i.Version, i.Log)
	if err != nil {
		i.Log.Error("install: open database failed", "error", err)
		data.Error = "数据库连接失败：" + err.Error()
		i.render(c, http.StatusUnprocessableEntity, data)
		return
	}

	ok := false
	defer func() {
		if !ok {
			_ = newApp.Close()
		}
	}()

	if newApp.Installed(c.Request.Context()) {
		data.Error = "该数据库已初始化，请直接登录。"
		i.render(c, http.StatusConflict, data)
		return
	}

	if _, err := newApp.Auth.CreateAdmin(c.Request.Context(), username, email, plain); err != nil {
		data.Error = httpx.ErrorOf(err).Message
		i.render(c, http.StatusUnprocessableEntity, data)
		return
	}

	values := map[string]any{"site.general.name": siteName}
	if baseURL != "" {
		values["site.general.url"] = baseURL
	}
	if err := newApp.Settings.SetMany(c.Request.Context(), values, ""); err != nil {
		i.Log.Error("install: save settings failed", "error", err)
		data.Error = "保存站点设置失败：" + err.Error()
		i.render(c, http.StatusInternalServerError, data)
		return
	}

	if err := cfg.Save(i.ConfigPath); err != nil {
		i.Log.Error("install: save config failed", "error", err)
		data.Error = "写入配置文件失败：" + err.Error()
		i.render(c, http.StatusInternalServerError, data)
		return
	}

	if err := i.OnReady(newApp); err != nil {
		i.Log.Error("install: finish failed", "error", err)
		data.Error = "启动失败：" + err.Error()
		i.render(c, http.StatusInternalServerError, data)
		return
	}
	ok = true
	c.Redirect(http.StatusFound, "/admin/login")
}

func (i *Installer) render(c *gin.Context, status int, data installData) {
	base := view.BaseData(c, i.site(), i.Version)
	base.Title = "安装 - " + base.Site.Name
	base.Data = data
	i.Renderer.Render(c, status, "install", base)
}
