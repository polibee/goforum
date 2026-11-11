package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a YAML-friendly time.Duration ("30m", "24h").
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

func (d Duration) Std() time.Duration { return time.Duration(d) }

type Config struct {
	Env      string         `yaml:"env"`
	HTTP     HTTPConfig     `yaml:"http"`
	Database DatabaseConfig `yaml:"database"`
	Cache    CacheConfig    `yaml:"cache"`
	Storage  StorageConfig  `yaml:"storage"`
	Mail     MailConfig     `yaml:"mail"`
	Log      LogConfig      `yaml:"log"`
	Security SecurityConfig `yaml:"security"`
	Worker   WorkerConfig   `yaml:"worker"`
}

type HTTPConfig struct {
	Addr           string   `yaml:"addr"`
	BaseURL        string   `yaml:"base_url"`
	TrustedProxies []string `yaml:"trusted_proxies"`
	MaxBodyBytes   int64    `yaml:"max_body_bytes"`
	MetricsEnabled bool     `yaml:"metrics_enabled"`
	MetricsToken   string   `yaml:"metrics_token"`
}

type DatabaseConfig struct {
	Driver          string   `yaml:"driver"`
	DSN             string   `yaml:"dsn"`
	MaxOpen         int      `yaml:"max_open"`
	MaxIdle         int      `yaml:"max_idle"`
	ConnMaxLifetime Duration `yaml:"conn_max_lifetime"`
}

type CacheConfig struct {
	Driver   string `yaml:"driver"` // memory | redis
	RedisURL string `yaml:"redis_url"`
	// Prefix namespaces keys so one Redis server can host several deployments.
	Prefix string   `yaml:"prefix"`
	TTL    Duration `yaml:"ttl"`
}

type StorageConfig struct {
	Driver    string `yaml:"driver"` // local | s3
	LocalPath string `yaml:"local_path"`
	BaseURL   string `yaml:"base_url"`
}

