package searchx

import "context"

type Document struct {
	ID         string
	Type       string // topic | post | user | tag
	TopicID    string
	Title      string
	Body       string
	CategoryID string
	AuthorID   string
	Tags       []string
	Visibility string
}

type Query struct {
	Text       string
	Type       string
	CategoryID string
	AuthorID   string
	Page       int
	PerPage    int
	Sort       string
}

type Result struct {
	ID      string
	Type    string
	TopicID string
	Title   string
	Excerpt string
	Score   float64
}

type Engine interface {
	Index(ctx context.Context, doc Document) error
	Delete(ctx context.Context, docType, id string) error
	Search(ctx context.Context, query Query) ([]Result, error)
	Reindex(ctx context.Context) error
}

// Noop is the M0 default. M2 wires dialect-backed FTS and external engines.
type Noop struct{}

func (Noop) Index(context.Context, Document) error           { return nil }
func (Noop) Delete(context.Context, string, string) error    { return nil }
func (Noop) Search(context.Context, Query) ([]Result, error) { return nil, nil }
func (Noop) Reindex(context.Context) error                   { return nil }
