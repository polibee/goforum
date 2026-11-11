package site

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/config"
	"goforum/internal/platform/events"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/markdown"
	"goforum/internal/platform/middleware"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/seo"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Module struct {
	db       *gorm.DB
	settings *settings.Store
	renderer *view.Renderer
	md       *markdown.Renderer
	auth     contracts.Authenticator
	sessions *session.Store
	cfg      *config.Config
	log      *slog.Logger
	version  string
	registry adminui.Registry
	layout   layoutSnapshot
	layoutMu sync.Mutex
}

func New(db *gorm.DB) *Module { return &Module{db: db, md: markdown.New()} }

func (m *Module) Configure(st *settings.Store, renderer *view.Renderer, auth contracts.Authenticator, sessions *session.Store, cfg *config.Config, log *slog.Logger, version string) {
	m.settings = st
	m.renderer = renderer
	m.auth = auth
	m.sessions = sessions
	m.cfg = cfg
	m.log = log
	m.version = version
}

func (m *Module) SetRegistry(r adminui.Registry) { m.registry = r }

func (m *Module) Name() string                         { return "site" }
func (m *Module) DependsOn() []string                  { return []string{"auth"} }
func (m *Module) Models() []any                        { return Models() }
func (m *Module) Permissions() []permission.Definition { return nil }
func (m *Module) Settings() []settings.Definition      { return nil }
func (m *Module) Subscribe(events.Bus) error           { return nil }

func (m *Module) Admin() adminui.Contribution {
	return adminui.Contribution{
		Nav: []adminui.NavItem{
			{Group: "site", Label: "页面管理", URL: "/admin/site/pages", Permission: "settings.site", Order: 10},
			{Group: "site", Label: "页眉导航", URL: "/admin/site/header", Permission: "settings.site", Order: 20},
			{Group: "site", Label: "页脚导航", URL: "/admin/site/footer", Permission: "settings.site", Order: 30},
			{Group: "site", Label: "侧边栏卡片", URL: "/admin/site/sidebar", Permission: "settings.site", Order: 40},
		},
	}
}

func (m *Module) EnsureDefaults(ctx context.Context) error {
	if err := m.ensureSidebarDefaults(ctx, false); err != nil {
		return err
	}
	var n int64
	if err := m.db.WithContext(ctx).Model(&Page{}).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		defaults := []Page{
			{Slug: "about", Title: "关于我们", Status: "published", BodyRaw: "欢迎来到 " + m.settings.GetString("site.general.name") + "。\n\n这里可以介绍社区定位、规则与联系方式。"},
			{Slug: "privacy", Title: "隐私政策", Status: "published", BodyRaw: "我们只收集提供服务所必需的信息，并按照适用的隐私法规处理。"},
			{Slug: "terms", Title: "服务条款", Status: "published", BodyRaw: "使用本站即表示你同意遵守社区规则与适用法律。"},
		}
		for i := range defaults {
			defaults[i].BodyHTML = m.md.Render(defaults[i].BodyRaw)
			if err := m.db.WithContext(ctx).Create(&defaults[i]).Error; err != nil {
				return err
			}
		}
	}
	return m.ensureLayoutDefaults(ctx)
}

func (m *Module) Routes(web, api *gin.RouterGroup) {
	if m.renderer == nil {
		return
	}
	md := middleware.Deps{
		Config: m.cfg, Sessions: m.sessions, Users: contracts.AsLoader(m.auth),
		Log: m.log, Renderer: m.renderer, Settings: m.settings, Version: m.version,
	}
	web.GET("/p/:id", m.legacyPageRedirect)
	web.GET("/:id", m.viewPage)
	web.GET("/admin/site", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.adminSiteHome)
	web.GET("/admin/site/pages", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.adminPages)
	web.GET("/admin/site/pages/new", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.adminPageNew)
	web.GET("/admin/site/pages/:id/edit", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.adminPageEdit)
	web.GET("/admin/site/header", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.adminHeader)
	web.GET("/admin/site/footer", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.adminFooter)
	web.GET("/admin/site/sidebar", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.adminSidebar)
	web.POST("/admin/site/pages", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.savePage)
	web.POST("/admin/site/nav", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.saveNav)
	web.POST("/admin/site/nav/action", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.navAction)
	web.POST("/admin/site/widget/action", middleware.RequireLogin(md), middleware.RequirePermission(md, "settings.site"), m.widgetAction)
}