type MailConfig struct {
	Enabled  bool   `yaml:"enabled"`
	SMTPHost string `yaml:"smtp_host"`
	SMTPPort int    `yaml:"smtp_port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type SecurityConfig struct {
	SecretKey      string   `yaml:"secret_key"`
	SessionTTL     Duration `yaml:"session_ttl"`
	SessionCookie  string   `yaml:"session_cookie"`
	TrustedProxies []string `yaml:"trusted_proxies"`
}

type WorkerConfig struct {
	Enabled bool `yaml:"enabled"`
}

// Default returns a runnable, dependency-free configuration.
func Default() *Config {
	return &Config{
		Env: "production",
		HTTP: HTTPConfig{
			Addr:         ":8080",
			MaxBodyBytes: 32 << 20,
		},
		Database: DatabaseConfig{
			Driver:          "sqlite",
			DSN:             "./data/goforum.db",
			MaxOpen:         50,
			MaxIdle:         20,
			ConnMaxLifetime: Duration(30 * time.Minute),
		},
		Cache: CacheConfig{
			Driver: "memory",
			TTL:    Duration(5 * time.Minute),
		},
		Storage: StorageConfig{
			Driver:    "local",
			LocalPath: "./data/uploads",
			BaseURL:   "/uploads",
		},
		Log: LogConfig{Level: "info", Format: "json"},
		Security: SecurityConfig{
			SessionTTL:    Duration(30 * 24 * time.Hour),
			SessionCookie: "gf_session",
		},
		Worker: WorkerConfig{Enabled: true},
	}
}

// Load resolves configuration in this order (later wins):
//
//	defaults → YAML file → .env file → real environment variables
//
// The .env file follows Laravel naming (DB_CONNECTION/DB_HOST/DB_DATABASE …)
// and is the primary place operators configure the database: it always wins
// over the legacy YAML file. YAML stays supported so existing deployments keep
// booting without a .env.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(b, cfg); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
		case os.IsNotExist(err):
			// keep defaults; installer will create the file
		default:
			return nil, err
		}
	}

	dotenv, err := ParseDotEnv(EnvPath())
	if err != nil {
		return nil, err
	}
	if len(dotenv) > 0 {
		cfg.applyDotEnv(dotenv)
	}

	applyEnv(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyEnv(c *Config) {
	set := func(key string, dst *string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	setInt := func(key string, dst *int) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	setBool := func(key string, dst *bool) {
		if v := os.Getenv(key); v != "" {
			*dst = strings.EqualFold(v, "true") || v == "1"
		}
	}
	set("GF_ENV", &c.Env)
	set("GF_HTTP_ADDR", &c.HTTP.Addr)
	set("GF_BASE_URL", &c.HTTP.BaseURL)
	set("GF_METRICS_TOKEN", &c.HTTP.MetricsToken)
	setBool("GF_METRICS_ENABLED", &c.HTTP.MetricsEnabled)
	if v := os.Getenv("GF_MAX_BODY_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.HTTP.MaxBodyBytes = n
		}
	}
	set("GF_DB_DRIVER", &c.Database.Driver)
	set("GF_DB_DSN", &c.Database.DSN)
	setInt("GF_DB_MAX_OPEN", &c.Database.MaxOpen)
	setInt("GF_DB_MAX_IDLE", &c.Database.MaxIdle)
	set("GF_REDIS_URL", &c.Cache.RedisURL)
	set("GF_STORAGE_DRIVER", &c.Storage.Driver)
	set("GF_STORAGE_LOCAL_PATH", &c.Storage.LocalPath)
	set("GF_SECRET_KEY", &c.Security.SecretKey)
	set("GF_LOG_LEVEL", &c.Log.Level)
	set("GF_LOG_FORMAT", &c.Log.Format)
	setBool("GF_WORKER_ENABLED", &c.Worker.Enabled)
	if raw := os.Getenv("GF_TRUSTED_PROXIES"); raw != "" {
		c.HTTP.TrustedProxies = splitCSV(raw)
	}
	if raw := os.Getenv("GF_SESSION_TTL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			c.Security.SessionTTL = Duration(d)
		}
	}
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c *Config) Validate() error {
	switch c.Database.Driver {
	case "sqlite", "mysql", "postgres":
	default:
		return fmt.Errorf("unsupported database driver %q (want sqlite|mysql|postgres)", c.Database.Driver)
	}
	if strings.TrimSpace(c.Database.DSN) == "" {
		return fmt.Errorf("database.dsn is required")
	}
	if c.Database.MaxOpen < 1 {
		c.Database.MaxOpen = 25
	}
	if c.Database.MaxIdle < 0 {
		c.Database.MaxIdle = 0
	}
	if c.Security.SessionTTL.Std() <= 0 {
		c.Security.SessionTTL = Duration(30 * 24 * time.Hour)
	}
	if c.Security.SessionCookie == "" {
		c.Security.SessionCookie = "gf_session"
	}
	if c.HTTP.MaxBodyBytes <= 0 {
		c.HTTP.MaxBodyBytes = 32 << 20
	}
	// Production traffic must be HTTPS: session cookies are marked Secure based
	// on this URL, so an http:// base URL would silently send cookies in clear.
	// Production traffic must be HTTPS because session cookies are marked
	// Secure based on this URL. An empty value means "not configured yet"
	// (fresh install), so only a configured non-https URL is rejected here.
	if strings.EqualFold(c.Env, "production") && strings.TrimSpace(c.HTTP.BaseURL) != "" {
		if !strings.HasPrefix(strings.ToLower(c.HTTP.BaseURL), "https://") {
			return fmt.Errorf("http.base_url must use https in production (got %q)", c.HTTP.BaseURL)
		}
	}
	return nil
}

// Save writes the configuration to disk with restrictive permissions.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (c *Config) IsDevelopment() bool { return c.Env == "development" || c.Env == "dev" }

// DefaultConfigPath respects GF_CONFIG, then ./configs/goforum.yaml.
func DefaultConfigPath() string {
	if p := os.Getenv("GF_CONFIG"); p != "" {
		return p
	}
	return filepath.Join("configs", "goforum.yaml")
}
