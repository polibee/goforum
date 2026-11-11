package community

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/adminui"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/view"
)

func (m *Module) writeAudit(c *gin.Context, action, targetType, targetID, reason string) {
	if m.audit == nil {
		return
	}
	p := authn.FromContext(c.Request.Context())
	actor := ""
	if p != nil {
		actor = p.ID
	}
	_ = m.audit.Write(c.Request.Context(), audit.Entry{
		ActorID: actor, Action: action, TargetType: targetType,
		TargetID: targetID, Reason: reason,
		IP: c.ClientIP(), RequestID: c.GetString("request_id"),
	})
}

type adminTopicsData struct {
	Rows       []TopicRow
	Total      int64
	Page       int
	TotalPages int
	Status     string
	Query      string
	CategoryID string
	Categories []Category
	Back       string
	BasePath   string
	Error      string
}

type adminPostsData struct {
	Rows       []AdminPostRow
	Total      int64
	Page       int
	TotalPages int
	Status     string
	Back       string
	BasePath   string
	Error      string
}

type adminCategoriesData struct {
	Rows  []Category
	Error string
}

type adminTagsData struct {
	Rows       []Tag
	Total      int64
	Page       int
	TotalPages int
	BasePath   string
	Error      string
}

func (m *Module) adminBase(c *gin.Context, title string) view.Data {
	data := m.page(c, title)
	if p := data.User; p != nil && m.registry != nil {
		data.AdminNav = m.registry.NavFor(p)
		data.AdminGroups = adminui.BuildGroups(data.AdminNav)
	}
	return data
}

func (m *Module) adminTopics(c *gin.Context) {
	page := parsePage(c)
	status := c.Query("status")
	query := strings.TrimSpace(c.Query("q"))
	categoryID := c.Query("category_id")
	rows, total, err := m.svc.AdminListTopics(c.Request.Context(), AdminTopicFilter{
		Status: status, CategoryID: categoryID, Query: query, Page: page, PerPage: 20,
	})
	if err != nil {
		m.log.Error("admin list topics failed", "error", err)
	}
	categories, _ := m.svc.ListCategories(c.Request.Context())

	data := m.adminBase(c, "主题管理")
	data.Data = adminTopicsData{
		Rows: rows, Total: total, Page: page, TotalPages: totalPages(total, 20),
		Status: status, Query: query, CategoryID: categoryID,
		Categories: categories, Back: c.Request.URL.RequestURI(), Error: c.Query("error"),
		BasePath: "/admin/topics?status=" + url.QueryEscape(status) + "&q=" + url.QueryEscape(query) + "&category_id=" + url.QueryEscape(categoryID),
	}
	m.renderer.Render(c, http.StatusOK, "admin_topics", data)
}

func (m *Module) applyTopicAction(ctx context.Context, id, action string) error {
	switch action {
	case "pin":
		return m.svc.SetTopicFlags(ctx, id, map[string]any{"is_pinned": true})
	case "unpin":
		return m.svc.SetTopicFlags(ctx, id, map[string]any{"is_pinned": false})
	case "feature":
		return m.svc.SetTopicFlags(ctx, id, map[string]any{"is_featured": true})
	case "unfeature":
		return m.svc.SetTopicFlags(ctx, id, map[string]any{"is_featured": false})
	case "lock":
		return m.svc.SetTopicFlags(ctx, id, map[string]any{"is_locked": true})
	case "unlock":
		return m.svc.SetTopicFlags(ctx, id, map[string]any{"is_locked": false})
	case "delete":
		return m.svc.DeleteTopic(ctx, id)
	case "restore":
		return m.svc.RestoreTopic(ctx, id)
	default:
		return errUnknownAction
	}
}

func (m *Module) adminTopicsBulk(c *gin.Context) {
	action := c.PostForm("action")
	ids := c.PostFormArray("ids")
	back := safeAdminBack(c.PostForm("back"), "/admin/topics")
	if len(ids) == 0 {
		c.Redirect(http.StatusFound, withError(back, "请选择要操作的主题"))
		return
	}
	applied := 0
	for _, id := range ids {
		if err := m.applyTopicAction(c.Request.Context(), id, action); err != nil {
			c.Redirect(http.StatusFound, withError(back, errorText(err)))
			return
		}
		m.writeAudit(c, "topic.bulk."+action, "topic", id, "")
		applied++
	}
	_ = applied
	c.Redirect(http.StatusFound, back)
}

