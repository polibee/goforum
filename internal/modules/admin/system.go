package admin

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/config"
	"goforum/internal/platform/i18n"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/sysinfo"
)

type systemData struct {
	Snap          sysinfo.Snapshot
	Locales       []string
	DefaultLocale string
	LangLabel     string
	AppLocale     string
	Tables        []tableStat
	TableTotal    int
	DataSize      uint64
	UploadSize    uint64
	UploadFiles   int
	SearchDocs    int64
	SessionCount  int64
	AuditCount    int64
	AdCount       int64
	MemHint       string
	DBHint        string
	EnvLabel      string
	DriverLabel   string
	EnvFile       string
	EnvFileOK     bool
	CacheDriver   string
	CachePrefix   string
	CacheNote     string
	EnvSourceNote string
	ConfigRows    []configRow
}

// configRow is one effective configuration value shown on the system page.
type configRow struct {
	Key    string
	Label  string
	Value  string
	Secret bool
}

type tableStat struct {
	Name string
	Rows int64
	Size uint64
	Has  bool
}

// systemTables lists the user-facing tables shown in the storage section.
var systemTables = []string{
	"users", "topics", "posts", "categories", "tags",
	"sessions", "audit_logs", "search_documents", "ads", "ad_slots",
	"orders", "products", "notifications", "messages",
}

func (m *Module) systemPage(c *gin.Context) {
	ctx := c.Request.Context()
	site := m.deps.Settings.SiteInfo()

	opt := sysinfo.Options{
		AppName:      site.Name,
		Version:      m.deps.Version,
		Env:          m.envName(),
		BaseURL:      m.baseURL(),
		Language:     site.Locale,
		Timezone:     site.Timezone,
		MaxBodyBytes: m.maxBody(),
		MetricsOn:    m.metricsOn(),
		StartedAt:    m.deps.StartedAt,
		DBDriver:     m.deps.DB.Dialector.Name(),
		CacheDriver:  m.cacheDriver(),
		CacheVersion: m.deps.Settings.Version(),
		CacheTTL:     m.cacheTTL(),
		DataPath:     m.dataPath(),
		UploadPath:   m.uploadPath(),
	}
	if m.deps.Config != nil {
		opt.ServerAddr = m.deps.Config.HTTP.Addr
		opt.TrustedProxy = len(m.deps.Config.HTTP.TrustedProxies)
		if m.deps.Config.HTTP.MaxBodyBytes > 0 {
			opt.MaxBodyBytes = m.deps.Config.HTTP.MaxBodyBytes
		}
	}
	if m.deps.AdsCache != nil {
		opt.AdsCacheRows, opt.AdsCacheAge, opt.AdsCacheWarm = m.deps.AdsCache.CacheStats()
	}

	// Database pool and file facts.
	if sqlDB, err := m.deps.DB.DB(); err == nil {
		st := sqlDB.Stats()
		opt.DBMaxOpen = st.MaxOpenConnections
		opt.DBOpenConns = st.OpenConnections
		opt.DBIdleConns = st.Idle
		opt.DBInUseConns = st.InUse
		opt.DBWaitCount = st.WaitCount
		opt.DBWaitSecs = st.WaitDuration.Seconds()
	}
	if driver := m.deps.DB.Dialector.Name(); driver == "sqlite" {
		opt.DBFile = m.sqliteFile()
		opt.DBVersion = sqliteVersion(m.deps.DB)
	} else {
		opt.DBVersion = m.deps.DB.Dialector.Name() + " " + serverVersion(m.deps.DB)
	}

	snap := sysinfo.Collect(opt)

	// Storage footprint.
	var tableRows []tableStat
	var totalSize uint64
	for _, name := range systemTables {
		var count int64
		if err := m.deps.DB.WithContext(ctx).Table(name).Count(&count).Error; err != nil {
			continue
		}
		tableRows = append(tableRows, tableStat{Name: name, Rows: count})
	}
	uploadSize, uploadFiles := sysinfo.DirsSize(opt.UploadPath)
	if opt.DBFile != "" {
		if st, err := os.Stat(opt.DBFile); err == nil {
			totalSize += uint64(st.Size())
		}
	}
	totalSize += uploadSize

	var searchDocs, sessionCount, auditCount, adCount int64
	_ = m.deps.DB.WithContext(ctx).Table("search_documents").Count(&searchDocs).Error
	_ = m.deps.DB.WithContext(ctx).Table("sessions").Count(&sessionCount).Error
	_ = m.deps.DB.WithContext(ctx).Table("audit_logs").Count(&auditCount).Error
	_ = m.deps.DB.WithContext(ctx).Table("ads").Count(&adCount).Error

	locales := []string{i18n.DefaultLocale}
	if m.deps.I18n != nil {
		seen := map[string]bool{i18n.DefaultLocale: true}
		for _, code := range m.deps.I18n.Supported() {
			if !seen[code] {
				seen[code] = true
				locales = append(locales, code)
			}
		}
	}

	data := m.base(c, "系统信息")
	data.Data = systemData{
		Snap:          snap,
		Locales:       locales,
		DefaultLocale: i18n.DefaultLocale,
		AppLocale:     site.Locale,
		EnvLabel:      m.envName(),
		DriverLabel:   m.deps.DB.Dialector.Name(),
		Tables:        tableRows,
		TableTotal:    len(tableRows),
		DataSize:      totalSize,
		UploadSize:    uploadSize,
		UploadFiles:   uploadFiles,
		SearchDocs:    searchDocs,
		SessionCount:  sessionCount,
		AuditCount:    auditCount,
		AdCount:       adCount,
		MemHint:       m.memHint(),
		DBHint:        m.dbHint(),
		EnvFile:       config.EnvPath(),
		EnvFileOK:     fileExists(config.EnvPath()),
		CacheDriver:   m.cacheDriver(),
		CachePrefix:   m.cachePrefix(),
		CacheNote:     m.cacheNote(),
		EnvSourceNote: m.envSourceNote(),
		ConfigRows:    m.configRows(),
	}
	m.deps.Renderer.Render(c, http.StatusOK, "admin_system", data)
}

