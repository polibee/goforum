package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// EnvPath is the .env file location. GF_ENV_FILE wins, then ./.env.
func EnvPath() string {
	if p := strings.TrimSpace(os.Getenv("GF_ENV_FILE")); p != "" {
		return p
	}
	return ".env"
}

// ParseDotEnv reads a Laravel-style .env file into a key/value map. Missing
// files are not an error: the caller simply gets an empty map.
//
// Supported syntax: KEY=VALUE, `export ` prefixes, # comments, single and
// double quotes, and ${VAR} references to earlier keys.
func ParseDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer f.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Text()
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		text = strings.TrimSpace(text)
		eq := strings.IndexByte(text, '=')
		if eq <= 0 {
			continue // tolerate stray lines instead of failing the boot
		}
		key := strings.TrimSpace(text[:eq])
		if key == "" {
			continue
		}
		value, err := parseDotEnvValue(strings.TrimSpace(text[eq+1:]))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		values[key] = expandVars(value, values)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseDotEnvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	switch raw[0] {
	case '"':
		end := closingQuote(raw, '"')
		if end < 0 {
			return "", fmt.Errorf("unterminated double quote")
		}
		inner := raw[1:end]
		inner = strings.ReplaceAll(inner, `\n`, "\n")
		inner = strings.ReplaceAll(inner, `\t`, "\t")
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		return inner, nil
	case '\'':
		end := closingQuote(raw, '\'')
		if end < 0 {
			return "", fmt.Errorf("unterminated single quote")
		}
		return raw[1:end], nil
	}
	// Unquoted: strip an inline comment that follows whitespace.
	if i := strings.Index(raw, " #"); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(raw), nil
}

func closingQuote(raw string, quote byte) int {
	for i := 1; i < len(raw); i++ {
		if raw[i] == '\\' && quote == '"' {
			i++
			continue
		}
		if raw[i] == quote {
			return i
		}
	}
	return -1
}

func expandVars(value string, known map[string]string) string {
	if !strings.Contains(value, "${") {
		return value
	}
	var b strings.Builder
	for i := 0; i < len(value); {
		if value[i] == '$' && i+1 < len(value) && value[i+1] == '{' {
			end := strings.IndexByte(value[i+2:], '}')
			if end >= 0 {
				name := value[i+2 : i+2+end]
				if v, ok := known[name]; ok {
					b.WriteString(v)
				} else if v, ok := os.LookupEnv(name); ok {
					b.WriteString(v)
				}
				i += 2 + end + 1
				continue
			}
		}
		b.WriteByte(value[i])
		i++
	}
	return b.String()
}

// envLookup resolves a key from the real environment first, then .env. This
// matches Laravel: exported shell variables win over the file.
func envLookup(dotenv map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		if v, ok := os.LookupEnv(key); ok {
			return v, true
		}
		v, ok := dotenv[key]
		return v, ok
	}
}

