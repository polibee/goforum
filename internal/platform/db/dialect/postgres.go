package dialect

import (
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type postgresDialect struct{}

func (postgresDialect) Name() string { return "postgres" }

func (postgresDialect) Open(dsn string) (*gorm.DB, error) {
	return gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Warn),
		DisableForeignKeyConstraintWhenMigrating: true,
		NowFunc:                                  utcNow,
	})
}

func (postgresDialect) SupportsFullText() bool { return true }

func (postgresDialect) ForUpdate(db *gorm.DB) *gorm.DB {
	return db.Clauses(clause.Locking{Strength: "UPDATE"})
}

func (postgresDialect) RandomOrder(db *gorm.DB) *gorm.DB { return db.Order("random()") }

func (postgresDialect) CaseInsensitiveLike(db *gorm.DB, column, pattern string) *gorm.DB {
	return db.Where(column+" ILIKE ?", pattern)
}