type pageData struct {
	Page *Page
}

// legacyPageRedirect keeps old /p/:slug links working after the prefix was removed.
func (m *Module) legacyPageRedirect(c *gin.Context) {
	target := "/" + c.Param("id")
	if q := c.Request.URL.RawQuery; q != "" {
		target += "?" + q
	}
	c.Redirect(http.StatusMovedPermanently, target)
}

func (m *Module) viewPage(c *gin.Context) {
	slug := strings.TrimSpace(c.Param("id"))
	var page Page
	err := m.db.WithContext(c.Request.Context()).Where("slug = ? AND status = ?", slug, "published").First(&page).Error
	if err != nil {
		data := view.BaseData(c, m.settings.SiteInfo(), m.version)
		data.Title = "页面不存在 - " + data.Site.Name
		data.Robots = "noindex,follow"
		data.Data = view.ErrorData{Code: 404, Message: "页面不存在", RequestID: c.GetString("request_id")}
		m.renderer.Render(c, http.StatusNotFound, "error", data)
		return
	}
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	title := page.SEOTitle
	if title == "" {
		title = page.Title
	}
	data.SetPageTitle(title, m.settings.GetString("seo.title_template"))
	data.Description = seo.Truncate(page.SEODescription, 160)
	if data.Description == "" {
		data.Description = seo.Truncate(stripHTML(page.BodyHTML), 160)
	}
	data.OGType = "article"
	if data.Site.URL != "" {
		base := strings.TrimRight(data.Site.URL, "/")
		data.JSONLD = seo.Graph(
			seo.Breadcrumbs([]seo.Crumb{
				{Name: data.Site.Name, URL: base + "/"},
				{Name: page.Title, URL: base + "/" + page.Slug},
			}),
		)
	}
	data.Data = pageData{Page: &page}
	m.renderer.Render(c, http.StatusOK, "page_view", data)
}

func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

type adminPageData struct {
	Pages         []Page
	Editing       *Page
	NavHeader     []NavItem
	NavFooter     []NavItem
	Widgets       []SidebarWidget
	EditingNav    *NavItem
	EditingWidget *SidebarWidget
	Error         string
}

func (m *Module) adminPage(c *gin.Context) {
	var pages []Page
	// 旧版合并视图未接入分页，这里限制返回条数避免无上限查询。
	_ = m.db.WithContext(c.Request.Context()).Order("updated_at DESC").Limit(200).Find(&pages).Error
	var editing *Page
	if id := c.Query("edit"); id != "" {
		var p Page
		if err := m.db.WithContext(c.Request.Context()).First(&p, "id = ?", id).Error; err == nil {
			editing = &p
		}
	}
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.Title = "页面与导航 - " + data.Site.Name
	if p := data.User; p != nil && m.registry != nil {
		data.AdminNav = m.registry.NavFor(p)
		data.AdminGroups = adminui.BuildGroups(data.AdminNav)
	}
	var header, footer []NavItem
	_ = m.db.WithContext(c.Request.Context()).Where("location = ?", "header").Order("sort ASC, created_at ASC").Find(&header).Error
	_ = m.db.WithContext(c.Request.Context()).Where("location = ?", "footer").Order("sort ASC, created_at ASC").Find(&footer).Error
	var widgets []SidebarWidget
	_ = m.db.WithContext(c.Request.Context()).Order("sort ASC, created_at ASC").Find(&widgets).Error
	var editingNav *NavItem
	if id := c.Query("nav"); id != "" {
		var row NavItem
		if err := m.db.WithContext(c.Request.Context()).First(&row, "id = ?", id).Error; err == nil {
			editingNav = &row
		}
	}
	var editingWidget *SidebarWidget
	if id := c.Query("widget"); id != "" {
		var row SidebarWidget
		if err := m.db.WithContext(c.Request.Context()).First(&row, "id = ?", id).Error; err == nil {
			editingWidget = &row
		}
	}
	data.Data = adminPageData{
		Pages:         pages,
		Editing:       editing,
		NavHeader:     header,
		NavFooter:     footer,
		Widgets:       widgets,
		EditingNav:    editingNav,
		EditingWidget: editingWidget,
		Error:         c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "admin_site", data)
}

