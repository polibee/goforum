package searchx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type docRow struct {
	ID         string    `gorm:"primaryKey;size:26"`
	Type       string    `gorm:"column:doc_type;size:16;index"`
	TopicID    string    `gorm:"size:26;index"`
	Title      string    `gorm:"size:255"`
	Body       string    `gorm:"type:text"`
	CategoryID string    `gorm:"size:26;index"`
	AuthorID   string    `gorm:"size:26;index"`
	Visibility string    `gorm:"size:16;index"`
	UpdatedAt  time.Time `gorm:"index"`
}

func (docRow) TableName() string { return "search_documents" }

// DBEngine keeps a unified document table and layers the native full-text
// implementation of each dialect on top. Any unsupported path falls back to LIKE.
type DBEngine struct {
	db      *gorm.DB
	dialect string
	fts     bool
}

func NewDBEngine(db *gorm.DB) *DBEngine {
	return &DBEngine{db: db, dialect: db.Dialector.Name()}
}

func (e *DBEngine) EnsureSchema(ctx context.Context) error {
	if err := e.db.WithContext(ctx).AutoMigrate(&docRow{}); err != nil {
		return err
	}
	switch e.dialect {
	case "sqlite":
		if err := e.db.WithContext(ctx).Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS search_fts
			USING fts5(doc_id UNINDEXED, doc_type UNINDEXED, title, body, tokenize='trigram')`).Error; err == nil {
			e.fts = true
			return nil
		}
		if err := e.db.WithContext(ctx).Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS search_fts
			USING fts5(doc_id UNINDEXED, doc_type UNINDEXED, title, body)`).Error; err == nil {
			e.fts = true
		}
	case "mysql":
		// ngram parser is optional; ignore failures and fall back to LIKE.
		if err := e.db.WithContext(ctx).Exec(`CREATE FULLTEXT INDEX ft_search_documents
			ON search_documents (title, body) WITH PARSER ngram`).Error; err == nil {
			e.fts = true
		} else if err := e.db.WithContext(ctx).Exec(`CREATE FULLTEXT INDEX ft_search_documents
			ON search_documents (title, body)`).Error; err == nil || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			e.fts = true
		}
	case "postgres":
		if err := e.db.WithContext(ctx).Exec(`ALTER TABLE search_documents
			ADD COLUMN IF NOT EXISTS search_vector tsvector`).Error; err != nil {
			return nil
		}
		if err := e.db.WithContext(ctx).Exec(`CREATE INDEX IF NOT EXISTS idx_search_documents_vector
			ON search_documents USING GIN (search_vector)`).Error; err != nil {
			return nil
		}
		_ = e.db.WithContext(ctx).Exec(`UPDATE search_documents
			SET search_vector = to_tsvector('simple', title || ' ' || body)
			WHERE search_vector IS NULL`).Error
		e.fts = true
	}
	return nil
}

func (e *DBEngine) Index(ctx context.Context, doc Document) error {
	if doc.ID == "" {
		return nil
	}
	visibility := doc.Visibility
	if visibility == "" {
		visibility = "public"
	}
	row := docRow{
		ID: doc.ID, Type: doc.Type, TopicID: doc.TopicID,
		Title: doc.Title, Body: doc.Body,
		CategoryID: doc.CategoryID, AuthorID: doc.AuthorID,
		Visibility: visibility, UpdatedAt: time.Now().UTC(),
	}
	if err := e.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"doc_type", "topic_id", "title", "body", "category_id", "author_id", "visibility", "updated_at",
		}),
	}).Create(&row).Error; err != nil {
		return err
	}

	switch e.dialect {
	case "sqlite":
		if e.fts {
			if err := e.db.WithContext(ctx).Exec(`DELETE FROM search_fts WHERE doc_id = ?`, doc.ID).Error; err != nil {
				return err
			}
			return e.db.WithContext(ctx).Exec(`INSERT INTO search_fts(doc_id, doc_type, title, body) VALUES (?, ?, ?, ?)`,
				doc.ID, doc.Type, doc.Title, doc.Body).Error
		}
	case "postgres":
		if e.fts {
			return e.db.WithContext(ctx).Exec(`UPDATE search_documents
				SET search_vector = to_tsvector('simple', ? || ' ' || ?) WHERE id = ?`,
				doc.Title, doc.Body, doc.ID).Error
		}
	}
	return nil
}

func (e *DBEngine) Delete(ctx context.Context, docType, id string) error {
	if err := e.db.WithContext(ctx).Where("id = ? AND doc_type = ?", id, docType).Delete(&docRow{}).Error; err != nil {
		return err
	}
	if e.dialect == "sqlite" && e.fts {
		return e.db.WithContext(ctx).Exec(`DELETE FROM search_fts WHERE doc_id = ?`, id).Error
	}
	return nil
}

type searchRow struct {
	ID      string `gorm:"column:id"`
	Type    string `gorm:"column:doc_type"`
	TopicID string `gorm:"column:topic_id"`
	Title   string `gorm:"column:title"`
	Excerpt string `gorm:"column:excerpt"`
}

