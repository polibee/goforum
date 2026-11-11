package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/adminui"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/session"
	"goforum/internal/platform/settings"
	"goforum/internal/platform/view"
)

type loginData struct {
	Login        string
	Error        string
	Action       string
	Next         string
	Providers    []contracts.OAuthProviderView
	HidePassword bool
}

type dailyActivityPoint struct {
	Label    string
	Users    int64
	Topics   int64
	Posts    int64
	UserPct  int
	TopicPct int
	PostPct  int
}

type dashboardData struct {
	UserCount       int64
	TopicCount      int64
	PostCount       int64
	PendingFlags    int64
	PendingReviews  int64
	PendingAppeals  int64
	PendingTotal    int64
	ModuleCount     int
	PermissionCount int
	SettingCount    int
	Modules         []adminui.ModuleInfo
	Activity        []dailyActivityPoint
	DBDriver        string
	Version         string
	Reindexed       string
	Error           string
}

func (m *Module) dailyActivity(ctx context.Context) []dailyActivityPoint {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -13)
	expr := "strftime('%Y-%m-%d', created_at)"
	switch m.deps.DB.Dialector.Name() {
	case "postgres":
		expr = "to_char(created_at, 'YYYY-MM-DD')"
	case "mysql":
		expr = "DATE_FORMAT(created_at, '%Y-%m-%d')"
	}
	type row struct {
		Day   string `gorm:"column:day"`
		Count int64  `gorm:"column:count"`
	}
	counts := make(map[string][3]int64)
	for idx, table := range []string{"users", "topics", "posts"} {
		var rows []row
		_ = m.deps.DB.WithContext(ctx).Table(table).
			Select(expr+" AS day, COUNT(*) AS count").
			Where("created_at >= ?", start).
			Group(expr).
			Scan(&rows).Error
		for _, r := range rows {
			v := counts[r.Day]
			v[idx] = r.Count
			counts[r.Day] = v
		}
	}
	points := make([]dailyActivityPoint, 0, 14)
	var max int64 = 1
	for i := 0; i < 14; i++ {
		d := start.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		v := counts[key]
		if v[0] > max {
			max = v[0]
		}
		if v[1] > max {
			max = v[1]
		}
		if v[2] > max {
			max = v[2]
		}
		points = append(points, dailyActivityPoint{
			Label: d.Format("01-02"), Users: v[0], Topics: v[1], Posts: v[2],
		})
	}
	for i := range points {
		points[i].UserPct = int(points[i].Users * 100 / max)
		points[i].TopicPct = int(points[i].Topics * 100 / max)
		points[i].PostPct = int(points[i].Posts * 100 / max)
	}
	return points
}

type settingsGroup struct {
	Key   string
	Label string
	Hint  string
	Items []settings.Item
}

type settingsPageData struct {
	Groups       []settingsGroup
	Saved        bool
	Error        string
	SecretErrors []string
}

type modulesData struct {
	Modules []adminui.ModuleInfo
}

type usersData struct {
	Users      []contracts.UserView
	Total      int64
	Query      string
	Status     string
	Page       int
	TotalPages int
	BasePath   string
	Error      string
}

func (m *Module) base(c *gin.Context, title string) view.Data {
	data := view.BaseData(c, m.deps.Settings.SiteInfo(), m.deps.Version)
	data.SetPageTitle(title, m.deps.Settings.GetString("seo.title_template"))
	if p := data.User; p != nil && m.deps.Registry != nil {
		data.AdminNav = m.deps.Registry.NavFor(p)
		data.AdminGroups = adminui.BuildGroups(data.AdminNav)
	}
	return data
}

func (m *Module) loginPage(c *gin.Context) {
	if authn.FromContext(c.Request.Context()) != nil {
		c.Redirect(http.StatusFound, "/admin")
		return
	}
	data := m.base(c, "登录")
	data.Data = loginData{Action: "/admin/login", Next: c.Query("next"), Providers: m.oauthProviders()}
	m.deps.Renderer.Render(c, http.StatusOK, "login", data)
}