func (m *Module) savePage(c *gin.Context) {
	action := c.PostForm("action")
	id := c.PostForm("id")
	if action == "delete" {
		if err := m.db.WithContext(c.Request.Context()).Delete(&Page{}, "id = ?", id).Error; err != nil {
			m.siteError(c, "pages", err)
			return
		}
		m.siteRedirect(c, "pages")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(c.PostForm("slug")))
	status := c.PostForm("status")
	if status != "published" && status != "draft" {
		status = "draft"
	}
	raw := c.PostForm("body")
	if !slugPattern.MatchString(slug) {
		m.renderPageEditor(c, &Page{ID: id, Slug: slug, Title: strings.TrimSpace(c.PostForm("title")), BodyRaw: raw, Status: status, SEOTitle: c.PostForm("seo_title"), SEODescription: c.PostForm("seo_description")}, http.StatusUnprocessableEntity, "Slug 只能包含小写字母、数字和连字符")
		return
	}
	page := Page{
		Slug: slug, Title: strings.TrimSpace(c.PostForm("title")),
		BodyRaw: raw, BodyHTML: m.md.Render(raw), Status: status,
		SEOTitle:       strings.TrimSpace(c.PostForm("seo_title")),
		SEODescription: strings.TrimSpace(c.PostForm("seo_description")),
	}
	if page.Title == "" {
		page.ID = id
		m.renderPageEditor(c, &page, http.StatusUnprocessableEntity, "标题不能为空")
		return
	}
	var err error
	if id == "" {
		err = m.db.WithContext(c.Request.Context()).Create(&page).Error
	} else {
		err = m.db.WithContext(c.Request.Context()).Model(&Page{}).Where("id = ?", id).Updates(map[string]any{
			"slug": page.Slug, "title": page.Title, "body_raw": page.BodyRaw, "body_html": page.BodyHTML,
			"status": page.Status, "seo_title": page.SEOTitle, "seo_description": page.SEODescription,
		}).Error
	}
	if err != nil {
		page.ID = id
		m.renderPageEditor(c, &page, http.StatusUnprocessableEntity, httpx.ErrorOf(err).Message)
		return
	}
	m.siteRedirect(c, "pages")
}

func (m *Module) saveNav(c *gin.Context) {
	actor := authn.FromContext(c.Request.Context()).ID
	header, err := parseNav(c.PostForm("nav_header"))
	if err == nil {
		err = m.settings.Set(c.Request.Context(), "site.nav.header", header, actor)
	}
	if err == nil {
		var footer []settings.NavItem
		footer, err = parseNav(c.PostForm("nav_footer"))
		if err == nil {
			err = m.settings.Set(c.Request.Context(), "site.nav.footer", footer, actor)
		}
	}
	if err != nil {
		m.redirectError(c, err)
		return
	}
	c.Redirect(http.StatusFound, "/admin/site")
}

func (m *Module) redirectError(c *gin.Context, err error) {
	c.Redirect(http.StatusFound, "/admin/site?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
}

func parseNav(raw string) ([]settings.NavItem, error) {
	items := make([]settings.NavItem, 0)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			return nil, httpx.Invalid(map[string]string{"nav": "导航格式应为 标签|链接|权限(可选)"})
		}
		label := strings.TrimSpace(parts[0])
		link := strings.TrimSpace(parts[1])
		if label == "" || link == "" {
			return nil, httpx.Invalid(map[string]string{"nav": "导航标签与链接不能为空"})
		}
		item := settings.NavItem{Label: label, URL: link}
		if len(parts) >= 3 {
			item.Permission = strings.TrimSpace(parts[2])
		}
		if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
			item.External = true
		}
		items = append(items, item)
	}
	return items, nil
}

