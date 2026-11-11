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

type rssLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr,omitempty"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	Author      string `xml:"author,omitempty"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Language    string    `xml:"language"`
	LastBuild   string    `xml:"lastBuildDate"`
	Atom        rssLink   `xml:"atom:link"`
	Items       []rssItem `xml:"item"`
}

type rssDoc struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	AtomNS  string     `xml:"xmlns:atom,attr"`
	Channel rssChannel `xml:"channel"`
}

type feedTopicRow struct {
	ID        string
	Number    int64
	Title     string
	Slug      string
	Excerpt   string
	Author    string
	CreatedAt time.Time
}

func (m *Module) writeFeed(c *gin.Context, title, feedPath, description string, topics []feedTopicRow) {
	base := m.baseURL(c)
	loc := base + feedPath
	now := time.Now().UTC()
	doc := rssDoc{
		Version: "2.0",
		AtomNS:  "http://www.w3.org/2005/Atom",
		Channel: rssChannel{
			Title:       title,
			Link:        base + "/",
			Description: description,
			Language:    m.settings.GetString("site.general.locale"),
			LastBuild:   now.Format(time.RFC1123Z),
			Atom:        rssLink{Href: loc, Rel: "self", Type: "application/rss+xml"},
		},
	}
	for _, t := range topics {
		link := base + "/t/" + strconv.FormatInt(t.Number, 10)
		doc.Channel.Items = append(doc.Channel.Items, rssItem{
			Title:       t.Title,
			Link:        link,
			GUID:        link,
			Description: t.Excerpt,
			PubDate:     t.CreatedAt.UTC().Format(time.RFC1123Z),
			Author:      t.Author,
		})
	}
	c.Header("Content-Type", "application/rss+xml; charset=utf-8")
	c.Header("Cache-Control", "public, max-age=300")
	_, _ = c.Writer.WriteString(xml.Header)
	enc := xml.NewEncoder(c.Writer)
	enc.Indent("", "  ")
	_ = enc.Encode(doc)
}

func (m *Module) feedTopics(c *gin.Context, join, where string, args ...any) ([]feedTopicRow, error) {
	q := m.db.WithContext(c.Request.Context()).Table("topics AS t").
		Select("t.id, t.number, t.title, t.slug, t.excerpt, u.username AS author, t.created_at").
		Joins("JOIN categories c ON c.id = t.category_id").
		Joins("JOIN users u ON u.id = t.user_id").
		Where("t.deleted_at IS NULL AND t.status IN ? AND t.visibility = ? AND c.visibility = ?",
			[]string{"open", "closed", "archived"}, "public", "public")
	if join != "" {
		q = q.Joins(join)
	}
	if where != "" {
		q = q.Where(where, args...)
	}
	var rows []feedTopicRow
	err := q.Order("t.created_at DESC").Limit(30).Scan(&rows).Error
	return rows, err
}

func (m *Module) feedAll(c *gin.Context) {
	site := m.settings.SiteInfo()
	rows, err := m.feedTopics(c, "", "")
	if err != nil {
		c.String(http.StatusInternalServerError, "feed failed")
		return
	}
	desc := site.Description
	if desc == "" {
		desc = site.Tagline
	}
	m.writeFeed(c, site.Name, "/feed.xml", desc, rows)
}

func (m *Module) feedCategory(c *gin.Context) {
	slug := strings.TrimSpace(c.Param("slug"))
	var name string
	_ = m.db.WithContext(c.Request.Context()).Table("categories").
		Select("name").Where("slug = ? AND visibility = ?", slug, "public").Scan(&name).Error
	if name == "" {
		c.String(http.StatusNotFound, "feed not found")
		return
	}
	rows, err := m.feedTopics(c, "", "c.slug = ?", slug)
	if err != nil {
		c.String(http.StatusInternalServerError, "feed failed")
		return
	}
	m.writeFeed(c, name+" - "+m.settings.GetString("site.general.name"),
		"/c/"+url.PathEscape(slug)+"/feed.xml", name+" 板块的最新主题", rows)
}

func (m *Module) feedTag(c *gin.Context) {
	slug := strings.TrimSpace(c.Param("slug"))
	var name string
	_ = m.db.WithContext(c.Request.Context()).Table("tags").
		Select("name").Where("slug = ?", slug).Scan(&name).Error
	if name == "" {
		c.String(http.StatusNotFound, "feed not found")
		return
	}
	join := "JOIN topic_tags tt ON tt.topic_id = t.id JOIN tags tg ON tg.id = tt.tag_id"
	rows, err := m.feedTopics(c, join, "tg.slug = ?", slug)
	if err != nil {
		c.String(http.StatusInternalServerError, "feed failed")
		return
	}
	m.writeFeed(c, "#"+name+" - "+m.settings.GetString("site.general.name"),
		"/tags/"+url.PathEscape(slug)+"/feed.xml", "标签 #"+name+" 的最新主题", rows)
}

func (m *Module) feedUser(c *gin.Context) {
	username := strings.TrimSpace(c.Param("username"))
	var user struct {
		ID       string
		Username string
	}
	_ = m.db.WithContext(c.Request.Context()).Table("users").
		Select("id, username").Where("username_lower = ? AND status = ?", strings.ToLower(username), "active").
		Scan(&user).Error
	if user.ID == "" {
		c.String(http.StatusNotFound, "feed not found")
		return
	}
	rows, err := m.feedTopics(c, "", "t.user_id = ?", user.ID)
	if err != nil {
		c.String(http.StatusInternalServerError, "feed failed")
		return
	}
	m.writeFeed(c, user.Username+" - "+m.settings.GetString("site.general.name"),
		"/u/"+url.PathEscape(user.Username)+"/feed.xml", user.Username+" 的最新主题", rows)
}
