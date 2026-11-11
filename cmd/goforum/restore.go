package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"goforum/internal/platform/backup"
)

// runRestore decrypts a backup file so it can be inspected or fed to the
// database tools. It never writes into the live data directory: the caller
// picks an output path, which keeps this command safe to run alongside a
// running server.
func runRestore(args []string) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("o", "", "output file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: goforum restore [-o FILE] <backup-file>")
		return 2
	}
	src := fs.Arg(0)
	if *out == "" {
		*out = filepath.Base(src) + ".plain"
	}

	key, err := backup.KeyFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore failed:", err)
		return 1
	}
	plain, err := backup.DecryptFile(src, key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore failed:", err)
		return 1
	}
	if err := os.WriteFile(*out, plain, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write output failed:", err)
		return 1
	}
	fmt.Printf("decrypted %s -> %s (%d bytes)\n", src, *out, len(plain))
	return 0
}
