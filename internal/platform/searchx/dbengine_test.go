package searchx

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestIndexSearchAndVisibility(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "search.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	engine := NewDBEngine(db)
	ctx := context.Background()
	if err := engine.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}

	if err := engine.Index(ctx, Document{
		ID: "t1", Type: "topic", Title: "Hello world", Body: "a searchable body about golang",
		CategoryID: "c1", AuthorID: "u1", Visibility: "public",
	}); err != nil {
		t.Fatalf("index topic: %v", err)
	}
	if err := engine.Index(ctx, Document{
		ID: "p1", Type: "post", TopicID: "t1", Title: "Hello world", Body: "reply about golang modules",
		CategoryID: "c1", AuthorID: "u2", Visibility: "public",
	}); err != nil {
		t.Fatalf("index post: %v", err)
	}
	if err := engine.Index(ctx, Document{
		ID: "t2", Type: "topic", Title: "Secret hello", Body: "private body",
		CategoryID: "c2", AuthorID: "u3", Visibility: "private",
	}); err != nil {
		t.Fatalf("index private: %v", err)
	}

	results, err := engine.Search(ctx, Query{Text: "hello", Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	for _, r := range results {
		if r.ID == "t2" {
			t.Fatal("private document leaked")
		}
	}

	// Update (upsert) should not duplicate.
	if err := engine.Index(ctx, Document{
		ID: "t1", Type: "topic", Title: "Hello world updated", Body: "updated body",
		CategoryID: "c1", AuthorID: "u1", Visibility: "public",
	}); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	results, _ = engine.Search(ctx, Query{Text: "updated", Page: 1, PerPage: 10})
	if len(results) != 1 || results[0].ID != "t1" {
		t.Fatalf("updated search = %+v", results)
	}

	if err := engine.Delete(ctx, "topic", "t1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	results, _ = engine.Search(ctx, Query{Text: "updated", Page: 1, PerPage: 10})
	if len(results) != 0 {
		t.Fatalf("deleted doc still indexed: %+v", results)
	}
}
