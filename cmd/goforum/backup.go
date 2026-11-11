package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"goforum/internal/platform/backup"
	"goforum/internal/platform/config"
)

func runBackup(cfgPath string, args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("o", "", "output directory (default backups/<timestamp>)")
	timeout := fs.Duration("timeout", 30*time.Minute, "backup timeout")
	plain := fs.Bool("plaintext", false, "write an unencrypted backup (not recommended)")
	genKey := fs.Bool("gen-key", false, "print a new random backup key and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *genKey {
		k, err := backup.GenerateKey()
		if err != nil {
			fmt.Fprintln(os.Stderr, "generate key:", err)
			return 1
		}
		fmt.Println(k)
		fmt.Fprintln(os.Stderr, "export GF_BACKUP_KEY=<the value above> and store it separately from backups")
		return 0
	}
	res, err := backup.RunWithOptions(ctx, cfg, version, *out, backup.Options{AllowPlaintext: *plain})
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup failed:", err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
	return 0
}