func formatNav(items []settings.NavItem) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString(item.Label)
		b.WriteString("|")
		b.WriteString(item.URL)
		if item.Permission != "" {
			b.WriteString("|")
			b.WriteString(item.Permission)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m *Module) navAction(c *gin.Context) {
	action := c.PostForm("action")
	id := c.PostForm("id")
	panel := "header"
	var existing NavItem
	if id != "" {
		if err := m.db.WithContext(c.Request.Context()).First(&existing, "id = ?", id).Error; err == nil {
			if existing.Location == "footer" {
				panel = "footer"
			}
		}
	}
	switch action {
	case "delete":
		if err := m.db.WithContext(c.Request.Context()).Delete(&NavItem{}, "id = ?", id).Error; err != nil {
			m.siteError(c, panel, err)
			return
		}
	case "toggle":
		if id == "" {
			m.siteError(c, panel, httpx.Invalid(map[string]string{"id": "缺少菜单 ID"}))
			return
		}
		if err := m.db.WithContext(c.Request.Context()).Model(&NavItem{}).Where("id = ?", id).
			Update("enabled", !existing.Enabled).Error; err != nil {
			m.siteError(c, panel, err)
			return
		}
	case "create", "update":
		location := c.PostForm("location")
		if location != "header" && location != "footer" {
			m.siteError(c, panel, httpx.Invalid(map[string]string{"location": "导航位置无效"}))
			return
		}
		panel = location
		label := strings.TrimSpace(c.PostForm("label"))
		link := strings.TrimSpace(c.PostForm("url"))
		if label == "" || link == "" {
			m.siteError(c, panel, httpx.Invalid(map[string]string{"nav": "名称与链接不能为空"}))
			return
		}
		sort, _ := strconv.Atoi(c.PostForm("sort"))
		row := NavItem{
			Location: location, Label: label, URL: link,
			Permission: strings.TrimSpace(c.PostForm("permission")),
			Section:    strings.TrimSpace(c.PostForm("section")),
			Sort:       sort,
			Enabled:    c.PostForm("enabled") != "",
			External:   strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://"),
		}
		var err error
		if action == "create" || id == "" {
			err = m.db.WithContext(c.Request.Context()).Create(&row).Error
		} else {
			err = m.db.WithContext(c.Request.Context()).Model(&NavItem{}).Where("id = ?", id).Updates(map[string]any{
				"location": row.Location, "label": row.Label, "url": row.URL,
				"permission": row.Permission, "section": row.Section, "sort": row.Sort, "enabled": row.Enabled,
				"external": row.External,
			}).Error
		}
		if err != nil {
			m.siteError(c, panel, err)
			return
		}
	default:
		m.siteError(c, panel, httpx.Invalid(map[string]string{"action": "未知操作"}))
		return
	}
	m.invalidateLayout()
	m.siteRedirect(c, panel)
}

