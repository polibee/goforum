package main

import (
	"fmt"
	"os"

	"goforum/internal/app"
	"goforum/internal/platform/config"
	applog "goforum/internal/platform/log"
)

// runMigrate applies the current schema to an existing database. It is the
// production upgrade path because startup intentionally does not AutoMigrate.
func runMigrate(cfgPath string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	logger := applog.New(cfg.Log.Level, cfg.Log.Format)
	if err := app.Migrate(cfg, logger); err != nil {
		fmt.Fprintln(os.Stderr, "migrate failed:", err)
		return 1
	}
	fmt.Println("database schema is up to date")
	return 0
}
