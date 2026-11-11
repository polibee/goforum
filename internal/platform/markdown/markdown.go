package markdown

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// Renderer converts Markdown to sanitized HTML.
type Renderer struct {
	md        goldmark.Markdown
	sigMD     goldmark.Markdown
	policy    *bluemonday.Policy
	sigPolicy *bluemonday.Policy
}

func New() *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
	)
	sigMD := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(html.WithHardWraps()),
	)
	p := bluemonday.UGCPolicy()
	p.AllowImages()
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AllowAttrs("class").
		Matching(regexp.MustCompile(`^language-[a-zA-Z0-9_+-]+$`)).
		OnElements("code")
	sig := bluemonday.NewPolicy()
	sig.AllowElements("p", "br", "strong", "b", "em", "i", "del", "s", "code", "pre", "blockquote", "ul", "ol", "li", "a", "hr")
	sig.AllowAttrs("href", "title").OnElements("a")
	sig.AllowRelativeURLs(true)
	sig.AllowURLSchemes("http", "https", "mailto")
	sig.RequireNoFollowOnLinks(true)
	sig.RequireNoReferrerOnLinks(true)
	return &Renderer{md: md, sigMD: sigMD, policy: p, sigPolicy: sig}
}

// RenderSignature renders the small Markdown subset used by user signatures.
// Images, headings and tables are removed so signatures cannot take over a page.
func (r *Renderer) RenderSignature(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := r.sigMD.Convert([]byte(raw), &buf); err != nil {
		return ""
	}
	return string(r.sigPolicy.SanitizeBytes(buf.Bytes()))
}

var imgTagRe = regexp.MustCompile(`(?i)<img\s+`)

func lazyImages(html string) string {
	return imgTagRe.ReplaceAllString(html, `<img loading="lazy" decoding="async" `)
}

func (r *Renderer) Render(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := r.md.Convert([]byte(raw), &buf); err != nil {
		return ""
	}
	return lazyImages(string(r.policy.SanitizeBytes(buf.Bytes())))
}

var (
	fenceRe = regexp.MustCompile("(?s)```.*?```")
	imgRe   = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	linkRe  = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	markRe  = regexp.MustCompile(`[#>*_~` + "`" + `\-]+`)
)

// Excerpt returns a plain-text preview, suitable for lists and meta descriptions.
func Excerpt(raw string, limit int) string {
	s := fenceRe.ReplaceAllString(raw, " ")
	s = imgRe.ReplaceAllString(s, "$1")
	s = linkRe.ReplaceAllString(s, "$1")
	s = markRe.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	if limit <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// Slug converts a title to a URL-safe ASCII slug. Non-ASCII titles fall back to "topic";
// the canonical URL always includes the ULID, so the slug is decorative.
func Slug(title string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ' || r == '-' || r == '_' || r == '.' || r == '/':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "topic"
	}
	if len(s) > 80 {
		s = strings.Trim(s[:80], "-")
	}
	return s
}