func (m *Module) widgetAction(c *gin.Context) {
	action := c.PostForm("action")
	id := c.PostForm("id")
	switch action {
	case "delete":
		if err := m.db.WithContext(c.Request.Context()).Delete(&SidebarWidget{}, "id = ?", id).Error; err != nil {
			m.siteError(c, "sidebar", err)
			return
		}
	case "toggle":
		var row SidebarWidget
		if err := m.db.WithContext(c.Request.Context()).First(&row, "id = ?", id).Error; err != nil {
			m.siteError(c, "sidebar", err)
			return
		}
		if err := m.db.WithContext(c.Request.Context()).Model(&SidebarWidget{}).Where("id = ?", id).
			Update("enabled", !row.Enabled).Error; err != nil {
			m.siteError(c, "sidebar", err)
			return
		}
	case "restore":
		if err := m.ensureSidebarDefaults(c.Request.Context(), true); err != nil {
			m.siteError(c, "sidebar", err)
			return
		}
	case "preset":
		key := c.PostForm("preset")
		var preset widgetPreset
		found := false
		for _, p := range widgetPresetCatalog {
			if p.Key == key {
				preset = p
				found = true
				break
			}
		}
		if !found {
			m.siteError(c, "sidebar", httpx.Invalid(map[string]string{"preset": "未知卡片类型"}))
			return
		}
		if preset.System {
			var n int64
			_ = m.db.WithContext(c.Request.Context()).Model(&SidebarWidget{}).Where("key = ?", preset.Key).Count(&n).Error
			if n > 0 {
				m.siteError(c, "sidebar", httpx.Invalid(map[string]string{"preset": "该内置卡片已添加"}))
				return
			}
		}
		var maxSort int
		_ = m.db.WithContext(c.Request.Context()).Model(&SidebarWidget{}).
			Select("COALESCE(MAX(sort), 0)").Scan(&maxSort).Error
		row := SidebarWidget{
			Title: preset.Title, Type: preset.Type, Sort: maxSort + 10, Enabled: true,
		}
		if preset.System {
			row.Key = preset.Key
		} else {
			row.Title = strings.TrimPrefix(preset.Title, "自定义")
			if preset.Type == "text" {
				row.Title = "自定义文本"
			} else {
				row.Title = "自定义链接"
			}
		}
		if err := m.db.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
			m.siteError(c, "sidebar", err)
			return
		}
		m.invalidateLayout()
		if preset.System {
			m.siteRedirect(c, "sidebar")
			return
		}
		c.Redirect(http.StatusFound, "/admin/site/sidebar?widget="+row.ID)
		return
	case "create", "update":
		title := strings.TrimSpace(c.PostForm("title"))
		if title == "" {
			m.siteError(c, "sidebar", httpx.Invalid(map[string]string{"title": "卡片标题不能为空"}))
			return
		}
		sort, _ := strconv.Atoi(c.PostForm("sort"))
		enabled := c.PostForm("enabled") != ""
		var existing SidebarWidget
		hasExisting := false
		if id != "" {
			if err := m.db.WithContext(c.Request.Context()).First(&existing, "id = ?", id).Error; err == nil {
				hasExisting = true
			}
		}
		if hasExisting && existing.Key != "" {
			sysLocation := strings.TrimSpace(c.PostForm("location"))
			if sysLocation != "home" && sysLocation != "topic" && sysLocation != "all" {
				sysLocation = existing.Location
				if sysLocation == "" {
					sysLocation = "all"
				}
			}
			if err := m.db.WithContext(c.Request.Context()).Model(&SidebarWidget{}).Where("id = ?", id).
				Updates(map[string]any{"title": title, "sort": sort, "enabled": enabled, "location": sysLocation}).Error; err != nil {
				m.siteError(c, "sidebar", err)
				return
			}
			break
		}
		typ := c.PostForm("type")
		if typ != "links" && typ != "text" {
			typ = "links"
		}
		body := strings.TrimSpace(c.PostForm("body_text"))
		if typ == "links" {
			labels := c.PostFormArray("link_label")
			urls := c.PostFormArray("link_url")
			lines := make([]string, 0, len(labels))
			for i := range labels {
				label := strings.TrimSpace(labels[i])
				link := ""
				if i < len(urls) {
					link = strings.TrimSpace(urls[i])
				}
				if label != "" && link != "" {
					lines = append(lines, label+"|"+link)
				}
			}
			body = strings.Join(lines, string(rune(10)))
		}
		location := strings.TrimSpace(c.PostForm("location"))
		if location != "home" && location != "topic" {
			location = "all"
		}
		row := SidebarWidget{Title: title, Type: typ, Body: body, Location: location, Sort: sort, Enabled: enabled}
		var err error
		if action == "create" || id == "" {
			err = m.db.WithContext(c.Request.Context()).Create(&row).Error
		} else {
			err = m.db.WithContext(c.Request.Context()).Model(&SidebarWidget{}).Where("id = ?", id).Updates(map[string]any{
				"title": row.Title, "type": row.Type, "body": row.Body,
				"location": location, "sort": row.Sort, "enabled": row.Enabled,
			}).Error
		}
		if err != nil {
			m.siteError(c, "sidebar", err)
			return
		}
	default:
		m.siteError(c, "sidebar", httpx.Invalid(map[string]string{"action": "未知操作"}))
		return
	}
	m.invalidateLayout()
	m.siteRedirect(c, "sidebar")
}

type widgetLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type siteNavGroup struct {
	Name  string
	Items []NavItem
}

type sitePagesData struct {
	Pages      []Page
	Total      int64
	Page       int
	TotalPages int
	BasePath   string
	Error      string
}

type pageEditorData struct {
	Page  *Page
	Error string
}

type siteNavData struct {
	Location string
	Items    []NavItem
	Groups   []siteNavGroup
	Sections []string
	Editing  *NavItem
	Error    string
}

