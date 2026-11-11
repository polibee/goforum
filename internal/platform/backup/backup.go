// Package backup creates consistent database and upload backups.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"goforum/internal/platform/config"
	"goforum/internal/platform/db"
)

type FileInfo struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Driver    string    `json:"driver"`
	// Encrypted marks the payload files as AES-256-GCM (magic header "GFBK1").
	Encrypted bool       `json:"encrypted"`
	Files     []FileInfo `json:"files"`
}

type Result struct {
	Dir      string   `json:"dir"`
	Manifest Manifest `json:"manifest"`
}

// Run creates a backup under outDir (or backups/<timestamp> when empty).
// Passwords and secrets inside are encrypted unless plaintext is explicitly
// allowed; see encrypt.go.
func Run(ctx context.Context, cfg *config.Config, version, outDir string) (*Result, error) {
	return RunWithOptions(ctx, cfg, version, outDir, Options{})
}

// Options controls how a backup is written.
type Options struct {
	// AllowPlaintext writes an unencrypted backup. It must be set explicitly;
	// the default refuses to run without a key.
	AllowPlaintext bool
	// EncryptionKey overrides GF_BACKUP_KEY.
	EncryptionKey string
}

// RunWithOptions creates a backup with explicit encryption settings.
func RunWithOptions(ctx context.Context, cfg *config.Config, version, outDir string, opts Options) (*Result, error) {
	key, err := resolveKey(opts.EncryptionKey, opts.AllowPlaintext)
	if err != nil {
		return nil, err
	}
	if outDir == "" {
		outDir = filepath.Join("backups", time.Now().UTC().Format("20060102-150405"))
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}
	manifest := Manifest{Version: version, CreatedAt: time.Now().UTC(), Driver: cfg.Database.Driver}

	dbFile := filepath.Join(outDir, "database.sql")
	switch cfg.Database.Driver {
	case "sqlite":
		dbFile = filepath.Join(outDir, "database.db")
		if err := backupSQLite(ctx, cfg, dbFile); err != nil {
			return nil, err
		}
	case "mysql":
		if err := backupMySQL(ctx, cfg, dbFile); err != nil {
			return nil, err
		}
	case "postgres":
		if err := backupPostgres(ctx, cfg, dbFile); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported driver %q", cfg.Database.Driver)
	}
	if key != nil {
		if err := encryptFile(dbFile, key); err != nil {
			return nil, fmt.Errorf("encrypt database backup: %w", err)
		}
		manifest.Encrypted = true
	}
	info, err := fileHash(dbFile)
	if err != nil {
		return nil, err
	}
	manifest.Files = append(manifest.Files, info)

	if cfg.Storage.Driver == "local" && cfg.Storage.LocalPath != "" {
		if st, err := os.Stat(cfg.Storage.LocalPath); err == nil && st.IsDir() {
			archive := filepath.Join(outDir, "uploads.tar.gz")
			if err := tarDir(cfg.Storage.LocalPath, archive); err != nil {
				return nil, fmt.Errorf("archive uploads: %w", err)
			}
			if key != nil {
				if err := encryptFile(archive, key); err != nil {
					return nil, fmt.Errorf("encrypt uploads backup: %w", err)
				}
				manifest.Encrypted = true
			}
			info, err := fileHash(archive)
			if err != nil {
				return nil, err
			}
			manifest.Files = append(manifest.Files, info)
		}
	}

	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "manifest.json"), payload, 0o600); err != nil {
		return nil, err
	}
	return &Result{Dir: outDir, Manifest: manifest}, nil
}

func backupSQLite(ctx context.Context, cfg *config.Config, target string) error {
	gdb, err := db.Open(cfg.Database)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	safe := strings.ReplaceAll(target, "'", "''")
	if _, err := sqlDB.ExecContext(ctx, "VACUUM INTO '"+safe+"'"); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	return nil
}

var mysqlDSN = regexp.MustCompile(`^(?:([^:]+):([^@]*)@)?tcp\(([^)]+)\)/([^?]+)`)

func backupMySQL(ctx context.Context, cfg *config.Config, target string) error {
	bin, err := exec.LookPath("mysqldump")
	if err != nil {
		return fmt.Errorf("mysqldump not found; install MySQL client or use logical export")
	}
	m := mysqlDSN.FindStringSubmatch(cfg.Database.DSN)
	if m == nil {
		return fmt.Errorf("unsupported MySQL DSN format")
	}
	user, pass, host, name := m[1], m[2], m[3], m[4]
	args := []string{"--single-transaction", "--quick", "--routines", "--triggers", "-h", host, "-u", user, name}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+pass)
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer out.Close()
	cmd.Stdout = out
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mysqldump: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// backupPostgres runs pg_dump without putting the password on the command line.
// A DSN passed to --dbname appears in the process argv, which any local user can
// read via ps; the pieces are split and the secret goes through PGPASSWORD.
func backupPostgres(ctx context.Context, cfg *config.Config, target string) error {
	bin, err := exec.LookPath("pg_dump")
	if err != nil {
		return fmt.Errorf("pg_dump not found; install PostgreSQL client or use logical export")
	}
	params := config.ParseDSNToParams("postgres", cfg.Database.DSN)

	args := []string{"--no-owner", "--no-privileges", "--format=plain", "--file=" + target}
	if params.Host != "" {
		args = append(args, "--host="+params.Host)
	}
	if params.Port != "" {
		args = append(args, "--port="+params.Port)
	}
	if params.Username != "" {
		args = append(args, "--username="+params.Username)
	}
	if params.Database != "" {
		args = append(args, "--dbname="+params.Database)
	}
	if strings.EqualFold(params.SSLMode, "disable") {
		args = append(args, "--no-password")
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	env := os.Environ()
	if params.SSLMode != "" {
		env = append(env, "PGSSLMODE="+params.SSLMode)
	}
	if params.Password != "" {
		env = append(env, "PGPASSWORD="+params.Password)
	}
	cmd.Env = env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func fileHash(path string) (FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileInfo{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return FileInfo{}, err
	}
	st, err := f.Stat()
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Name:   filepath.Base(path),
		Size:   st.Size(),
		SHA256: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

func tarDir(src, target string) error {
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == src {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}