func (m *Module) loginPost(c *gin.Context) {
	login := strings.TrimSpace(c.PostForm("login"))
	plain := c.PostForm("password")
	next := c.PostForm("next")
	if next == "" {
		next = c.Query("next")
	}
	if login == "" || plain == "" {
		m.renderLoginError(c, login, next, "请输入用户名与密码", http.StatusUnprocessableEntity)
		return
	}

	user, err := m.deps.Auth.Authenticate(c.Request.Context(), login, plain)
	if err != nil {
		m.renderLoginError(c, login, next, httpx.ErrorOf(err).Message, http.StatusUnauthorized)
		return
	}

	if tf, ok := m.deps.Auth.(contracts.TwoFactorAuthenticator); ok {
		switch tf.TwoFactorMode(c.Request.Context(), user) {
		case "verify":
			raw, err := tf.CreateLoginChallenge(c.Request.Context(), user.ID, safeNext(next))
			if err != nil {
				m.deps.Log.Error("create 2fa challenge failed", "error", err)
				m.renderLoginError(c, login, next, "登录失败，请稍后重试", http.StatusInternalServerError)
				return
			}
			session.SetTwoFactorCookie(c, m.deps.Config, raw, 5*time.Minute)
			c.Redirect(http.StatusFound, "/login/2fa")
			return
		case "setup":
			if _, token, err := m.deps.Sessions.Create(c.Request.Context(), user.ID, c.ClientIP(), c.Request.UserAgent()); err != nil {
				m.deps.Log.Error("create session failed", "error", err)
				m.renderLoginError(c, login, next, "登录失败，请稍后重试", http.StatusInternalServerError)
				return
			} else {
				session.SetCookie(c, m.deps.Config, token, m.deps.Sessions.TTL())
			}
			c.Redirect(http.StatusFound, "/settings/security/2fa/setup")
			return
		}
	}

	if _, token, err := m.deps.Sessions.Create(c.Request.Context(), user.ID, c.ClientIP(), c.Request.UserAgent()); err != nil {
		m.deps.Log.Error("create session failed", "error", err)
		m.renderLoginError(c, login, next, "登录失败，请稍后重试", http.StatusInternalServerError)
		return
	} else {
		session.SetCookie(c, m.deps.Config, token, m.deps.Sessions.TTL())
	}
	c.Redirect(http.StatusFound, safeNext(next))
}

func (m *Module) renderLoginError(c *gin.Context, login, next, message string, status int) {
	data := m.base(c, "登录")
	data.Data = loginData{Login: login, Error: message, Action: "/admin/login", Next: next, Providers: m.oauthProviders()}
	m.deps.Renderer.Render(c, status, "login", data)
}

