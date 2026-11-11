package view

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/adminui"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/i18n"
	"goforum/internal/platform/settings"
)

type PageSpec struct {
	Layout string
	File   string
}

var Pages = map[string]PageSpec{
	"home":                 {Layout: "base", File: "pages/home.html"},
	"error":                {Layout: "base", File: "pages/error.html"},
	"install":              {Layout: "auth", File: "pages/install.html"},
	"login":                {Layout: "auth", File: "pages/login.html"},
	"login_2fa":            {Layout: "auth", File: "pages/login_2fa.html"},
	"register":             {Layout: "auth", File: "pages/register.html"},
	"notice":               {Layout: "auth", File: "pages/notice.html"},
	"forgot":               {Layout: "auth", File: "pages/forgot.html"},
	"reset":                {Layout: "auth", File: "pages/reset.html"},
	"profile":              {Layout: "base", File: "pages/profile.html"},
	"promote":              {Layout: "base", File: "pages/promote.html"},
	"security":             {Layout: "base", File: "pages/security.html"},
	"security_2fa_setup":   {Layout: "base", File: "pages/security_2fa_setup.html"},
	"feed":                 {Layout: "base", File: "pages/feed.html"},
	"topic":                {Layout: "base", File: "pages/topic.html"},
	"composer":             {Layout: "base", File: "pages/composer.html"},
	"edit_post":            {Layout: "base", File: "pages/edit_post.html"},
	"edit_topic":           {Layout: "base", File: "pages/edit_topic.html"},
	"user":                 {Layout: "base", File: "pages/user.html"},
	"admin_topics":         {Layout: "admin", File: "pages/admin/topics.html"},
	"admin_topic_edit":     {Layout: "admin", File: "pages/admin/topic_edit.html"},
	"admin_posts":          {Layout: "admin", File: "pages/admin/posts.html"},
	"admin_categories":     {Layout: "admin", File: "pages/admin/categories.html"},
	"admin_tags":           {Layout: "admin", File: "pages/admin/tags.html"},
	"notifications":        {Layout: "base", File: "pages/notifications.html"},
	"notification_prefs":   {Layout: "base", File: "pages/notification_prefs.html"},
	"messages":             {Layout: "base", File: "pages/messages.html"},
	"conversation":         {Layout: "base", File: "pages/conversation.html"},
	"bookmarks":            {Layout: "base", File: "pages/bookmarks.html"},
	"following":            {Layout: "base", File: "pages/following.html"},
	"admin_dashboard":      {Layout: "admin", File: "pages/admin/dashboard.html"},
	"admin_settings":       {Layout: "admin", File: "pages/admin/settings.html"},
	"admin_modules":        {Layout: "admin", File: "pages/admin/modules.html"},
	"admin_system":         {Layout: "admin", File: "pages/admin/system.html"},
	"admin_login_methods":  {Layout: "admin", File: "pages/admin/login_methods.html"},
	"admin_users":          {Layout: "admin", File: "pages/admin/users.html"},
	"admin_user_detail":    {Layout: "admin", File: "pages/admin/user_detail.html"},
	"admin_groups":         {Layout: "admin", File: "pages/admin/groups.html"},
	"admin_audit":          {Layout: "admin", File: "pages/admin/audit.html"},
	"report":               {Layout: "base", File: "pages/report.html"},
	"appeal":               {Layout: "base", File: "pages/appeal.html"},
	"admin_review":         {Layout: "admin", File: "pages/admin/review.html"},
	"admin_appeals":        {Layout: "admin", File: "pages/admin/appeals.html"},
	"admin_banned_words":   {Layout: "admin", File: "pages/admin/banned_words.html"},
	"points":               {Layout: "base", File: "pages/points.html"},
	"invites":              {Layout: "base", File: "pages/invites.html"},
	"lotteries":            {Layout: "base", File: "pages/lotteries.html"},
	"lottery_detail":       {Layout: "base", File: "pages/lottery_detail.html"},
	"bounties":             {Layout: "base", File: "pages/bounties.html"},
	"admin_gamification":   {Layout: "admin", File: "pages/admin/gamification.html"},
	"pricing":              {Layout: "base", File: "pages/pricing.html"},
	"checkout":             {Layout: "base", File: "pages/checkout.html"},
	"orders":               {Layout: "base", File: "pages/orders.html"},
	"subscription":         {Layout: "base", File: "pages/subscription.html"},
	"admin_billing":        {Layout: "admin", File: "pages/admin/billing.html"},
	"admin_seo":            {Layout: "admin", File: "pages/admin/seo.html"},
	"page_view":            {Layout: "base", File: "pages/page_view.html"},
	"admin_site":           {Layout: "admin", File: "pages/admin/site.html"},
	"admin_site_pages":     {Layout: "admin", File: "pages/admin/site_pages.html"},
	"admin_site_page_edit": {Layout: "admin", File: "pages/admin/site_page_edit.html"},
	"admin_site_nav":       {Layout: "admin", File: "pages/admin/site_nav.html"},
	"admin_site_sidebar":   {Layout: "admin", File: "pages/admin/site_sidebar.html"},
	"admin_ads":            {Layout: "admin", File: "pages/admin/ads.html"},
	"admin_ads_settings":   {Layout: "admin", File: "pages/admin/ads_settings.html"},
	"admin_ads_slots":      {Layout: "admin", File: "pages/admin/ads_slots.html"},
}