// applyDotEnv maps .env keys onto the config. Keys mirror Laravel conventions
// so operators can reuse muscle memory: DB_CONNECTION, DB_HOST, DB_DATABASE…
func (c *Config) applyDotEnv(dotenv map[string]string) {
	get := envLookup(dotenv)
	str := func(key string, dst *string) {
		if v, ok := get(key); ok {
			*dst = strings.TrimSpace(v)
		}
	}
	boolean := func(key string, dst *bool) {
		if v, ok := get(key); ok {
			*dst = truthy(v)
		}
	}
	integer := func(key string, dst *int) {
		if v, ok := get(key); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				*dst = n
			}
		}
	}
	int64Value := func(key string, dst *int64) {
		if v, ok := get(key); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				*dst = n
			}
		}
	}
	duration := func(key string, dst *Duration) {
		if v, ok := get(key); ok {
			if d, err := parseFlexibleDuration(v); err == nil {
				*dst = Duration(d)
			}
		}
	}

	str("APP_ENV", &c.Env)
	str("APP_URL", &c.HTTP.BaseURL)
	str("APP_KEY", &c.Security.SecretKey)

	str("HTTP_ADDR", &c.HTTP.Addr)
	bodyMegabytes := 0
	integer("HTTP_MAX_BODY_MB", &bodyMegabytes)
	int64Value("HTTP_MAX_BODY_BYTES", &c.HTTP.MaxBodyBytes)
	if bodyMegabytes > 0 {
		c.HTTP.MaxBodyBytes = int64(bodyMegabytes) << 20
	}
	boolean("HTTP_METRICS_ENABLED", &c.HTTP.MetricsEnabled)
	str("HTTP_METRICS_TOKEN", &c.HTTP.MetricsToken)
	if v, ok := get("HTTP_TRUSTED_PROXIES"); ok {
		c.HTTP.TrustedProxies = splitCSV(v)
	}

	if v, ok := get("DB_CONNECTION"); ok {
		c.Database.Driver = normalizeDriver(v)
	}
	str("DB_DSN", &c.Database.DSN)
	dbHost, hasHost := get("DB_HOST")
	dbPort, _ := get("DB_PORT")
	dbName, hasName := get("DB_DATABASE")
	dbUser, hasUser := get("DB_USERNAME")
	dbPass, hasPass := get("DB_PASSWORD")
	dbSSL, _ := get("DB_SSLMODE")
	// Only synthesize a DSN when split fields are present; DB_DSN stays authoritative.
	if hasHost || hasName || hasUser || hasPass {
		dsn, err := BuildDSN(c.Database.Driver, DBParams{
			Host:     strings.TrimSpace(dbHost),
			Port:     strings.TrimSpace(dbPort),
			Database: strings.TrimSpace(dbName),
			Username: strings.TrimSpace(dbUser),
			Password: dbPass,
			SSLMode:  strings.TrimSpace(dbSSL),
		})
		if err == nil && dsn != "" {
			// An explicit DB_DSN always wins over synthesized fields.
			if _, ok := get("DB_DSN"); !ok {
				c.Database.DSN = dsn
			} else if strings.TrimSpace(c.Database.DSN) == "" {
				c.Database.DSN = dsn
			}
		}
	}
	integer("DB_MAX_OPEN", &c.Database.MaxOpen)
	integer("DB_MAX_IDLE", &c.Database.MaxIdle)
	duration("DB_MAX_LIFETIME", &c.Database.ConnMaxLifetime)

	str("CACHE_STORE", &c.Cache.Driver)
	str("CACHE_DRIVER", &c.Cache.Driver)
	str("CACHE_PREFIX", &c.Cache.Prefix)
	str("REDIS_URL", &c.Cache.RedisURL)
	duration("CACHE_TTL", &c.Cache.TTL)

	str("FILESYSTEM_DISK", &c.Storage.Driver)
	str("STORAGE_DRIVER", &c.Storage.Driver)
	str("STORAGE_PATH", &c.Storage.LocalPath)
	str("STORAGE_URL", &c.Storage.BaseURL)

	boolean("MAIL_ENABLED", &c.Mail.Enabled)
	if v, ok := get("MAIL_MAILER"); ok {
		// Laravel naming: log|smtp. Anything else enables SMTP.
		mailer := strings.ToLower(strings.TrimSpace(v))
		c.Mail.Enabled = mailer != "" && mailer != "log" && mailer != "none"
	}
	str("MAIL_HOST", &c.Mail.SMTPHost)
	integer("MAIL_PORT", &c.Mail.SMTPPort)
	str("MAIL_USERNAME", &c.Mail.Username)
	str("MAIL_PASSWORD", &c.Mail.Password)
	str("MAIL_FROM_ADDRESS", &c.Mail.From)
	str("MAIL_FROM", &c.Mail.From)

	str("LOG_LEVEL", &c.Log.Level)
	str("LOG_CHANNEL", &c.Log.Format)
	str("LOG_FORMAT", &c.Log.Format)

	str("SESSION_COOKIE", &c.Security.SessionCookie)
	duration("SESSION_LIFETIME", &c.Security.SessionTTL)

	boolean("WORKER_ENABLED", &c.Worker.Enabled)
	boolean("QUEUE_WORKER", &c.Worker.Enabled)
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "(true)":
		return true
	}
	return false
}