func (m *Module) envName() string {
	if m.deps.Config == nil || m.deps.Config.Env == "" {
		return "production"
	}
	return m.deps.Config.Env
}

func (m *Module) baseURL() string {
	if m.deps.Config == nil {
		return ""
	}
	return m.deps.Config.HTTP.BaseURL
}

func (m *Module) maxBody() int64 {
	if m.deps.Config == nil {
		return 0
	}
	return m.deps.Config.HTTP.MaxBodyBytes
}

func (m *Module) metricsOn() bool {
	if m.deps.Config == nil {
		return false
	}
	return m.deps.Config.HTTP.MetricsEnabled
}

func (m *Module) cacheDriver() string {
	if m.deps.Config == nil || m.deps.Config.Cache.Driver == "" {
		return "memory"
	}
	return m.deps.Config.Cache.Driver
}

func (m *Module) cacheTTL() time.Duration {
	if m.deps.Config == nil {
		return 0
	}
	return m.deps.Config.Cache.TTL.Std()
}

func (m *Module) dataPath() string {
	if m.deps.Config == nil {
		return "data"
	}
	if driver := m.deps.DB.Dialector.Name(); driver == "sqlite" {
		if fp := m.sqliteFile(); fp != "" {
			return filepath.Dir(fp)
		}
	}
	return "data"
}

func (m *Module) uploadPath() string {
	if m.deps.Config == nil || m.deps.Config.Storage.LocalPath == "" {
		return "data/uploads"
	}
	return m.deps.Config.Storage.LocalPath
}

func (m *Module) sqliteFile() string {
	if m.deps.Config == nil {
		return ""
	}
	dsn := m.deps.Config.Database.DSN
	if dsn == "" || dsn == ":memory:" || strings.HasPrefix(dsn, "file::memory:") {
		return ""
	}
	if i := strings.Index(dsn, "?"); i >= 0 {
		dsn = dsn[:i]
	}
	return dsn
}

func sqliteVersion(db *gorm.DB) string {
	var v string
	if err := db.Raw("SELECT sqlite_version()").Scan(&v).Error; err == nil && v != "" {
		return "SQLite " + v
	}
	return "SQLite"
}

func serverVersion(db *gorm.DB) string {
	var v string
	_ = db.Raw("SHOW server_version").Scan(&v).Error
	if v == "" {
		_ = db.Raw("SELECT version()").Scan(&v).Error
	}
	if v == "" {
		return ""
	}
	if i := strings.IndexByte(v, ' '); i > 0 {
		v = v[:i]
	}
	return v
}

