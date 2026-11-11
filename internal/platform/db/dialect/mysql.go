package dialect

import (
	"net/url"
	"strings"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type mysqlDialect struct{}

func (mysqlDialect) Name() string { return "mysql" }

// Open connects to MySQL.
//
// parseTime=True is mandatory: without it the driver returns DATETIME columns
// as []byte and every scan into time.Time fails with
// "unsupported Scan, storing driver.Value type []uint8". The installer and
// hand-written DSNs routinely omit it, so it is enforced here instead of
// relying on the operator.
func (mysqlDialect) Open(dsn string) (*gorm.DB, error) {
	return gorm.Open(mysql.Open(ensureMySQLParams(dsn)), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Warn),
		DisableForeignKeyConstraintWhenMigrating: true,
		NowFunc:                                  utcNow,
	})
}

func (mysqlDialect) SupportsFullText() bool { return true }

func (mysqlDialect) ForUpdate(db *gorm.DB) *gorm.DB {
	return db.Clauses(clause.Locking{Strength: "UPDATE"})
}

func (mysqlDialect) RandomOrder(db *gorm.DB) *gorm.DB { return db.Order("RAND()") }

func (mysqlDialect) CaseInsensitiveLike(db *gorm.DB, column, pattern string) *gorm.DB {
	return db.Where(column+" LIKE ?", pattern)
}

// ensureMySQLParams adds the parameters the driver needs to be usable.
func ensureMySQLParams(dsn string) string {
	base, rawQuery, hasQuery := strings.Cut(dsn, "?")
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		values = url.Values{}
	}
	if !strings.EqualFold(values.Get("parseTime"), "true") {
		values.Set("parseTime", "true")
	}
	if values.Get("charset") == "" {
		values.Set("charset", "utf8mb4")
	}
	if values.Get("loc") == "" {
		values.Set("loc", "UTC")
	}
	if !hasQuery {
		return base + "?" + values.Encode()
	}
	return base + "?" + values.Encode()
}