func (m *Module) logout(c *gin.Context) {
	if v, ok := c.Get("session"); ok {
		if sess, ok := v.(*session.Session); ok {
			_ = m.deps.Sessions.Delete(c.Request.Context(), sess.ID)
		}
	}
	cookieName := "gf_session"
	if m.deps.Config != nil && m.deps.Config.Security.SessionCookie != "" {
		cookieName = m.deps.Config.Security.SessionCookie
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(cookieName, "", -1, "/", "", c.Request.TLS != nil, true)
	c.Redirect(http.StatusFound, "/admin/login")
}

func (m *Module) dashboard(c *gin.Context) {
	ctx := c.Request.Context()
	if p := authn.FromContext(ctx); p != nil && p.Can("moderation.queue.view") &&
		!p.Can("admin.user.view") && !p.Can("settings.read") {
		c.Redirect(http.StatusFound, "/admin/review")
		return
	}
	users, _ := m.deps.Auth.Count(ctx)

	var topics, posts int64
	_ = m.deps.DB.WithContext(ctx).Table("topics").Count(&topics).Error
	_ = m.deps.DB.WithContext(ctx).Table("posts").Count(&posts).Error

	var pendingFlags, pendingReviews, pendingAppeals int64
	_ = m.deps.DB.WithContext(ctx).Table("flags").Where("status = ?", "pending").Count(&pendingFlags).Error
	_ = m.deps.DB.WithContext(ctx).Table("review_queue").Where("status = ?", "pending").Count(&pendingReviews).Error
	_ = m.deps.DB.WithContext(ctx).Table("appeals").Where("status = ?", "pending").Count(&pendingAppeals).Error

	modules := []adminui.ModuleInfo{}
	if m.deps.Registry != nil {
		modules = m.deps.Registry.ModuleInfos()
	}
	permCount := 0
	if m.deps.Perms != nil {
		permCount = m.deps.Perms.Len()
	}
	settingCount := len(m.deps.Settings.Items(""))

	data := m.base(c, "仪表盘")
	data.Data = dashboardData{
		UserCount:       users,
		TopicCount:      topics,
		PostCount:       posts,
		PendingFlags:    pendingFlags,
		PendingReviews:  pendingReviews,
		PendingAppeals:  pendingAppeals,
		PendingTotal:    pendingFlags + pendingReviews + pendingAppeals,
		ModuleCount:     len(modules),
		Activity:        m.dailyActivity(ctx),
		PermissionCount: permCount,
		SettingCount:    settingCount,
		Modules:         modules,
		DBDriver:        m.deps.DB.Dialector.Name(),
		Version:         m.deps.Version,
		Reindexed:       c.Query("reindexed"),
		Error:           c.Query("error"),
	}
	m.deps.Renderer.Render(c, http.StatusOK, "admin_dashboard", data)
}

var groupLabels = map[string]string{
	"general":      "基础信息",
	"registration": "注册与登录",
	"verification": "账号验证",
	"content":      "内容与讨论",
	"appearance":   "外观",
	"navigation":   "导航与页面",
	"notification": "通知与邮件",
	"seo":          "SEO",
	"antispam":     "反垃圾",
	"media":        "上传与存储",
	"payment":      "支付",
	"search":       "搜索",
	"security":     "安全",
	"integrations": "集成",
	"maintenance":  "维护",
	"ops":          "运营",
	"trust":        "信任等级",
}

var groupHints = map[string]string{
	"general":      "每个页面都会用到的站点身份信息，建议先填这里。",
	"registration": "控制谁能注册以及新账号的初始限制。",
	"verification": "控制注册邮箱验证、密码找回、修改密码与换绑邮箱等自助安全功能。",
	"content":      "发帖、回复与内容展示的相关规则。",
	"appearance":   "配色、主题与 Logo。改完刷新前台即可看到效果。",
	"navigation":   "顶部导航与页脚链接的结构化配置。",
	"notification": "发信服务器与通知策略。不配置时邮件只会写入日志。",
	"seo":          "搜索引擎收录、标题模板与站点地图。",
	"antispam":     "关键词过滤与频率限制，减少垃圾内容。",
	"media":        "上传体积、允许的文件类型与图片处理。",
	"payment":      "支付渠道密钥与结算相关参数。",
	"search":       "站内搜索的行为与结果数量。",
	"security":     "会话、Cookie 与安全策略。",
	"integrations": "第三方服务接入，例如统计代码与登录方式。",
	"maintenance":  "维护模式开关与提示文案。",
	"ops":          "运营相关的积分、签到等玩法参数。",
	"trust":        "信任等级（TL0–TL4）的自动升级门槛。",
}

func (m *Module) settingsPage(c *gin.Context) {
	groups := make([]settingsGroup, 0)
	for _, key := range m.deps.Settings.Groups() {
		label := groupLabels[key]
		if label == "" {
			label = key
		}
		hint := groupHints[key]
		if hint == "" {
			hint = "该分组的配置项。"
		}
		groups = append(groups, settingsGroup{
			Key:   key,
			Label: label,
			Hint:  hint,
			Items: m.deps.Settings.Items(key),
		})
	}
	data := m.base(c, "站点设置")
	data.Data = settingsPageData{
		Groups:       groups,
		Saved:        c.Query("saved") == "1",
		SecretErrors: m.deps.Settings.SecretErrors(),
	}
	m.deps.Renderer.Render(c, http.StatusOK, "admin_settings", data)
}

func (m *Module) settingsSave(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		m.renderSettingsError(c, "表单解析失败")
		return
	}
	principal := authn.FromContext(c.Request.Context())
	actor := ""
	if principal != nil {
		actor = principal.ID
	}

	values := make(map[string]any)
	var problems []string

	for name, list := range c.Request.PostForm {
		if !strings.HasPrefix(name, "setting.") {
			continue
		}
		key := strings.TrimPrefix(name, "setting.")
		def, ok := m.deps.Settings.Def(key)
		if !ok {
			continue
		}
		raw := ""
		if len(list) > 0 {
			raw = list[len(list)-1]
		}

		switch def.Type {
		case settings.TypeBool:
			values[key] = raw == "true" || raw == "1"
		case settings.TypeInt:
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s 必须是整数", def.Label))
				continue
			}
			values[key] = n
		case settings.TypeJSON:
			if strings.TrimSpace(raw) == "" {
				values[key] = def.Default
				continue
			}
			var v any
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				problems = append(problems, fmt.Sprintf("%s 不是合法 JSON", def.Label))
				continue
			}
			values[key] = v
		case settings.TypeSecret:
			if raw == "" || raw == "••••••••" {
				continue
			}
			values[key] = raw
		default:
			values[key] = raw
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		m.renderSettingsError(c, strings.Join(problems, "；"))
		return
	}

	if err := m.deps.Settings.SetMany(c.Request.Context(), values, actor); err != nil {
		var ve *settings.ValidationError
		if errors.As(err, &ve) {
			fields := make([]string, 0, len(ve.Fields))
			for k, v := range ve.Fields {
				fields = append(fields, k+": "+v)
			}
			sort.Strings(fields)
			m.renderSettingsError(c, strings.Join(fields, "；"))
			return
		}
		m.deps.Log.Error("save settings failed", "error", err)
		m.renderSettingsError(c, "保存失败，请稍后重试")
		return
	}
	auditValues := make(map[string]any, len(values))
	for k, v := range values {
		if def, ok := m.deps.Settings.Def(k); ok && (def.Secret || def.Type == settings.TypeSecret) {
			auditValues[k] = "***"
			continue
		}
		auditValues[k] = v
	}
	m.writeAudit(c, "settings.update", "settings", "", "", nil, auditValues)

	c.Redirect(http.StatusFound, "/admin/settings?saved=1")
}

