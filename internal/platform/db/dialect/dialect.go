package dialect

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

func utcNow() time.Time { return time.Now().UTC() }

// Dialect isolates database-specific behavior from services and handlers.
type Dialect interface {
	Name() string
	Open(dsn string) (*gorm.DB, error)
	SupportsFullText() bool
	ForUpdate(db *gorm.DB) *gorm.DB
	RandomOrder(db *gorm.DB) *gorm.DB
	CaseInsensitiveLike(db *gorm.DB, column, pattern string) *gorm.DB
}

var registry = map[string]Dialect{
	"sqlite":   sqliteDialect{},
	"mysql":    mysqlDialect{},
	"postgres": postgresDialect{},
}

func Get(name string) (Dialect, error) {
	d, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown database dialect %q", name)
	}
	return d, nil
}

func Names() []string { return []string{"sqlite", "mysql", "postgres"} }
