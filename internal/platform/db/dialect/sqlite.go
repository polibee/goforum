package dialect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sqliteDialect struct{}

func (sqliteDialect) Name() string { return "sqlite" }

func (sqliteDialect) Open(dsn string) (*gorm.DB, error) {
	path := dsn
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite dir: %w", err)
		}
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn = dsn + sep + "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"

	return gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Warn),
		DisableForeignKeyConstraintWhenMigrating: true,
		NowFunc:                                  utcNow,
	})
}

func (sqliteDialect) SupportsFullText() bool { return true }

func (sqliteDialect) ForUpdate(db *gorm.DB) *gorm.DB { return db }

func (sqliteDialect) RandomOrder(db *gorm.DB) *gorm.DB { return db.Order("RANDOM()") }

func (sqliteDialect) CaseInsensitiveLike(db *gorm.DB, column, pattern string) *gorm.DB {
	return db.Where("LOWER("+column+") LIKE LOWER(?)", pattern)
}
