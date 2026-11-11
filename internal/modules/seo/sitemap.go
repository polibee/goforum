package seo

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type sitemapIndexDoc struct {
	XMLName  xml.Name       `xml:"sitemapindex"`
	XMLNS    string         `xml:"xmlns,attr"`
	Sitemaps []sitemapEntry `xml:"sitemap"`
}

func writeXML(c *gin.Context, v any) {
	c.Header("Content-Type", "application/xml; charset=utf-8")
	c.Header("Cache-Control", "public, max-age=600")
	_, _ = c.Writer.WriteString(xml.Header)
	enc := xml.NewEncoder(c.Writer)
	enc.Indent("", "  ")
	_ = enc.Encode(v)
}

func (m *Module) noSitemap(c *gin.Context) {
	c.String(http.StatusNotFound, "sitemap disabled")
}

func (m *Module) sitemapIndex(c *gin.Context) {
	if !m.sitemapEnabled() {
		m.noSitemap(c)
		return
	}
	base := m.baseURL(c)
	now := time.Now().UTC().Format("2006-01-02")
	doc := sitemapIndexDoc{
		XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9",
		Sitemaps: []sitemapEntry{
			{Loc: base + "/sitemap-topics.xml", LastMod: now},
			{Loc: base + "/sitemap-categories.xml", LastMod: now},
			{Loc: base + "/sitemap-tags.xml", LastMod: now},
			{Loc: base + "/sitemap-users.xml", LastMod: now},
			{Loc: base + "/sitemap-pages.xml", LastMod: now},
		},
	}
	writeXML(c, doc)
}

type topicSitemapRow struct {
	ID        string
	Number    int64
	Slug      string
	UpdatedAt time.Time
}

func (m *Module) sitemapTopics(c *gin.Context) {
	if !m.sitemapEnabled() {
		m.noSitemap(c)
		return
	}
	base := m.baseURL(c)
	var rows []topicSitemapRow
	err := m.db.WithContext(c.Request.Context()).
		Table("topics AS t").
		Select("t.id, t.number, t.slug, t.updated_at").
		Joins("JOIN categories c ON c.id = t.category_id").
		Where("t.deleted_at IS NULL AND t.status IN ? AND t.visibility = ? AND c.visibility = ?",
			[]string{"open", "closed", "archived"}, "public", "public").
		Order("t.updated_at DESC").Limit(50000).Scan(&rows).Error
	if err != nil {
		c.String(http.StatusInternalServerError, "sitemap failed")
		return
	}
	doc := sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, row := range rows {
		doc.URLs = append(doc.URLs, sitemapURL{
			Loc:     base + "/t/" + strconv.FormatInt(row.Number, 10),
			LastMod: row.UpdatedAt.UTC().Format("2006-01-02"),
		})
	}
	writeXML(c, doc)
}

type categorySitemapRow struct {
	Slug      string
	UpdatedAt time.Time
}

func (m *Module) sitemapCategories(c *gin.Context) {
	if !m.sitemapEnabled() {
		m.noSitemap(c)
		return
	}
	base := m.baseURL(c)
	var rows []categorySitemapRow
	err := m.db.WithContext(c.Request.Context()).Table("categories").
		Select("slug, updated_at").Where("visibility = ?", "public").
		Order("position ASC").Limit(50000).Scan(&rows).Error
	if err != nil {
		c.String(http.StatusInternalServerError, "sitemap failed")
		return
	}
	doc := sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, row := range rows {
		doc.URLs = append(doc.URLs, sitemapURL{
			Loc:     base + "/c/" + url.PathEscape(row.Slug),
			LastMod: row.UpdatedAt.UTC().Format("2006-01-02"),
		})
	}
	writeXML(c, doc)
}

func (m *Module) sitemapTags(c *gin.Context) {
	if !m.sitemapEnabled() {
		m.noSitemap(c)
		return
	}
	base := m.baseURL(c)
	var rows []categorySitemapRow
	err := m.db.WithContext(c.Request.Context()).Table("tags").
		Select("slug, updated_at").Where("topic_count > 0").
		Order("topic_count DESC").Limit(50000).Scan(&rows).Error
	if err != nil {
		c.String(http.StatusInternalServerError, "sitemap failed")
		return
	}
	doc := sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, row := range rows {
		doc.URLs = append(doc.URLs, sitemapURL{
			Loc:     base + "/tags/" + url.PathEscape(row.Slug),
			LastMod: row.UpdatedAt.UTC().Format("2006-01-02"),
		})
	}
	writeXML(c, doc)
}

type userSitemapRow struct {
	Username  string
	UpdatedAt time.Time
}

func (m *Module) sitemapUsers(c *gin.Context) {
	if !m.sitemapEnabled() || !m.settings.GetBool("seo.index_users") {
		m.noSitemap(c)
		return
	}
	base := m.baseURL(c)
	var rows []userSitemapRow
	err := m.db.WithContext(c.Request.Context()).Table("users").
		Select("username, updated_at").Where("status = ?", "active").
		Order("updated_at DESC").Limit(50000).Scan(&rows).Error
	if err != nil {
		c.String(http.StatusInternalServerError, "sitemap failed")
		return
	}
	doc := sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, row := range rows {
		doc.URLs = append(doc.URLs, sitemapURL{
			Loc:     base + "/u/" + url.PathEscape(row.Username),
			LastMod: row.UpdatedAt.UTC().Format("2006-01-02"),
		})
	}
	writeXML(c, doc)
}

func (m *Module) sitemapPages(c *gin.Context) {
	if !m.sitemapEnabled() {
		m.noSitemap(c)
		return
	}
	base := m.baseURL(c)
	var rows []categorySitemapRow
	err := m.db.WithContext(c.Request.Context()).Table("pages").
		Select("slug, updated_at").Where("status = ?", "published").
		Order("updated_at DESC").Limit(50000).Scan(&rows).Error
	if err != nil {
		// The pages table may not exist in minimal deployments; an empty shard is valid.
		rows = nil
	}
	doc := sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, row := range rows {
		doc.URLs = append(doc.URLs, sitemapURL{
			Loc:     base + "/" + url.PathEscape(row.Slug),
			LastMod: row.UpdatedAt.UTC().Format("2006-01-02"),
		})
	}
	writeXML(c, doc)
}

func (m *Module) robotsText(c *gin.Context) string {
	base := m.baseURL(c)
	if custom := strings.TrimSpace(m.settings.GetString("seo.robots_custom")); custom != "" {
		if !strings.Contains(custom, "Sitemap:") && m.sitemapEnabled() {
			custom += "\n\nSitemap: " + base + "/sitemap.xml"
		}
		if !strings.HasSuffix(custom, "\n") {
			custom += "\n"
		}
		return custom
	}
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	b.WriteString("Allow: /\n")
	for _, path := range []string{"/admin", "/api", "/install", "/search", "/settings", "/notifications",
		"/messages", "/checkout", "/billing", "/login", "/register", "/forgot", "/reset"} {
		b.WriteString("Disallow: " + path + "\n")
	}
	if m.sitemapEnabled() {
		b.WriteString("\nSitemap: " + base + "/sitemap.xml\n")
	}
	if extra := strings.TrimSpace(m.settings.GetString("seo.robots_extra")); extra != "" {
		b.WriteString("\n")
		b.WriteString(extra)
		b.WriteString("\n")
	}
	return b.String()
}

func (m *Module) robots(c *gin.Context) {
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Cache-Control", "public, max-age=600")
	c.String(http.StatusOK, m.robotsText(c))
}