type widgetPreset struct {
	Key         string
	Title       string
	Description string
	Icon        string
	Type        string
	System      bool
	Exists      bool
}

var widgetPresetCatalog = []widgetPreset{
	{Key: "user_card", Title: "个人资料卡", Description: "展示当前用户头像、等级与快捷入口。", Icon: "👤", Type: "user_card", System: true},
	{Key: "compose", Title: "发帖引导", Description: "鼓励用户发布主题的入口卡片。", Icon: "✍️", Type: "compose", System: true},
	{Key: "categories", Title: "板块列表", Description: "列出所有公开板块与主题数量。", Icon: "🗂️", Type: "categories", System: true},
	{Key: "trending", Title: "本周热门", Description: "展示最近 7 天热门主题。", Icon: "🔥", Type: "trending", System: true},
	{Key: "stats", Title: "社区数据", Description: "展示用户、主题与回复总数。", Icon: "📊", Type: "stats", System: true},
	{Key: "tags", Title: "热门标签", Description: "展示常用标签入口。", Icon: "🏷️", Type: "tags", System: true},
	{Key: "sidebar_ad", Title: "广告位", Description: "在侧边栏展示当前投放的广告。", Icon: "📣", Type: "ad", System: true},
	{Key: "promote_card", Title: "自助推广", Description: "引导用户购买广告位。", Icon: "🚀", Type: "promote", System: true},
	{Key: "custom_links", Title: "自定义链接卡片", Description: "自己填写一组名称与链接。", Icon: "🔗", Type: "links"},
	{Key: "custom_text", Title: "自定义文本卡片", Description: "展示一段说明文字。", Icon: "📝", Type: "text"},
}

type siteSidebarData struct {
	Widgets      []SidebarWidget
	Presets      []widgetPreset
	Editing      *SidebarWidget
	EditingLinks []widgetLink
	Error        string
	// Location filters the list: "" (all), "home", "topic".
	Location      string
	HomeCount     int
	TopicCount    int
	AllCount      int
	SelectedCount int
}

func (m *Module) adminSiteHome(c *gin.Context) {
	c.Redirect(http.StatusFound, "/admin/site/pages")
}

func (m *Module) adminPages(c *gin.Context) {
	const perPage = 20
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	base := m.db.WithContext(c.Request.Context()).Model(&Page{})
	var total int64
	if err := base.Count(&total).Error; err != nil {
		m.log.Error("count site pages failed", "error", err)
	}
	var pages []Page
	if err := base.Order("updated_at DESC").Limit(perPage).Offset((page - 1) * perPage).Find(&pages).Error; err != nil {
		m.log.Error("list site pages failed", "error", err)
	}
	pagesCount := int((total + int64(perPage) - 1) / int64(perPage))
	if pagesCount < 1 {
		pagesCount = 1
	}
	data := m.sitePage(c, "页面管理")
	data.Data = sitePagesData{
		Pages: pages, Total: total, Page: page, TotalPages: pagesCount,
		BasePath: "/admin/site/pages", Error: c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "admin_site_pages", data)
}

func (m *Module) adminPageNew(c *gin.Context) {
	m.renderPageEditor(c, &Page{Status: "draft"}, http.StatusOK, "")
}

func (m *Module) adminPageEdit(c *gin.Context) {
	var page Page
	if err := m.db.WithContext(c.Request.Context()).First(&page, "id = ?", c.Param("id")).Error; err != nil {
		m.siteError(c, "pages", httpx.NotFound("page.not_found", "页面不存在"))
		return
	}
	m.renderPageEditor(c, &page, http.StatusOK, "")
}

func (m *Module) renderPageEditor(c *gin.Context, page *Page, status int, message string) {
	title := "新建页面"
	if page.ID != "" {
		title = "编辑页面"
	}
	data := m.sitePage(c, title)
	data.Data = pageEditorData{Page: page, Error: message}
	m.renderer.Render(c, status, "admin_site_page_edit", data)
}

func (m *Module) adminHeader(c *gin.Context) { m.adminNav(c, "header") }
func (m *Module) adminFooter(c *gin.Context) { m.adminNav(c, "footer") }

