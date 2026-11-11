package markdown

import (
	"strings"
	"testing"
)

func TestRenderSanitizes(t *testing.T) {
	r := New()
	out := r.Render("**hi** <script>alert(1)</script>\n\n[click](javascript:alert(1))\n\n```go\nfmt.Println(1)\n```")
	if strings.Contains(out, "<script") {
		t.Fatalf("script survived: %s", out)
	}
	if strings.Contains(out, "javascript:") {
		t.Fatalf("javascript URL survived: %s", out)
	}
	if !strings.Contains(out, "<strong>hi</strong>") {
		t.Fatalf("expected strong: %s", out)
	}
	if !strings.Contains(out, "language-go") {
		t.Fatalf("expected code class: %s", out)
	}
	img := r.Render("![x](https://example.com/a.png)")
	if !strings.Contains(img, `loading="lazy"`) || !strings.Contains(img, `decoding="async"`) {
		t.Fatalf("image not lazy: %s", img)
	}
}

func TestRenderSignature(t *testing.T) {
	r := New()
	out := r.RenderSignature("**hi**\n\n![x](https://example.com/a.png)\n\n# 标题\n\n[link](https://example.com)")
	if !strings.Contains(out, "<strong>hi</strong>") {
		t.Fatalf("expected strong: %s", out)
	}
	if strings.Contains(out, "<img") {
		t.Fatalf("image survived: %s", out)
	}
	if strings.Contains(out, "<h1") {
		t.Fatalf("heading survived: %s", out)
	}
	if !strings.Contains(out, "标题") {
		t.Fatalf("heading text removed: %s", out)
	}
	if !strings.Contains(out, "nofollow") {
		t.Fatalf("link should be nofollow: %s", out)
	}
	if br := r.RenderSignature("line1" + string(rune(10)) + "line2"); !strings.Contains(br, "<br") {
		t.Fatalf("signature line break lost: %s", br)
	}
}

func TestExcerpt(t *testing.T) {
	got := Excerpt("# 标题\n\n这是**正文**，包含[链接](https://example.com)与 `code`。", 12)
	if strings.Contains(got, "#") || strings.Contains(got, "**") {
		t.Fatalf("markdown markers left: %q", got)
	}
	if !strings.Contains(got, "标题") {
		t.Fatalf("unexpected excerpt: %q", got)
	}
}

func TestSlug(t *testing.T) {
	if got := Slug("Hello World!"); got != "hello-world" {
		t.Fatalf("slug = %q", got)
	}
	if got := Slug("中文标题"); got != "topic" {
		t.Fatalf("non-ascii slug = %q", got)
	}
}
