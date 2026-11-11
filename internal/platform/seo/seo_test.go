package seo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goforum/internal/platform/settings"
)

func TestTruncate(t *testing.T) {
	if got := Truncate("  hello world  ", 5); got != "hello…" {
		t.Fatalf("truncate = %q", got)
	}
	if got := Truncate("short", 10); got != "short" {
		t.Fatalf("short = %q", got)
	}
}

func TestWebSiteJSONLDIsValid(t *testing.T) {
	raw := WebSite(settings.Site{Name: "Test", Description: "desc"}, "https://example.com/")
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if doc["@type"] != "WebSite" || doc["url"] != "https://example.com/" {
		t.Fatalf("doc = %+v", doc)
	}
	action, ok := doc["potentialAction"].(map[string]any)
	if !ok || action["@type"] != "SearchAction" {
		t.Fatalf("search action missing: %+v", doc)
	}
}

func TestGraphAndPosting(t *testing.T) {
	now := time.Now()
	posting := DiscussionForumPosting("https://example.com/t/1", "Title", "Body", "alice",
		"https://example.com/u/alice", now, now, 3)
	crumbs := Breadcrumbs([]Crumb{{Name: "Home", URL: "https://example.com/"}})
	raw := Graph(posting, crumbs)
	var doc struct {
		Graph []map[string]any `json:"@graph"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("invalid graph: %v", err)
	}
	if len(doc.Graph) != 2 {
		t.Fatalf("graph nodes = %d", len(doc.Graph))
	}
	if doc.Graph[0]["@type"] != "DiscussionForumPosting" || doc.Graph[1]["@type"] != "BreadcrumbList" {
		t.Fatalf("graph types = %+v", doc.Graph)
	}
}

func TestJSONEscapesScriptTags(t *testing.T) {
	raw := JSON(map[string]any{"text": "</script><script>alert(1)</script>"})
	if strings.Contains(string(raw), "</script>") {
		t.Fatalf("script tag not escaped: %s", raw)
	}
}
