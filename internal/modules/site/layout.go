package site

import (
	"context"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
	"strings"
)

type layoutSnapshot struct {
	valid   bool
	header  []settings.NavItem
	footer  []settings.NavItem
	widgets []view.SidebarWidget
}

func (m *Module) invalidateLayout() {
	m.layoutMu.Lock()
	m.layout.valid = false
	m.layoutMu.Unlock()
}

// Layout returns the cached navigation and sidebar used by every page.
func (m *Module) Layout() ([]settings.NavItem, []settings.NavItem, []view.SidebarWidget) {
	m.layoutMu.Lock()
	if m.layout.valid {
		header := append([]settings.NavItem(nil), m.layout.header...)
		footer := append([]settings.NavItem(nil), m.layout.footer...)
		widgets := append([]view.SidebarWidget(nil), m.layout.widgets...)
		m.layoutMu.Unlock()
		return header, footer, widgets
	}
	m.layoutMu.Unlock()

	header := m.navItems("header")
	footer := m.navItems("footer")
	widgets := m.widgets()

	m.layoutMu.Lock()
	m.layout = layoutSnapshot{valid: true, header: header, footer: footer, widgets: widgets}
	m.layoutMu.Unlock()
	return header, footer, widgets
}

func (m *Module) navItems(location string) []settings.NavItem {
	var rows []NavItem
	_ = m.db.Where("location = ? AND enabled = ?", location, true).
		Order("sort ASC, created_at ASC").Find(&rows).Error
	var out []settings.NavItem
	for _, row := range rows {
		out = append(out, settings.NavItem{
			Label: row.Label, URL: row.URL, Permission: row.Permission, Section: row.Section, External: row.External,
		})
	}
	return out
}

func (m *Module) widgets() []view.SidebarWidget {
	var rows []SidebarWidget
	_ = m.db.Where("enabled = ?", true).Order("sort ASC, created_at ASC").Find(&rows).Error
	out := make([]view.SidebarWidget, 0, len(rows))
	for _, row := range rows {
		location := row.Location
		if location == "" {
			location = "all"
		}
		w := view.SidebarWidget{Title: row.Title, Type: row.Type, Text: row.Body, Location: location}
		if row.Type == "links" {
			for _, line := range strings.Split(row.Body, "\n") {
				parts := strings.SplitN(strings.TrimSpace(line), "|", 2)
				if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
					w.Links = append(w.Links, view.SidebarLink{
						Label: strings.TrimSpace(parts[0]), URL: strings.TrimSpace(parts[1]),
					})
				}
			}
		}
		out = append(out, w)
	}
	return out
}

func (m *Module) ensureLayoutDefaults(ctx context.Context) error {
	var n int64
	if err := m.db.WithContext(ctx).Model(&NavItem{}).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 && m.settings != nil {
		migrate := func(location, key string) error {
			var items []settings.NavItem
			if !m.settings.GetJSON(key, &items) || len(items) == 0 {
				return nil
			}
			for i, item := range items {
				row := NavItem{
					Location: location, Label: item.Label, URL: item.URL,
					Permission: item.Permission, Sort: i * 10, Enabled: true,
					External: item.External || strings.HasPrefix(item.URL, "http://") || strings.HasPrefix(item.URL, "https://"),
				}
				if err := m.db.WithContext(ctx).Create(&row).Error; err != nil {
					return err
				}
			}
			return nil
		}
		if err := migrate("header", "site.nav.header"); err != nil {
			return err
		}
		if err := migrate("footer", "site.nav.footer"); err != nil {
			return err
		}
		// The settings-based navigation was only a migration source; clear it so
		// deleting every DB row does not resurrect the old menu on next boot.
		_ = m.settings.Set(ctx, "site.nav.header", []any{}, "system")
		_ = m.settings.Set(ctx, "site.nav.footer", []any{}, "system")
	}
	// Static pages moved from /p/:slug to /:slug; rewrite saved navigation.
	var legacy []NavItem
	if err := m.db.WithContext(ctx).Where("url LIKE ?", "/p/%").Find(&legacy).Error; err == nil {
		for _, row := range legacy {
			next := strings.TrimPrefix(row.URL, "/p")
			if next == "" {
				next = "/"
			}
			_ = m.db.WithContext(ctx).Model(&NavItem{}).Where("id = ?", row.ID).Update("url", next).Error
		}
	}
	m.invalidateLayout()
	return nil
}

type systemWidget struct {
	Key   string
	Title string
	Type  string
	Sort  int
}

var systemWidgets = []systemWidget{
	{Key: "user_card", Title: "个人资料", Type: "user_card", Sort: 10},
	{Key: "categories", Title: "板块", Type: "categories", Sort: 20},
	{Key: "trending", Title: "本周热门", Type: "trending", Sort: 30},
	{Key: "tags", Title: "热门标签", Type: "tags", Sort: 40},
	{Key: "stats", Title: "社区数据", Type: "stats", Sort: 35},
	{Key: "sidebar_ad", Title: "广告位", Type: "ad", Sort: 50},
	{Key: "promote_card", Title: "自助推广", Type: "promote", Sort: 60},
	{Key: "compose", Title: "分享你的想法", Type: "compose", Sort: 70},
}

const sidebarDefaultsVersion = 3

// ensureSidebarDefaults registers the built-in forum sidebar cards as
// manageable rows. It only runs once per installation so deleting a built-in
// card is persistent; the admin "restore defaults" action is explicit.
func (m *Module) ensureSidebarDefaults(ctx context.Context, force bool) error {
	if m.settings == nil {
		return nil
	}
	if !force && int(m.settings.GetInt("site.layout.sidebar_version")) >= sidebarDefaultsVersion {
		return nil
	}
	for _, item := range systemWidgets {
		var n int64
		if err := m.db.WithContext(ctx).Model(&SidebarWidget{}).Where("key = ?", item.Key).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		row := SidebarWidget{
			Key: item.Key, Title: item.Title, Type: item.Type, Sort: item.Sort, Enabled: true,
		}
		if err := m.db.WithContext(ctx).Create(&row).Error; err != nil {
			return err
		}
	}
	if !force {
		_ = m.settings.Set(ctx, "site.layout.sidebar_version", sidebarDefaultsVersion, "system")
	}
	m.invalidateLayout()
	return nil
}