func normalizeDriver(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "pgsql", "postgres", "postgresql":
		return "postgres"
	case "mysql", "mariadb":
		return "mysql"
	case "sqlite", "sqlite3":
		return "sqlite"
	}
	return strings.ToLower(strings.TrimSpace(v))
}

func parseFlexibleDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Accept a bare number as seconds, like Laravel's SESSION_LIFETIME=120.
	if n, err := strconv.Atoi(raw); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return time.ParseDuration(raw)
}

// DBParams holds the individual database fields used to build a DSN.
type DBParams struct {
	Host     string
	Port     string
	Database string
	Username string
	Password string
	SSLMode  string
}

// BuildDSN assembles a driver DSN from Laravel-style fields.
func BuildDSN(driver string, p DBParams) (string, error) {
	switch normalizeDriver(driver) {
	case "sqlite":
		if p.Database == "" {
			return "", fmt.Errorf("DB_DATABASE is required for sqlite")
		}
		return p.Database, nil
	case "mysql":
		host := p.Host
		if host == "" {
			host = "127.0.0.1"
		}
		port := p.Port
		if port == "" {
			port = "3306"
		}
		user := p.Username
		if user == "" {
			user = "root"
		}
		pass := p.Password
		db := p.Database
		if db == "" {
			return "", fmt.Errorf("DB_DATABASE is required for mysql")
		}
		credentials := user
		if pass != "" {
			credentials += ":" + pass
		}
		return fmt.Sprintf("%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=UTC",
			credentials, host, port, db), nil
	case "postgres":
		host := p.Host
		if host == "" {
			host = "127.0.0.1"
		}
		port := p.Port
		if port == "" {
			port = "5432"
		}
		db := p.Database
		if db == "" {
			return "", fmt.Errorf("DB_DATABASE is required for pgsql")
		}
		ssl := p.SSLMode
		if ssl == "" {
			ssl = "disable"
		}
		parts := []string{
			"host=" + quoteDSNValue(host),
			"port=" + port,
			"user=" + quoteDSNValue(p.Username),
			"password=" + quoteDSNValue(p.Password),
			"dbname=" + quoteDSNValue(db),
			"sslmode=" + ssl,
			"TimeZone=UTC",
		}
		return strings.Join(parts, " "), nil
	}
	return "", fmt.Errorf("unsupported DB_CONNECTION %q (want sqlite|mysql|pgsql)", driver)
}

// quoteDSNValue quotes a libpq keyword value when it contains spaces or quotes.
func quoteDSNValue(v string) string {
	if v == "" {
		return "''"
	}
	if !strings.ContainsAny(v, " '\\") {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}

// AppendDatabaseEnv records the database selected by the installer as a final
// override block. Appending (instead of rewriting) preserves operator comments
// and unrelated keys; the dotenv parser lets the last value win.
func AppendDatabaseEnv(path, driver, dsn string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f,
		"\n# --- database selected by the installer ---\nDB_CONNECTION=%s\nDB_DSN=%s\n",
		normalizeDriver(driver), quoteEnvValue(dsn))
	return err
}

