package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"goforum/internal/app"
	"goforum/internal/platform/config"
	applog "goforum/internal/platform/log"
)

var version = "dev"

func main() {
	cfgPath := config.DefaultConfigPath()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backup":
			os.Exit(runBackup(cfgPath, os.Args[2:]))
		case "restore":
			os.Exit(runRestore(os.Args[2:]))
		case "cleanup":
			os.Exit(runCleanup(cfgPath, os.Args[2:]))
		case "migrate":
			os.Exit(runMigrate(cfgPath))
		case "version":
			fmt.Println(version)
			return
		case "help", "-h", "--help":
			fmt.Println("usage: goforum [backup|restore|cleanup|migrate|version]")
			fmt.Println("  backup [-o DIR]   create database and uploads backup")
			fmt.Println("  cleanup [-n]      delete orphan rows (dry run with -n)")
			fmt.Println("  migrate           create/upgrade the database schema (backup first)")
			fmt.Println("  restore FILE      decrypt a backup file (needs GF_BACKUP_KEY)")
			return
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}
	logger := applog.New(cfg.Log.Level, cfg.Log.Format)

	var handler atomic.Value
	var current *app.App
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()

	current, err = app.Open(cfg, cfgPath, version, logger)
	if err != nil {
		if errors.Is(err, app.ErrSchemaIncomplete) {
			logger.Error("database schema is incomplete; run `goforum migrate` before starting", "error", err)
			os.Exit(1)
		}
		logger.Error("open app failed; entering installer", "error", err)
		current = nil
	}

	onReady := func(a *app.App) error {
		engine, err := a.Router()
		if err != nil {
			return err
		}
		handler.Store(http.Handler(engine))
		if current != nil && current != a {
			_ = current.Close()
		}
		current = a
		a.StartBackground(bgCtx)
		return nil
	}

	if current != nil && current.Installed(context.Background()) {
		if err := onReady(current); err != nil {
			logger.Error("build router failed", "error", err)
			os.Exit(1)
		}
	} else {
		installer, err := app.NewInstaller(current, cfg, cfgPath, version, logger, onReady)
		if err != nil {
			logger.Error("build installer failed", "error", err)
			os.Exit(1)
		}
		handler.Store(http.Handler(installer.Router()))
	}

	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := handler.Load()
		if h == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		h.(http.Handler).ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		dbInfo := cfg.Database.Driver
		if params := config.ParseDSNToParams(cfg.Database.Driver, cfg.Database.DSN); params.Database != "" {
			dbInfo += "/" + params.Database
		}
		logger.Info("goforum listening",
			"addr", cfg.HTTP.Addr,
			"version", version,
			"config", cfgPath,
			"env_file", config.EnvPath(),
			"db", dbInfo)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bgCancel()
	_ = srv.Shutdown(ctx)
	if current != nil {
		_ = current.Close()
	}
	logger.Info("shutdown complete")
}