func (m *Module) adminTopicAction(c *gin.Context) {
	id := c.Param("id")
	action := c.PostForm("action")
	back := safeAdminBack(c.PostForm("back"), "/admin/topics")

	err := m.applyTopicAction(c.Request.Context(), id, action)
	if err != nil {
		back = withError(back, errorText(err))
	} else {
		m.writeAudit(c, "topic."+action, "topic", id, "")
	}
	c.Redirect(http.StatusFound, back)
}

func (m *Module) adminPosts(c *gin.Context) {
	page := parsePage(c)
	status := c.Query("status")
	rows, total, err := m.svc.AdminListPosts(c.Request.Context(), status, page, 20)
	if err != nil {
		m.log.Error("admin list posts failed", "error", err)
	}
	data := m.adminBase(c, "回复管理")
	data.Data = adminPostsData{
		Rows: rows, Total: total, Page: page, TotalPages: totalPages(total, 20),
		Status: status, Back: c.Request.URL.RequestURI(), Error: c.Query("error"),
		BasePath: "/admin/posts?status=" + url.QueryEscape(status),
	}
	m.renderer.Render(c, http.StatusOK, "admin_posts", data)
}

func (m *Module) applyPostAction(ctx context.Context, id, action string) error {
	switch action {
	case "hide":
		return m.svc.SetPostStatus(ctx, id, "hidden")
	case "visible":
		return m.svc.SetPostStatus(ctx, id, "visible")
	case "delete":
		return m.svc.DeletePost(ctx, id)
	case "restore":
		return m.svc.RestorePost(ctx, id)
	default:
		return errUnknownAction
	}
}

func (m *Module) adminPostsBulk(c *gin.Context) {
	action := c.PostForm("action")
	ids := c.PostFormArray("ids")
	back := safeAdminBack(c.PostForm("back"), "/admin/posts")
	if len(ids) == 0 {
		c.Redirect(http.StatusFound, withError(back, "请选择要操作的回复"))
		return
	}
	for _, id := range ids {
		if err := m.applyPostAction(c.Request.Context(), id, action); err != nil {
			c.Redirect(http.StatusFound, withError(back, errorText(err)))
			return
		}
		m.writeAudit(c, "post.bulk."+action, "post", id, "")
	}
	c.Redirect(http.StatusFound, back)
}

func (m *Module) adminPostAction(c *gin.Context) {
	id := c.Param("id")
	action := c.PostForm("action")
	back := safeAdminBack(c.PostForm("back"), "/admin/posts")

	err := m.applyPostAction(c.Request.Context(), id, action)
	if err != nil {
		back = withError(back, errorText(err))
	} else {
		m.writeAudit(c, "post."+action, "post", id, "")
	}
	c.Redirect(http.StatusFound, back)
}

func (m *Module) adminCategories(c *gin.Context) {
	rows, err := m.svc.ListCategories(c.Request.Context())
	if err != nil {
		m.log.Error("admin list categories failed", "error", err)
	}
	data := m.adminBase(c, "板块管理")
	data.Data = adminCategoriesData{Rows: rows, Error: c.Query("error")}
	m.renderer.Render(c, http.StatusOK, "admin_categories", data)
}

func (m *Module) adminCategoryCreate(c *gin.Context) {
	position, _ := strconv.Atoi(c.PostForm("position"))
	cat := &Category{
		Name:            strings.TrimSpace(c.PostForm("name")),
		Slug:            strings.TrimSpace(c.PostForm("slug")),
		Description:     strings.TrimSpace(c.PostForm("description")),
		Color:           strings.TrimSpace(c.PostForm("color")),
		Position:        position,
		Visibility:      defaultString(c.PostForm("visibility"), "public"),
		RequireApproval: c.PostForm("require_approval") != "",
	}
	if err := m.svc.CreateCategory(c.Request.Context(), cat); err != nil {
		c.Redirect(http.StatusFound, withError("/admin/categories", errorText(err)))
		return
	}
	c.Redirect(http.StatusFound, "/admin/categories")
}

func (m *Module) adminCategoryUpdate(c *gin.Context) {
	position, _ := strconv.Atoi(c.PostForm("position"))
	cat := &Category{
		ID:              c.Param("id"),
		Name:            strings.TrimSpace(c.PostForm("name")),
		Description:     strings.TrimSpace(c.PostForm("description")),
		Color:           strings.TrimSpace(c.PostForm("color")),
		Position:        position,
		Visibility:      defaultString(c.PostForm("visibility"), "public"),
		RequireApproval: c.PostForm("require_approval") != "",
	}
	if err := m.svc.UpdateCategory(c.Request.Context(), cat); err != nil {
		c.Redirect(http.StatusFound, withError("/admin/categories", errorText(err)))
		return
	}
	c.Redirect(http.StatusFound, "/admin/categories")
}