func quoteEnvValue(v string) string {
	if v == "" {
		return `""`
	}
	if !strings.ContainsAny(v, " \t#\"'\\") {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

// SaveEnv writes the current config as a Laravel-style .env file.
func (c *Config) SaveEnv(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(c.EnvFile()), 0o600)
}

// EnvFile renders the .env representation of this config.
func (c *Config) EnvFile() string {
	driver := c.Database.Driver
	conn := driver
	if driver == "postgres" {
		conn = "pgsql"
	}
	params := ParseDSNToParams(driver, c.Database.DSN)

	var b strings.Builder
	b.WriteString("# GoForum environment configuration (Laravel-style .env)\n")
	b.WriteString("# Lines use KEY=VALUE. Quote values that contain spaces.\n")
	b.WriteString("# Shell-exported variables override this file.\n\n")

	b.WriteString("# --- Application ---\n")
	fmt.Fprintf(&b, "APP_ENV=%s\n", c.Env)
	fmt.Fprintf(&b, "APP_URL=%s\n", c.HTTP.BaseURL)
	if c.Security.SecretKey != "" {
		fmt.Fprintf(&b, "APP_KEY=%s\n", c.Security.SecretKey)
	} else {
		b.WriteString("APP_KEY=\n")
	}
	fmt.Fprintf(&b, "HTTP_ADDR=%s\n", c.HTTP.Addr)
	fmt.Fprintf(&b, "HTTP_MAX_BODY_MB=%d\n", c.HTTP.MaxBodyBytes>>20)
	if len(c.HTTP.TrustedProxies) > 0 {
		fmt.Fprintf(&b, "HTTP_TRUSTED_PROXIES=%s\n", strings.Join(c.HTTP.TrustedProxies, ","))
	} else {
		b.WriteString("HTTP_TRUSTED_PROXIES=\n")
	}
	fmt.Fprintf(&b, "HTTP_METRICS_ENABLED=%t\n", c.HTTP.MetricsEnabled)
	if c.HTTP.MetricsToken != "" {
		fmt.Fprintf(&b, "HTTP_METRICS_TOKEN=%s\n", c.HTTP.MetricsToken)
	}

	b.WriteString("\n# --- Database ---\n")
	b.WriteString("# DB_CONNECTION: sqlite | mysql | pgsql\n")
	fmt.Fprintf(&b, "DB_CONNECTION=%s\n", conn)
	if driver == "sqlite" {
		fmt.Fprintf(&b, "DB_DATABASE=%s\n", params.Database)
	} else {
		fmt.Fprintf(&b, "DB_HOST=%s\n", params.Host)
		fmt.Fprintf(&b, "DB_PORT=%s\n", params.Port)
		fmt.Fprintf(&b, "DB_DATABASE=%s\n", params.Database)
		fmt.Fprintf(&b, "DB_USERNAME=%s\n", params.Username)
		fmt.Fprintf(&b, "DB_PASSWORD=\"%s\"\n", strings.ReplaceAll(params.Password, `"`, `\"`))
		if driver == "postgres" {
			fmt.Fprintf(&b, "DB_SSLMODE=%s\n", params.SSLMode)
		}
	}
	fmt.Fprintf(&b, "DB_MAX_OPEN=%d\n", c.Database.MaxOpen)
	fmt.Fprintf(&b, "DB_MAX_IDLE=%d\n", c.Database.MaxIdle)
	fmt.Fprintf(&b, "DB_MAX_LIFETIME=%s\n", c.Database.ConnMaxLifetime.Std())

	b.WriteString("\n# --- Cache ---\n")
	b.WriteString("# CACHE_STORE: memory | redis\n")
	fmt.Fprintf(&b, "CACHE_STORE=%s\n", c.Cache.Driver)
	if c.Cache.RedisURL != "" {
		fmt.Fprintf(&b, "REDIS_URL=%s\n", c.Cache.RedisURL)
	} else {
		b.WriteString("REDIS_URL=\n")
	}
	fmt.Fprintf(&b, "CACHE_TTL=%s\n", c.Cache.TTL.Std())

	b.WriteString("\n# --- Storage ---\n")
	fmt.Fprintf(&b, "FILESYSTEM_DISK=%s\n", c.Storage.Driver)
	fmt.Fprintf(&b, "STORAGE_PATH=%s\n", c.Storage.LocalPath)
	fmt.Fprintf(&b, "STORAGE_URL=%s\n", c.Storage.BaseURL)

	b.WriteString("\n# --- Mail ---\n")
	b.WriteString("# MAIL_MAILER: log | smtp\n")
	if c.Mail.Enabled {
		b.WriteString("MAIL_MAILER=smtp\n")
	} else {
		b.WriteString("MAIL_MAILER=log\n")
	}
	fmt.Fprintf(&b, "MAIL_HOST=%s\n", c.Mail.SMTPHost)
	fmt.Fprintf(&b, "MAIL_PORT=%d\n", c.Mail.SMTPPort)
	fmt.Fprintf(&b, "MAIL_USERNAME=%s\n", c.Mail.Username)
	fmt.Fprintf(&b, "MAIL_PASSWORD=\"%s\"\n", strings.ReplaceAll(c.Mail.Password, `"`, `\"`))
	fmt.Fprintf(&b, "MAIL_FROM_ADDRESS=%s\n", c.Mail.From)

	b.WriteString("\n# --- Logging ---\n")
	fmt.Fprintf(&b, "LOG_LEVEL=%s\n", c.Log.Level)
	fmt.Fprintf(&b, "LOG_CHANNEL=%s\n", c.Log.Format)

	b.WriteString("\n# --- Session ---\n")
	fmt.Fprintf(&b, "SESSION_COOKIE=%s\n", c.Security.SessionCookie)
	fmt.Fprintf(&b, "SESSION_LIFETIME=%s\n", c.Security.SessionTTL.Std())

	b.WriteString("\n# --- Workers ---\n")
	fmt.Fprintf(&b, "WORKER_ENABLED=%t\n", c.Worker.Enabled)
	return b.String()
}

// ParseDSNToParams reverses a DSN into the split fields used by .env.
func ParseDSNToParams(driver, dsn string) DBParams {
	p := DBParams{SSLMode: "disable"}
	switch normalizeDriver(driver) {
	case "sqlite":
		p.Database = dsn
	case "postgres":
		for _, field := range splitDSNFields(dsn) {
			parts := strings.SplitN(field, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := parts[0]
			value := strings.Trim(parts[1], `'"`)
			switch key {
			case "host":
				p.Host = value
			case "port":
				p.Port = value
			case "user":
				p.Username = value
			case "password":
				p.Password = value
			case "dbname":
				p.Database = value
			case "sslmode":
				p.SSLMode = value
			}
		}
		if p.Port == "" {
			p.Port = "5432"
		}
	case "mysql":
		// user:pass@tcp(host:port)/dbname?params
		rest := dsn
		if at := strings.LastIndex(rest, "@tcp("); at >= 0 {
			credentials := rest[:at]
			rest = rest[at+5:]
			if colon := strings.Index(credentials, ":"); colon >= 0 {
				p.Username = credentials[:colon]
				p.Password = credentials[colon+1:]
			} else {
				p.Username = credentials
			}
		}
		if close := strings.Index(rest, ")"); close >= 0 {
			hostPort := rest[:close]
			rest = rest[close+1:]
			if colon := strings.LastIndex(hostPort, ":"); colon >= 0 {
				p.Host = hostPort[:colon]
				p.Port = hostPort[colon+1:]
			} else {
				p.Host = hostPort
			}
		}
		rest = strings.TrimPrefix(rest, "/")
		if q := strings.Index(rest, "?"); q >= 0 {
			rest = rest[:q]
		}
		p.Database = rest
		if p.Port == "" {
			p.Port = "3306"
		}
	}
	return p
}

// splitDSNFields splits a libpq keyword/value string honoring single quotes.
func splitDSNFields(dsn string) []string {
	var out []string
	var current strings.Builder
	inQuote := false
	for i := 0; i < len(dsn); i++ {
		ch := dsn[i]
		switch {
		case ch == '\'':
			inQuote = !inQuote
			current.WriteByte(ch)
		case ch == '\\' && inQuote && i+1 < len(dsn):
			current.WriteByte(ch)
			i++
			current.WriteByte(dsn[i])
		case ch == ' ' && !inQuote:
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}
