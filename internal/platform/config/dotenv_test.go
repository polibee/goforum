package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnvBasics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := `# comment line
APP_ENV=production
APP_URL="https://forum.example.com"
DB_CONNECTION=pgsql
DB_PASSWORD="p@ss word#1"
EMPTY=
export EXPORTED=value
SPACED=hello world   # trailing comment
SINGLE='literal $APP_ENV'
EXPANDED="${APP_URL}/path"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := ParseDotEnv(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := map[string]string{
		"APP_ENV":       "production",
		"APP_URL":       "https://forum.example.com",
		"DB_CONNECTION": "pgsql",
		"DB_PASSWORD":   "p@ss word#1",
		"EMPTY":         "",
		"EXPORTED":      "value",
		"SPACED":        "hello world",
		"SINGLE":        "literal $APP_ENV",
		"EXPANDED":      "https://forum.example.com/path",
	}
	for key, want := range cases {
		if got := values[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestParseDotEnvMissingFile(t *testing.T) {
	values, err := ParseDotEnv(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("expected empty map, got %v", values)
	}
}

func TestBuildDSNPostgres(t *testing.T) {
	dsn, err := BuildDSN("pgsql", DBParams{
		Host: "db.internal", Port: "5433", Database: "forum",
		Username: "goforum", Password: "s3cret", SSLMode: "require",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"host=db.internal", "port=5433", "user=goforum",
		"password=s3cret", "dbname=forum", "sslmode=require",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn %q missing %q", dsn, want)
		}
	}
}

func TestBuildDSNMySQL(t *testing.T) {
	dsn, err := BuildDSN("mysql", DBParams{
		Host: "127.0.0.1", Database: "forum",
		Username: "goforum", Password: "s3cret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "goforum:s3cret@tcp(127.0.0.1:3306)/forum"; !strings.HasPrefix(dsn, want) {
		t.Errorf("dsn = %q, want prefix %q", dsn, want)
	}
}

func TestDotEnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "goforum.yaml")
	if err := os.WriteFile(yamlPath, []byte(`
env: production
http:
  addr: ":9000"
database:
  driver: sqlite
  dsn: ./data/from-yaml.db
`), 0o600); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(`
APP_ENV=development
HTTP_ADDR=127.0.0.1:9999
DB_CONNECTION=pgsql
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forum
DB_USERNAME=postgres
DB_PASSWORD=hunter2
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GF_ENV_FILE", envPath)
	// Clear any ambient overrides that would win over the file.
	for _, key := range []string{"GF_ENV", "GF_HTTP_ADDR", "GF_DB_DRIVER", "GF_DB_DSN"} {
		t.Setenv(key, "")
	}

	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Env != "development" {
		t.Errorf("env = %q, want development from .env", cfg.Env)
	}
	if cfg.HTTP.Addr != "127.0.0.1:9999" {
		t.Errorf("addr = %q, want .env value", cfg.HTTP.Addr)
	}
	if cfg.Database.Driver != "postgres" {
		t.Errorf("driver = %q, want postgres from .env", cfg.Database.Driver)
	}
	if !strings.Contains(cfg.Database.DSN, "dbname=forum") {
		t.Errorf("dsn = %q, want synthesized postgres dsn", cfg.Database.DSN)
	}
	if strings.Contains(cfg.Database.DSN, "from-yaml") {
		t.Errorf("dsn still references YAML: %q", cfg.Database.DSN)
	}
}

func TestEnvFileRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.Database.Driver = "postgres"
	cfg.Database.DSN = "host=db port=5432 user=u password=p dbname=d sslmode=require TimeZone=UTC"
	out := cfg.EnvFile()
	if !strings.Contains(out, "DB_CONNECTION=pgsql") {
		t.Errorf("missing DB_CONNECTION=pgsql:\n%s", out)
	}
	if !strings.Contains(out, "DB_HOST=db") || !strings.Contains(out, "DB_DATABASE=d") {
		t.Errorf("split database fields missing:\n%s", out)
	}
	if !strings.Contains(out, "DB_SSLMODE=require") {
		t.Errorf("sslmode missing:\n%s", out)
	}
}