func (m *Module) renderSettingsError(c *gin.Context, message string) {
	groups := make([]settingsGroup, 0)
	for _, key := range m.deps.Settings.Groups() {
		label := groupLabels[key]
		if label == "" {
			label = key
		}
		hint := groupHints[key]
		if hint == "" {
			hint = "该分组的配置项。"
		}
		groups = append(groups, settingsGroup{Key: key, Label: label, Hint: hint, Items: m.deps.Settings.Items(key)})
	}
	data := m.base(c, "站点设置")
	data.Data = settingsPageData{Groups: groups, Error: message, SecretErrors: m.deps.Settings.SecretErrors()}
	m.deps.Renderer.Render(c, http.StatusUnprocessableEntity, "admin_settings", data)
}

func (m *Module) modulesPage(c *gin.Context) {
	modules := []adminui.ModuleInfo{}
	if m.deps.Registry != nil {
		modules = m.deps.Registry.ModuleInfos()
	}
	data := m.base(c, "模块清单")
	data.Data = modulesData{Modules: modules}
	m.deps.Renderer.Render(c, http.StatusOK, "admin_modules", data)
}

func (m *Module) usersPage(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const perPage = 20
	offset := (page - 1) * perPage

	query := strings.TrimSpace(c.Query("q"))
	status := c.Query("status")
	users, total, err := m.deps.Auth.ListFiltered(c.Request.Context(), query, status, true, perPage, offset)
	if err != nil {
		m.deps.Log.Error("list users failed", "error", err)
	}
	pages := int((total + int64(perPage) - 1) / int64(perPage))
	if pages < 1 {
		pages = 1
	}
	base := "/admin/users?q=" + url.QueryEscape(query) + "&status=" + url.QueryEscape(status)
	data := m.base(c, "用户")
	data.Data = usersData{Users: users, Total: total, Query: query, Status: status, Page: page, TotalPages: pages, BasePath: base, Error: c.Query("error")}
	m.deps.Renderer.Render(c, http.StatusOK, "admin_users", data)
}

func (m *Module) userStatus(c *gin.Context) {
	status := c.PostForm("status")
	if status == "" {
		status = "active"
	}
	// Disabling a super admin would lock the site out of its admin panel with
	// no recovery path, so that action is refused outright.
	if status != "active" {
		if target, err := m.deps.Auth.GetByID(c.Request.Context(), c.Param("id")); err == nil && target.SuperAdmin {
			c.Redirect(http.StatusFound, "/admin/users?error="+
				url.QueryEscape("不能限制超级管理员账号"))
			return
		}
	}
	if err := m.deps.Auth.SetStatus(c.Request.Context(), c.Param("id"), status); err != nil {
		m.deps.Log.Error("set user status failed", "error", err, "user_id", c.Param("id"))
	} else {
		m.writeAudit(c, "user.status", "user", c.Param("id"), "", nil, status)
	}
	c.Redirect(http.StatusFound, "/admin/users")
}

func (m *Module) usersBulk(c *gin.Context) {
	status := c.PostForm("status")
	ids := c.PostFormArray("ids")
	if len(ids) == 0 {
		c.Redirect(http.StatusFound, "/admin/users?error="+url.QueryEscape("请选择用户"))
		return
	}
	switch status {
	case "active", "suspended", "banned", "rejected":
	default:
		c.Redirect(http.StatusFound, "/admin/users?error="+url.QueryEscape("无效的用户状态"))
		return
	}
	if status != "active" {
		for _, id := range ids {
			if target, err := m.deps.Auth.GetByID(c.Request.Context(), id); err == nil && target.SuperAdmin {
				c.Redirect(http.StatusFound, "/admin/users?error="+
					url.QueryEscape("批量操作包含超级管理员，已取消"))
				return
			}
		}
	}
	for _, id := range ids {
		if err := m.deps.Auth.SetStatus(c.Request.Context(), id, status); err != nil {
			c.Redirect(http.StatusFound, "/admin/users?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
			return
		}
		m.writeAudit(c, "user.bulk.status", "user", id, "", nil, status)
	}
	c.Redirect(http.StatusFound, "/admin/users")
}

func safeNext(next string) string {
	if next == "" {
		return "/admin"
	}
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/admin"
	}
	// Backslashes are normalized to slashes by browsers, so reject them too.
	if adminSafePath(next) {
		return "/admin"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" || u.Scheme != "" {
		return "/admin"
	}
	if strings.HasPrefix(next, "/admin/login") {
		return "/admin"
	}
	return next
}

// adminSafePath reports whether a redirect target contains characters that
// browsers normalize into a scheme or host separator.
func adminSafePath(p string) bool {
	for _, r := range p {
		if r == 0x5c || r == 13 || r == 10 {
			return true
		}
	}
	return false
}