type Flash struct {
	Kind    string
	Message string
}

type ErrorData struct {
	Code      int
	Message   string
	RequestID string
}

func BaseData(c *gin.Context, site settings.Site, version string) Data {
	principal := authn.FromContext(c.Request.Context())
	csrf, _ := c.Get("csrf")
	nonce, _ := c.Get("nonce")
	path := c.Request.URL.Path
	unread, _ := c.Get("unread")
	var n int64
	if v, ok := unread.(int64); ok {
		n = v
	}
	msgUnread, _ := c.Get("msg_unread")
	var mn int64
	if v, ok := msgUnread.(int64); ok {
		mn = v
	}
	lang := i18n.FromContext(c.Request.Context())
	canonical := ""
	if site.URL != "" {
		canonical = strings.TrimRight(site.URL, "/") + path
		values := url.Values{}
		if page, _ := strconv.Atoi(c.Query("page")); page > 1 {
			values.Set("page", strconv.Itoa(page))
		}
		if lang != i18n.DefaultLocale {
			values.Set("lang", lang)
		}
		if len(values) > 0 {
			canonical += "?" + values.Encode()
		}
	}
	description := site.Description
	ogImage := site.OGImage
	if ogImage == "" {
		ogImage = site.LogoLight
	}
	preset := c.GetString("theme_preset")
	if preset == "" {
		preset = site.ThemePreset
	}
	site = site.ApplyThemePreset(preset)
	dark := site.ColorMode == "dark" || preset == "douyin"
	if preset == "wechat" || preset == "minimal" {
		dark = false
	}
	var navHeader, navFooter []settings.NavItem
	var sidebar []SidebarWidget
	if layoutProvider != nil {
		navHeader, navFooter, sidebar = layoutProvider()
	}
	if navHeader == nil {
		navHeader = site.NavHeader
	}
	if navFooter == nil {
		navFooter = site.NavFooter
	}
	var footerGroups []FooterGroup
	if len(navFooter) > 0 {
		index := map[string]int{}
		for _, item := range navFooter {
			key := item.Section
			idx, ok := index[key]
			if !ok {
				index[key] = len(footerGroups)
				footerGroups = append(footerGroups, FooterGroup{Label: key})
				idx = len(footerGroups) - 1
			}
			footerGroups[idx].Items = append(footerGroups[idx].Items, item)
		}
	}
	return Data{
		Lang:         lang,
		ThemePreset:  preset,
		Dark:         dark,
		AssetVersion: globalAsset,
		NavHeader:    navHeader,
		NavFooter:    navFooter,
		FooterGroups: footerGroups,
		Sidebar:      sidebar,
		AdSlots:      loadAds(c.Request.Context()),
		Site:         site,
		Description:  description,
		Robots:       "index,follow",
		Canonical:    canonical,
		OGType:       "website",
		OGImage:      ogImage,
		Twitter:      site.Twitter,
		User:         principal,
		CSRF:         asString(csrf),
		Nonce:        asString(nonce),
		Path:         path,
		Unread:       n,
		MsgUnread:    mn,
		Version:      version,
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

type Data struct {
	Lang         string
	ThemePreset  string
	Dark         bool
	AssetVersion string
	Title        string
	PageTitle    string
	Description  string
	Robots       string
	Canonical    string
	OGType       string
	OGImage      string
	Twitter      string
	JSONLD       template.JS
	PrevURL      string
	NextURL      string
	PublishedAt  *time.Time
	ModifiedAt   *time.Time
	Site         settings.Site
	NavHeader    []settings.NavItem
	NavFooter    []settings.NavItem
	FooterGroups []FooterGroup
	Sidebar      []SidebarWidget
	AdSlots      AdSlots
	User         *authn.Principal
	CSRF         string
	Nonce        string
	Path         string
	Flash        []Flash
	Unread       int64
	MsgUnread    int64
	AdminNav     []adminui.NavItem
	AdminGroups  []adminui.NavGroup
	Version      string
	Data         any
}

// AdView is a resolved advertisement for a frontend placement.
type AdView struct {
	ID       string
	Format   string
	Title    string
	Text     string
	URL      string
	ImageURL string
	HTML     template.HTML
	Height   int
}

type AdSlots map[string][]AdView

var adProvider func(context.Context) AdSlots

func SetAdProvider(fn func(context.Context) AdSlots) { adProvider = fn }

type SidebarLink struct {
	Label string
	URL   string
}

type FooterGroup struct {
	Label string
	Items []settings.NavItem
}

type SidebarWidget struct {
	Title string
	Type  string
	Text  string
	Links []SidebarLink
	// Location is "home", "topic" or "all"; the base layout only renders
	// cards matching the current page kind.
	Location string
}

var layoutProvider func() ([]settings.NavItem, []settings.NavItem, []SidebarWidget)

// SetLayoutProvider installs the DB-backed navigation and sidebar source.
func SetLayoutProvider(fn func() ([]settings.NavItem, []settings.NavItem, []SidebarWidget)) {
	layoutProvider = fn
}

var (
	globalTranslate func(locale, text string) string
	globalAsset     string
)

// SetGlobalTranslator installs the translator used for page titles.
func SetGlobalTranslator(fn func(locale, text string) string) { globalTranslate = fn }

// SetPageTitle applies the site's SEO title template ({title} / {site}).
func (d *Data) SetPageTitle(title, tmpl string) {
	if globalTranslate != nil && title != "" {
		title = globalTranslate(d.Lang, title)
	}
	if title == "" {
		d.PageTitle = d.Site.Name
		d.Title = d.Site.Name
		return
	}
	d.PageTitle = title
	if tmpl == "" {
		tmpl = "{title} - {site}"
	}
	t := strings.ReplaceAll(tmpl, "{title}", title)
	t = strings.ReplaceAll(t, "{site}", d.Site.Name)
	if strings.TrimSpace(t) == "" {
		t = title
	}
	d.Title = t
}

type Renderer struct {
	fsys  fs.FS
	dev   bool
	log   *slog.Logger
	funcs template.FuncMap
	cache map[string]*template.Template
	// cacheMu guards the template cache: Render reads it on every request while
	// dev mode (and a first miss) writes to it, so concurrent access without a
	// lock crashes the process with "concurrent map read and map write".
	cacheMu      sync.RWMutex
	translate    func(locale, text string) string
	assetVersion string
}

// AssetVersion hashes a filesystem's static directory for cache busting.
func AssetVersion(fsys fs.FS) string {
	h := sha256.New()
	_ = fs.WalkDir(fsys, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		_, _ = io.WriteString(h, path)
		f, err := fsys.Open(path)
		if err != nil {
			return nil
		}
		_, _ = io.Copy(h, f)
		_ = f.Close()
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// SetAssetVersion sets the cache-busting query appended to asset URLs.
func (r *Renderer) SetAssetVersion(v string) { r.assetVersion = v; globalAsset = v }

// SetTranslator installs the translation lookup used by the t template func.
func (r *Renderer) SetTranslator(fn func(locale, text string) string) {
	r.translate = fn
}

func New(fsys fs.FS, dev bool, log *slog.Logger) (*Renderer, error) {
	r := &Renderer{fsys: fsys, dev: dev, log: log, cache: make(map[string]*template.Template)}

	r.funcs = template.FuncMap{
		"t": func(text, locale string) string {
			if r.translate == nil {
				return text
			}
			return r.translate(locale, text)
		},
		"tTitle": func(text, locale string) string {
			if r.translate == nil {
				return text
			}
			if got := r.translate(locale, text); got != text {
				return got
			}
			if i := strings.Index(text, " - "); i > 0 {
				return r.translate(locale, text[:i]) + text[i:]
			}
			return text
		},
		"langURL": func(base, locale string) string {
			u, err := url.Parse(base)
			if err != nil || base == "" {
				return base
			}
			q := u.Query()
			q.Set("lang", locale)
			u.RawQuery = q.Encode()
			return u.String()
		},
		"asset": func(name string) string {
			u := "/static/dist/" + strings.TrimLeft(name, "/")
			if r.assetVersion != "" {
				u += "?v=" + r.assetVersion
			}
			return u
		},
		"dict": func(values ...any) (map[string]any, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("dict requires an even number of arguments")
			}
			m := make(map[string]any, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				k, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				m[k] = values[i+1]
			}
			return m, nil
		},
		"hasPerm": func(p *authn.Principal, key string) bool { return p.Can(key) },
		"toJSON": func(v any) string {
			b, err := json.Marshal(v)
			if err != nil {
				return ""
			}
			return string(b)
		},
		"year":     func() int { return nowYear() },
		"add":      func(a, b int) int { return a + b },
		"sub":      func(a, b int) int { return a - b },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"replyLabel": func(locale string, n any) string {
			count := int64(0)
			switch v := n.(type) {
			case int:
				count = int64(v)
			case int64:
				count = v
			}
			if locale == "en-US" {
				if count == 1 {
					return "reply"
				}
				return "replies"
			}
			return "条回复"
		},
		"initial": func(s string) string {
			for _, r := range s {
				return string(r)
			}
			return ""
		},
		"bioExcerpt": bioExcerpt,
		"hasPrefix":  func(s, prefix string) bool { return strings.HasPrefix(s, prefix) },
		"trimPrefix": func(s, prefix string) string { return strings.TrimPrefix(s, prefix) },
		"highlight": func(text, query string) template.HTML {
			escaped := html.EscapeString(text)
			query = strings.TrimSpace(query)
			if query == "" {
				return template.HTML(escaped)
			}
			re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(html.EscapeString(query)))
			if err != nil {
				return template.HTML(escaped)
			}
			return template.HTML(re.ReplaceAllString(escaped, "<mark>$0</mark>"))
		},
		"money": func(minor int64) string {
			return fmt.Sprintf("%.2f", float64(minor)/100)
		},
		"add64":      func(a, b int64) int64 { return a + b },
		"pageWindow": pageWindow,
		"list":       func(values ...string) []string { return values },
		"usd":        func(minor int64) string { return fmt.Sprintf("$%d.%02d", minor/100, minor%100) },
		"usdInput":   func(minor int64) string { return fmt.Sprintf("%d.%02d", minor/100, minor%100) },
		"bytes": func(n any) string {
			switch v := n.(type) {
			case uint64:
				return humanBytes(v)
			case int64:
				if v > 0 {
					return humanBytes(uint64(v))
				}
			case int:
				if v > 0 {
					return humanBytes(uint64(v))
				}
			}
			return "0 B"
		},
		"dur": func(seconds int64) string {
			if seconds <= 0 {
				return "—"
			}
			return humanDuration(time.Duration(seconds) * time.Second)
		},
		"seq": func(start, end int) []int {
			if end < start {
				return nil
			}
			out := make([]int, 0, end-start+1)
			for i := start; i <= end; i++ {
				out = append(out, i)
			}
			return out
		},
		"pageURL": func(base string, page int) string {
			sep := "?"
			if strings.Contains(base, "?") {
				sep = "&"
			}
			return fmt.Sprintf("%s%spage=%d", base, sep, page)
		},
		"contains": func(list []string, value string) bool {
			for _, v := range list {
				if v == value {
					return true
				}
			}
			return false
		},
	}
	for name := range Pages {
		if _, err := r.parse(name); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// cached returns a parsed template, compiling it on first use.
func (r *Renderer) cached(name string) (*template.Template, error) {
	if r.dev {
		return r.parse(name)
	}
	r.cacheMu.RLock()
	tpl, ok := r.cache[name]
	r.cacheMu.RUnlock()
	if ok {
		return tpl, nil
	}
	return r.parse(name)
}

func (r *Renderer) parse(name string) (*template.Template, error) {
	// Serialise compilation so two requests cannot parse the same page at once.
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if !r.dev {
		if tpl, ok := r.cache[name]; ok {
			return tpl, nil
		}
	}
	spec, ok := Pages[name]
	if !ok {
		return nil, fmt.Errorf("unknown page %q", name)
	}
	files := []string{path.Join("layouts", spec.Layout+".html"), spec.File}
	components, err := fs.Glob(r.fsys, "components/*.html")
	if err != nil {
		return nil, err
	}
	files = append(files, components...)
	tpl, err := template.New(name).Funcs(r.funcs).ParseFS(r.fsys, files...)
	if err != nil {
		return nil, fmt.Errorf("parse page %s: %w", name, err)
	}
	r.cache[name] = tpl
	return tpl, nil
}

func (r *Renderer) Render(c *gin.Context, status int, name string, data Data) {
	tpl, err := r.cached(name)
	if err != nil {
		r.log.Error("template parse failed", "page", name, "error", err)
		c.String(http.StatusInternalServerError, "template error")
		return
	}
	spec := Pages[name]
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)
	if err := tpl.ExecuteTemplate(c.Writer, spec.Layout, data); err != nil {
		r.log.Error("template render failed", "page", name, "error", err)
	}
}

func nowYear() int {
	return timeNow().Year()
}

var (
	mdLinkPattern   = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	mdLinePrefix    = regexp.MustCompile(`(?m)^\s{0,3}(?:#{1,6}\s+|>\s?|[-*+]\s+|\d+\.\s+)`)
	mdInlineMarkers = regexp.MustCompile("[`*_~]+")
)

func bioExcerpt(s string) string {
	s = mdLinkPattern.ReplaceAllString(s, "$1")
	s = mdLinePrefix.ReplaceAllString(s, "")
	s = mdInlineMarkers.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 100 {
		return string(r[:100]) + "…"
	}
	return s
}

func pageWindow(current, total int) []int {
	if total <= 1 {
		return nil
	}
	if current < 1 {
		current = 1
	}
	if current > total {
		current = total
	}
	start, finish := current-2, current+2
	if start < 1 {
		start = 1
	}
	if finish > total {
		finish = total
	}
	pages := make([]int, 0, 9)
	if start > 1 {
		pages = append(pages, 1, 0)
	}
	for i := start; i <= finish; i++ {
		pages = append(pages, i)
	}
	if finish < total {
		pages = append(pages, 0, total)
	}
	return pages
}

func loadAds(ctx context.Context) AdSlots {
	if adProvider == nil {
		return nil
	}
	return adProvider(ctx)
}

// humanDuration renders seconds with the largest two units for templates.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	secs := int64(d.Seconds())
	parts := make([]string, 0, 2)
	for _, unit := range []struct {
		size int64
		name string
	}{{86400, "天"}, {3600, "小时"}, {60, "分"}, {1, "秒"}} {
		if secs >= unit.size && len(parts) < 2 {
			parts = append(parts, fmt.Sprintf("%d%s", secs/unit.size, unit.name))
			secs %= unit.size
		}
	}
	if len(parts) == 0 {
		return "0 秒"
	}
	return strings.Join(parts, " ")
}

// humanBytes renders a byte count with binary units for templates.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	text := strings.TrimSuffix(fmt.Sprintf("%.1f", value), ".0")
	return text + " " + string("KMGTPE"[exp]) + "iB"
}