func (m *Module) memHint() string {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	if stats.HeapSys == 0 {
		return ""
	}
	return strconv.FormatFloat(float64(stats.HeapAlloc)/float64(stats.HeapSys)*100, 'f', 0, 64) + "%"
}

func (m *Module) dbHint() string {
	if sqlDB, err := m.deps.DB.DB(); err == nil {
		st := sqlDB.Stats()
		return strconv.Itoa(st.OpenConnections) + "/" + strconv.Itoa(st.MaxOpenConnections)
	}
	return ""
}

// oauthProviders lists enabled providers for the admin login page so the shared
// login template can render the same buttons as the frontend.
func (m *Module) oauthProviders() []contracts.OAuthProviderView {
	return oauthProvidersFromSettings(m.deps.Settings)
}

func oauthProvidersFromSettings(st *settings.Store) []contracts.OAuthProviderView {
	if st == nil {
		return nil
	}
	type prov struct{ key, name, icon string }
	all := []prov{
		{"google", "Google", "google"},
		{"github", "GitHub", "github"},
	}
	var out []contracts.OAuthProviderView
	for _, p := range all {
		if !st.GetBool("oauth." + p.key + ".enabled") {
			continue
		}
		if st.GetString("oauth."+p.key+".client_id") == "" || st.GetString("oauth."+p.key+".client_secret") == "" {
			continue
		}
		out = append(out, contracts.OAuthProviderView{Key: p.key, Name: p.name, Icon: p.icon})
	}
	return out
}

// envSourceNote explains which layer supplied the effective configuration.
func (m *Module) envSourceNote() string {
	if fileExists(config.EnvPath()) {
		return "当前配置来自 .env，其次被本地环境变量覆盖。修改后重启生效。"
	}
	return "未找到 .env，正在使用 YAML 与内置默认值。复制 .env.example 为 .env 即可接管数据库等配置。"
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// configRows lists the effective non-secret runtime configuration.
func (m *Module) configRows() []configRow {
	cfg := m.deps.Config
	if cfg == nil {
		return nil
	}
	dsn := cfg.Database.DSN
	if cfg.Database.Driver == "postgres" || cfg.Database.Driver == "mysql" {
		params := config.ParseDSNToParams(cfg.Database.Driver, dsn)
		dsn = params.Username + "@" + params.Host + ":" + params.Port + "/" + params.Database
	}
	return []configRow{
		{Key: "APP_ENV", Label: "运行模式", Value: cfg.Env},
		{Key: "APP_URL", Label: "站点地址", Value: orDash(cfg.HTTP.BaseURL)},
		{Key: "HTTP_ADDR", Label: "监听地址", Value: cfg.HTTP.Addr},
		{Key: "DB_CONNECTION", Label: "数据库驱动", Value: cfg.Database.Driver},
		{Key: "DB_DATABASE", Label: "数据库连接", Value: dsn},
		{Key: "DB_MAX_OPEN", Label: "最大连接数", Value: strconv.Itoa(cfg.Database.MaxOpen)},
		{Key: "CACHE_STORE", Label: "缓存驱动", Value: cfg.Cache.Driver},
		{Key: "FILESYSTEM_DISK", Label: "存储驱动", Value: cfg.Storage.Driver},
		{Key: "STORAGE_PATH", Label: "上传目录", Value: cfg.Storage.LocalPath},
		{Key: "MAIL_MAILER", Label: "邮件方式", Value: boolText(cfg.Mail.Enabled, "smtp", "log")},
		{Key: "LOG_LEVEL", Label: "日志级别", Value: cfg.Log.Level},
		{Key: "SESSION_LIFETIME", Label: "会话有效期", Value: cfg.Security.SessionTTL.Std().String()},
		{Key: "WORKER_ENABLED", Label: "后台任务", Value: boolText(cfg.Worker.Enabled, "启用", "停用")},
	}
}

func orDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "—"
	}
	return v
}

func boolText(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func (m *Module) cachePrefix() string {
	if m.deps.Config == nil {
		return ""
	}
	return m.deps.Config.Cache.Prefix
}

// cacheNote explains what the current cache driver means for scaling.
func (m *Module) cacheNote() string {
	switch m.cacheDriver() {
	case "redis":
		return "Redis：限流、站点统计缓存与后台任务锁在多实例间共享，可水平扩容。"
	default:
		return "内存：仅本进程生效。多实例部署请把 CACHE_STORE 改为 redis 并填写 REDIS_URL。"
	}
}
