package adminui

import (
	"sort"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/authn"
)

type NavItem struct {
	Group      string
	Label      string
	URL        string
	Permission string
	Order      int
	Icon       string
}

type Page struct {
	Method     string
	Path       string
	Permission string
	Handler    gin.HandlerFunc
}

type Widget struct {
	Key        string
	Title      string
	Permission string
	Order      int
}

type Contribution struct {
	Nav     []NavItem
	Pages   []Page
	Widgets []Widget
}

type ModuleInfo struct {
	Name            string
	DependsOn       []string
	PermissionCount int
	SettingCount    int
}

// Registry is what the admin shell needs; implemented by app.Registry.
type Registry interface {
	ModuleInfos() []ModuleInfo
	NavFor(principal *authn.Principal) []NavItem
}

type NavGroup struct {
	Key   string
	Label string
	Items []NavItem
}

var groupOrder = []string{
	"overview", "content", "users", "moderation", "ops", "billing", "site", "seo", "system",
}

var groupLabels = map[string]string{
	"overview":   "概览",
	"content":    "内容",
	"users":      "用户",
	"moderation": "审核",
	"ops":        "运营",
	"billing":    "商业化",
	"site":       "站点",
	"seo":        "SEO",
	"system":     "系统",
}

func BuildGroups(items []NavItem) []NavGroup {
	byGroup := make(map[string][]NavItem)
	for _, item := range items {
		byGroup[item.Group] = append(byGroup[item.Group], item)
	}
	out := make([]NavGroup, 0, len(byGroup))
	for _, key := range groupOrder {
		group, ok := byGroup[key]
		if !ok || len(group) == 0 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].Order == group[j].Order {
				return group[i].Label < group[j].Label
			}
			return group[i].Order < group[j].Order
		})
		out = append(out, NavGroup{Key: key, Label: groupLabels[key], Items: group})
		delete(byGroup, key)
	}
	// Unknown groups go last, sorted by key.
	unknown := make([]string, 0, len(byGroup))
	for key := range byGroup {
		unknown = append(unknown, key)
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		group := byGroup[key]
		sort.SliceStable(group, func(i, j int) bool { return group[i].Order < group[j].Order })
		out = append(out, NavGroup{Key: key, Label: key, Items: group})
	}
	return out
}
