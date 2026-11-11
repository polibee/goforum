package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/view"
)

type groupOption struct {
	ID       string
	Name     string
	Color    string
	Selected bool
}

type groupsPageData struct {
	Groups []contracts.GroupView
	Perms  []permission.Definition
	Error  string
}

type userDetailData struct {
	User      *contracts.UserView
	Groups    []contracts.GroupView
	AllGroups []contracts.GroupView
	Options   []groupOption
	Error     string
}

type auditPageData struct {
	Entries    []audit.Entry
	Total      int64
	Page       int
	TotalPages int
	ActorNames map[string]string
	Actor      string
	Action     string
	TargetType string
}

func (m *Module) writeAudit(c *gin.Context, action, targetType, targetID, reason string, before, after any) {
	if m.deps.Audit == nil {
		return
	}
	p := authn.FromContext(c.Request.Context())
	actor := ""
	if p != nil {
		actor = p.ID
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	_ = m.deps.Audit.Write(c.Request.Context(), audit.Entry{
		ActorID: actor, Action: action, TargetType: targetType, TargetID: targetID,
		Reason: reason, BeforeJSON: string(beforeJSON), AfterJSON: string(afterJSON),
		IP: c.ClientIP(), RequestID: c.GetString("request_id"),
	})
}

func (m *Module) groupsPage(c *gin.Context) {
	groups, err := m.deps.Auth.ListGroups(c.Request.Context())
	if err != nil {
		m.deps.Log.Error("list groups failed", "error", err)
	}
	data := m.base(c, "用户组")
	data.Data = groupsPageData{
		Groups: groups, Perms: m.deps.Perms.All(), Error: c.Query("error"),
	}
	m.deps.Renderer.Render(c, http.StatusOK, "admin_groups", data)
}

func (m *Module) groupCreate(c *gin.Context) {
	priority, _ := strconv.Atoi(c.PostForm("priority"))
	_, err := m.deps.Auth.CreateGroup(c.Request.Context(), contracts.GroupInput{
		Name:        strings.TrimSpace(c.PostForm("name")),
		Slug:        strings.TrimSpace(c.PostForm("slug")),
		Color:       strings.TrimSpace(c.PostForm("color")),
		Priority:    priority,
		Permissions: c.PostFormArray("perms"),
	})
	if err != nil {
		c.Redirect(http.StatusFound, adminError("/admin/groups", err.Error()))
		return
	}
	m.writeAudit(c, "group.create", "group", "", "", nil, c.PostForm("name"))
	c.Redirect(http.StatusFound, "/admin/groups")
}

func (m *Module) groupUpdate(c *gin.Context) {
	priority, _ := strconv.Atoi(c.PostForm("priority"))
	in := contracts.GroupInput{
		ID: c.Param("id"), Name: strings.TrimSpace(c.PostForm("name")),
		Color: strings.TrimSpace(c.PostForm("color")), Priority: priority,
		Permissions: c.PostFormArray("perms"),
	}
	if err := m.deps.Auth.UpdateGroup(c.Request.Context(), in); err != nil {
		c.Redirect(http.StatusFound, adminError("/admin/groups", err.Error()))
		return
	}
	m.writeAudit(c, "group.update", "group", in.ID, "", nil, in)
	c.Redirect(http.StatusFound, "/admin/groups")
}

func (m *Module) groupDelete(c *gin.Context) {
	if err := m.deps.Auth.DeleteGroup(c.Request.Context(), c.Param("id")); err != nil {
		c.Redirect(http.StatusFound, adminError("/admin/groups", err.Error()))
		return
	}
	m.writeAudit(c, "group.delete", "group", c.Param("id"), "", nil, nil)
	c.Redirect(http.StatusFound, "/admin/groups")
}

func (m *Module) userDetail(c *gin.Context) {
	user, err := m.deps.Auth.GetByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		data := m.base(c, "用户")
		data.Data = view.ErrorData{Code: 404, Message: "用户不存在", RequestID: c.GetString("request_id")}
		m.deps.Renderer.Render(c, http.StatusNotFound, "error", data)
		return
	}
	groups, _ := m.deps.Auth.GroupsOf(c.Request.Context(), user.ID)
	all, _ := m.deps.Auth.ListGroups(c.Request.Context())
	selected := make(map[string]bool, len(groups))
	for _, g := range groups {
		selected[g.ID] = true
	}
	options := make([]groupOption, 0, len(all))
	for _, g := range all {
		options = append(options, groupOption{ID: g.ID, Name: g.Name, Color: g.Color, Selected: selected[g.ID]})
	}
	data := m.base(c, "用户："+user.Username)
	data.Data = userDetailData{User: user, Groups: groups, AllGroups: all, Options: options, Error: c.Query("error")}
	m.deps.Renderer.Render(c, http.StatusOK, "admin_user_detail", data)
}

func (m *Module) userGroupsSave(c *gin.Context) {
	userID := c.Param("id")
	if err := m.deps.Auth.SetGroups(c.Request.Context(), userID, c.PostFormArray("groups")); err != nil {
		c.Redirect(http.StatusFound, adminError("/admin/users/"+userID, err.Error()))
		return
	}
	m.writeAudit(c, "user.groups", "user", userID, "", nil, c.PostFormArray("groups"))
	c.Redirect(http.StatusFound, "/admin/users/"+userID)
}

func (m *Module) userTrustSave(c *gin.Context) {
	userID := c.Param("id")
	level, _ := strconv.Atoi(c.PostForm("trust_level"))
	if err := m.deps.Auth.SetTrustLevel(c.Request.Context(), userID, level); err != nil {
		c.Redirect(http.StatusFound, adminError("/admin/users/"+userID, err.Error()))
		return
	}
	m.writeAudit(c, "user.trust_level", "user", userID, "", nil, level)
	c.Redirect(http.StatusFound, "/admin/users/"+userID)
}

func (m *Module) auditPage(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	filter := audit.Filter{
		ActorID: c.Query("actor"), Action: c.Query("action"),
		TargetType: c.Query("target_type"), Page: page, PerPage: 20,
	}
	rows, total, err := m.deps.Audit.List(c.Request.Context(), filter)
	if err != nil {
		m.deps.Log.Error("list audit failed", "error", err)
	}
	ids := make([]string, 0, len(rows))
	for _, e := range rows {
		if e.ActorID != "" {
			ids = append(ids, e.ActorID)
		}
	}
	names := map[string]string{}
	if len(ids) > 0 {
		if sums, err := m.deps.Auth.GetSummaries(c.Request.Context(), ids); err == nil {
			for id, u := range sums {
				names[id] = u.Username
			}
		}
	}
	pages := int((total + 19) / 20)
	if pages < 1 {
		pages = 1
	}
	data := m.base(c, "审计日志")
	data.Data = auditPageData{
		Entries: rows, Total: total, Page: page, TotalPages: pages,
		ActorNames: names, Actor: filter.ActorID, Action: filter.Action, TargetType: filter.TargetType,
	}
	m.deps.Renderer.Render(c, http.StatusOK, "admin_audit", data)
}

func adminError(path, message string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "error=" + url.QueryEscape(message)
}
