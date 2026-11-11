package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Fatalf("default driver = %q", cfg.Database.Driver)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("GF_DB_DRIVER", "mysql")
	t.Setenv("GF_DB_DSN", "user:pass@tcp(127.0.0.1:3306)/goforum?charset=utf8mb4&parseTime=True&loc=UTC")
	t.Setenv("GF_HTTP_ADDR", "127.0.0.1:19090")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Database.Driver != "mysql" {
		t.Fatalf("driver = %q", cfg.Database.Driver)
	}
	if cfg.HTTP.Addr != "127.0.0.1:19090" {
		t.Fatalf("addr = %q", cfg.HTTP.Addr)
	}
}

func TestInvalidDriver(t *testing.T) {
	cfg := Default()
	cfg.Database.Driver = "oracle"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unsupported driver error")
	}
}

func TestDuration(t *testing.T) {
	var v struct {
		D Duration `yaml:"d"`
	}
	if err := yaml.Unmarshal([]byte("d: 30m"), &v); err != nil {
		t.Fatalf("duration: %v", err)
	}
	if v.D.Std().Minutes() != 30 {
		t.Fatalf("duration = %v", v.D.Std())
	}
}