func (m *Module) adminNav(c *gin.Context, location string) {
	var items []NavItem
	_ = m.db.WithContext(c.Request.Context()).Where("location = ?", location).
		Order("sort ASC, created_at ASC").Find(&items).Error
	var editing *NavItem
	if id := c.Query("nav"); id != "" {
		var row NavItem
		if err := m.db.WithContext(c.Request.Context()).First(&row, "id = ?", id).Error; err == nil {
			editing = &row
		}
	}
	groups := make([]siteNavGroup, 0)
	sections := make([]string, 0)
	if location == "footer" {
		index := make(map[string]int)
		for _, item := range items {
			name := strings.TrimSpace(item.Section)
			if name == "" {
				name = "未分组"
			}
			i, ok := index[name]
			if !ok {
				i = len(groups)
				index[name] = i
				groups = append(groups, siteNavGroup{Name: name})
				if name != "未分组" {
					sections = append(sections, name)
				}
			}
			groups[i].Items = append(groups[i].Items, item)
		}
	}
	title := "页眉导航"
	if location == "footer" {
		title = "页脚导航"
	}
	data := m.sitePage(c, title)
	data.Data = siteNavData{
		Location: location, Items: items, Groups: groups, Sections: sections,
		Editing: editing, Error: c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "admin_site_nav", data)
}

func (m *Module) adminSidebar(c *gin.Context) {
	location := strings.TrimSpace(c.Query("location"))
	if location != "home" && location != "topic" {
		location = ""
	}

	var all []SidebarWidget
	_ = m.db.WithContext(c.Request.Context()).Order("sort ASC, created_at ASC").Find(&all).Error
	counts := map[string]int{}
	for _, w := range all {
		loc := w.Location
		if loc == "" {
			loc = "all"
		}
		counts[loc]++
	}
	var widgets []SidebarWidget
	for _, w := range all {
		if location == "" {
			widgets = append(widgets, w)
			continue
		}
		loc := w.Location
		if loc == "" {
			loc = "all"
		}
		// "all" cards appear on both pages, so show them in either filter.
		if loc == location || loc == "all" {
			widgets = append(widgets, w)
		}
	}
	var editing *SidebarWidget
	if id := c.Query("widget"); id != "" {
		var row SidebarWidget
		if err := m.db.WithContext(c.Request.Context()).First(&row, "id = ?", id).Error; err == nil {
			editing = &row
		}
	}
	links := []widgetLink{}
	if editing != nil && editing.Type == "links" {
		links = parseWidgetLinks(editing.Body)
	}
	keys := map[string]bool{}
	for _, w := range widgets {
		if w.Key != "" {
			keys[w.Key] = true
		}
	}
	presets := make([]widgetPreset, 0, len(widgetPresetCatalog))
	for _, p := range widgetPresetCatalog {
		p.Exists = p.System && keys[p.Key]
		presets = append(presets, p)
	}
	data := m.sitePage(c, "侧边栏卡片")
	data.Data = siteSidebarData{
		Widgets: widgets, Presets: presets, Editing: editing, EditingLinks: links,
		Error: c.Query("error"), Location: location,
		HomeCount: counts["home"], TopicCount: counts["topic"], AllCount: counts["all"],
		SelectedCount: len(widgets),
	}
	m.renderer.Render(c, http.StatusOK, "admin_site_sidebar", data)
}

func (m *Module) sitePage(c *gin.Context, title string) view.Data {
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.SetPageTitle(title, m.settings.GetString("seo.title_template"))
	if p := data.User; p != nil && m.registry != nil {
		data.AdminNav = m.registry.NavFor(p)
		data.AdminGroups = adminui.BuildGroups(data.AdminNav)
	}
	return data
}

func parseWidgetLinks(body string) []widgetLink {
	out := make([]widgetLink, 0)
	for _, line := range strings.Split(body, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			out = append(out, widgetLink{Label: strings.TrimSpace(parts[0]), URL: strings.TrimSpace(parts[1])})
		}
	}
	return out
}

func (m *Module) siteRedirect(c *gin.Context, panel string) {
	c.Redirect(http.StatusFound, "/admin/site/"+panel)
}

func (m *Module) siteError(c *gin.Context, panel string, err error) {
	c.Redirect(http.StatusFound, "/admin/site/"+panel+"?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
}
