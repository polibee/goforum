package site

import "testing"

func TestParseAndFormatNav(t *testing.T) {
	raw := "首页|/\n搜索|/search|topic.create\n外部|https://example.com\n"
	items, err := parseNav(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
	if items[1].Permission != "topic.create" {
		t.Fatalf("permission = %q", items[1].Permission)
	}
	if !items[2].External {
		t.Fatal("external not detected")
	}
	formatted := formatNav(items)
	again, err := parseNav(formatted)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if len(again) != len(items) || again[0].Label != items[0].Label {
		t.Fatalf("roundtrip mismatch: %+v", again)
	}
}

func TestParseNavRejectsMalformed(t *testing.T) {
	if _, err := parseNav("missing-separator"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := parseNav("|/empty-label"); err == nil {
		t.Fatal("expected error for empty label")
	}
}

func TestStripHTML(t *testing.T) {
	got := stripHTML("<p>Hello <b>world</b></p>\n<p>next</p>")
	if got != "Hello world next" {
		t.Fatalf("strip = %q", got)
	}
}

func TestParseWidgetLinks(t *testing.T) {
	links := parseWidgetLinks("新手指南|/about\n\n赞助|https://example.com\nbad line\n")
	if len(links) != 2 {
		t.Fatalf("links = %+v", links)
	}
	if links[0].Label != "新手指南" || links[0].URL != "/about" || links[1].URL != "https://example.com" {
		t.Fatalf("links = %+v", links)
	}
}