func (m *Module) adminCategoryDelete(c *gin.Context) {
	if err := m.svc.DeleteCategory(c.Request.Context(), c.Param("id")); err != nil {
		c.Redirect(http.StatusFound, withError("/admin/categories", errorText(err)))
		return
	}
	c.Redirect(http.StatusFound, "/admin/categories")
}

func (m *Module) adminTags(c *gin.Context) {
	page := parsePage(c)
	rows, total, err := m.svc.ListTagsPage(c.Request.Context(), 20, (page-1)*20)
	if err != nil {
		m.log.Error("admin list tags failed", "error", err)
	}
	data := m.adminBase(c, "标签管理")
	data.Data = adminTagsData{
		Rows: rows, Total: total, Page: page, TotalPages: totalPages(total, 20),
		BasePath: "/admin/tags", Error: c.Query("error"),
	}
	m.renderer.Render(c, http.StatusOK, "admin_tags", data)
}

func (m *Module) adminTagCreate(c *gin.Context) {
	tag := &Tag{
		Name:        strings.TrimSpace(c.PostForm("name")),
		Slug:        strings.TrimSpace(c.PostForm("slug")),
		Color:       strings.TrimSpace(c.PostForm("color")),
		Description: strings.TrimSpace(c.PostForm("description")),
	}
	if err := m.svc.CreateTag(c.Request.Context(), tag); err != nil {
		c.Redirect(http.StatusFound, withError("/admin/tags", errorText(err)))
		return
	}
	c.Redirect(http.StatusFound, "/admin/tags")
}

func (m *Module) adminTagDelete(c *gin.Context) {
	if err := m.svc.DeleteTag(c.Request.Context(), c.Param("id")); err != nil {
		c.Redirect(http.StatusFound, withError("/admin/tags", errorText(err)))
		return
	}
	c.Redirect(http.StatusFound, "/admin/tags")
}

func (m *Module) reindexSearch(c *gin.Context) {
	n, err := m.svc.ReindexSearch(c.Request.Context())
	if err != nil {
		c.Redirect(http.StatusFound, "/admin?error="+url.QueryEscape(errorText(err)))
		return
	}
	m.writeAudit(c, "search.reindex", "system", "", "")
	c.Redirect(http.StatusFound, "/admin?reindexed="+strconv.Itoa(n))
}

var errUnknownAction = httpx.NewError("validation.invalid", http.StatusUnprocessableEntity, "未知操作")

func safeAdminBack(back, fallback string) string {
	if strings.HasPrefix(back, "/admin/") && !strings.Contains(back, "://") {
		return back
	}
	return fallback
}

func withError(path, message string) string {
	if message == "" {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "error=" + url.QueryEscape(message)
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

type adminTopicEditData struct {
	Topic      Topic
	FirstPost  *PostRow
	Categories []Category
	Error      string
}

func (m *Module) adminTopicEditPage(c *gin.Context) {
	detail, err := m.svc.GetTopicDetail(c.Request.Context(), c.Param("id"), 1, 1, "", true)
	if err != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在")
		return
	}
	categories, _ := m.svc.ListCategories(c.Request.Context())
	data := m.adminBase(c, "编辑主题")
	var first *PostRow
	if len(detail.Posts) > 0 {
		first = &detail.Posts[0]
	}
	data.Data = adminTopicEditData{Topic: detail.Topic, FirstPost: first, Categories: categories, Error: c.Query("error")}
	m.renderer.Render(c, http.StatusOK, "admin_topic_edit", data)
}

func (m *Module) adminTopicEditSave(c *gin.Context) {
	id := c.Param("id")
	err := m.svc.AdminUpdateTopic(c.Request.Context(), id,
		c.PostForm("title"), c.PostForm("category_id"), c.PostForm("status"))
	if err == nil {
		if body := c.PostForm("body"); body != "" {
			if firstID := c.PostForm("first_post_id"); firstID != "" {
				actor := authn.FromContext(c.Request.Context())
				_, err = m.svc.EditPost(c.Request.Context(), EditPostInput{
					PostID: firstID, UserID: actor.ID, Raw: body, IsModerator: true,
				})
			}
		}
	}
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/topics/"+id+"/edit?error="+url.QueryEscape(httpx.ErrorOf(err).Message))
		return
	}
	c.Redirect(http.StatusFound, "/admin/topics")
}