func (e *DBEngine) Search(ctx context.Context, q Query) ([]Result, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, nil
	}
	if q.PerPage <= 0 || q.PerPage > 100 {
		q.PerPage = 20
	}
	if q.Page < 1 {
		q.Page = 1
	}
	offset := (q.Page - 1) * q.PerPage

	if e.fts && !(e.dialect == "sqlite" && len([]rune(text)) < 3) {
		if rows, err := e.searchFTS(ctx, text, q, offset); err == nil {
			return rows, nil
		}
	}
	return e.searchLike(ctx, text, q, offset)
}

func (e *DBEngine) searchFTS(ctx context.Context, text string, q Query, offset int) ([]Result, error) {
	var sql string
	var args []any
	switch e.dialect {
	case "sqlite":
		escaped := `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
		sql = `SELECT d.id AS id, d.doc_type AS doc_type, d.topic_id AS topic_id, d.title AS title,
			snippet(search_fts, 3, '', '', '…', 16) AS excerpt
			FROM search_fts JOIN search_documents d ON d.id = search_fts.doc_id
			WHERE search_fts MATCH ? AND d.visibility = 'public'`
		args = append(args, escaped)
		sql, args = appendFilters(sql, args, q)
		order := "bm25(search_fts)"
		if q.Sort == "latest" {
			order = "d.updated_at DESC"
		}
		sql += ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
		args = append(args, q.PerPage, offset)
	case "mysql":
		sql = `SELECT id, doc_type, topic_id, title, SUBSTRING(body, 1, 200) AS excerpt
			FROM search_documents
			WHERE MATCH(title, body) AGAINST (? IN NATURAL LANGUAGE MODE) AND visibility = 'public'`
		args = append(args, text)
		sql, args = appendFilters(sql, args, q)
		order := "MATCH(title, body) AGAINST (? IN NATURAL LANGUAGE MODE) DESC"
		if q.Sort == "latest" {
			order = "updated_at DESC"
			sql += ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
			args = append(args, q.PerPage, offset)
			break
		}
		sql += ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
		args = append(args, text, q.PerPage, offset)
	case "postgres":
		sql = `SELECT id, doc_type, topic_id, title, LEFT(body, 200) AS excerpt
			FROM search_documents
			WHERE search_vector @@ plainto_tsquery('simple', ?) AND visibility = 'public'`
		args = append(args, text)
		sql, args = appendFilters(sql, args, q)
		order := "ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC"
		if q.Sort == "latest" {
			order = "updated_at DESC"
			sql += ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
			args = append(args, q.PerPage, offset)
			break
		}
		sql += ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
		args = append(args, text, q.PerPage, offset)
	default:
		return nil, fmt.Errorf("unsupported dialect %s", e.dialect)
	}

	var rows []searchRow
	if err := e.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return toResults(rows), nil
}

func (e *DBEngine) searchLike(ctx context.Context, text string, q Query, offset int) ([]Result, error) {
	like := "%" + text + "%"
	sql := `SELECT id, doc_type, topic_id, title, SUBSTR(body, 1, 200) AS excerpt
		FROM search_documents WHERE (title LIKE ? OR body LIKE ?) AND visibility = 'public'`
	args := []any{like, like}
	sql, args = appendFilters(sql, args, q)
	sql += ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	args = append(args, q.PerPage, offset)

	var rows []searchRow
	// SUBSTRING works on SQLite/MySQL/PostgreSQL; PostgreSQL uses SUBSTRING too.
	if err := e.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return toResults(rows), nil
}

func appendFilters(sql string, args []any, q Query) (string, []any) {
	if q.Type != "" {
		sql += ` AND doc_type = ?`
		args = append(args, q.Type)
	} else {
		sql += ` AND doc_type IN ('topic','post')`
	}
	if q.CategoryID != "" {
		sql += ` AND category_id = ?`
		args = append(args, q.CategoryID)
	}
	if q.AuthorID != "" {
		sql += ` AND author_id = ?`
		args = append(args, q.AuthorID)
	}
	return sql, args
}

func toResults(rows []searchRow) []Result {
	out := make([]Result, 0, len(rows))
	for _, r := range rows {
		topicID := r.TopicID
		if r.Type == "topic" {
			topicID = r.ID
		}
		excerpt := strings.Join(strings.Fields(r.Excerpt), " ")
		out = append(out, Result{
			ID: r.ID, Type: r.Type, TopicID: topicID, Title: r.Title, Excerpt: excerpt,
		})
	}
	return out
}

func (e *DBEngine) Reindex(context.Context) error { return nil }

// Reset clears all indexed documents. Callers are expected to re-index afterwards.
func (e *DBEngine) Reset(ctx context.Context) error {
	if e.dialect == "sqlite" && e.fts {
		if err := e.db.WithContext(ctx).Exec(`DELETE FROM search_fts`).Error; err != nil {
			return err
		}
	}
	return e.db.WithContext(ctx).Where("1 = 1").Delete(&docRow{}).Error
}
