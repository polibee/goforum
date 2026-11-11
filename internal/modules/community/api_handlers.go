package community

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/authn"
	"goforum/internal/platform/httpx"
)

func (m *Module) apiCategories(c *gin.Context) {
	rows, err := m.svc.ListCategories(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, rows)
}

func (m *Module) apiTopics(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "20"))
	rows, total, err := m.svc.ListTopics(c.Request.Context(), TopicFilter{
		CategoryID: c.Query("category_id"),
		TagSlug:    c.Query("tag"),
		Query:      c.Query("q"),
		Sort:       c.Query("sort"),
		Page:       page,
		PerPage:    perPage,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"topics": rows, "total": total, "page": page, "per_page": perPage})
}

type apiTopicInput struct {
	CategoryID string   `json:"category_id"`
	Title      string   `json:"title"`
	Raw        string   `json:"raw"`
	Tags       []string `json:"tags"`
}

func (m *Module) apiCreateTopic(c *gin.Context) {
	var in apiTopicInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"body": "请求体格式错误"}))
		return
	}
	user := authn.FromContext(c.Request.Context())
	topic, post, err := m.svc.CreateTopic(c.Request.Context(), CreateTopicInput{
		CategoryID: in.CategoryID,
		UserID:     user.ID,
		TrustLevel: user.TrustLevel,
		Title:      in.Title,
		Raw:        in.Raw,
		TagIDs:     in.Tags,
		IP:         c.ClientIP(),
		UserAgent:  c.Request.UserAgent(),
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, gin.H{"topic": topic, "post": post, "url": "/t/" + topic.ID + "-" + topic.Slug})
}

func (m *Module) apiTopic(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	resolved, err := m.svc.ResolveTopicIdent(c.Request.Context(), c.Param("id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	detail, err := m.svc.GetTopicDetail(c.Request.Context(), resolved, page, 20, userID, canModerate(user))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, detail)
}

type apiReplyInput struct {
	Raw           string `json:"raw"`
	ReplyToPostID string `json:"reply_to_post_id"`
}

func (m *Module) apiReply(c *gin.Context) {
	var in apiReplyInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"body": "请求体格式错误"}))
		return
	}
	user := authn.FromContext(c.Request.Context())
	post, err := m.svc.Reply(c.Request.Context(), ReplyInput{
		TopicID: c.Param("id"), UserID: user.ID, Raw: in.Raw,
		ReplyToPostID: in.ReplyToPostID, IP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, post)
}

func (m *Module) apiEditPost(c *gin.Context) {
	var in struct {
		Raw string `json:"raw"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"body": "请求体格式错误"}))
		return
	}
	user := authn.FromContext(c.Request.Context())
	post, err := m.svc.EditPost(c.Request.Context(), EditPostInput{
		PostID: c.Param("id"), UserID: user.ID, Raw: in.Raw, IsModerator: canModerate(user),
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, post)
}

func (m *Module) apiReadState(c *gin.Context) {
	var in struct {
		TopicID    string `json:"topic_id"`
		PostNumber int    `json:"post_number"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"body": "请求体格式错误"}))
		return
	}
	user := authn.FromContext(c.Request.Context())
	if err := m.svc.MarkRead(c.Request.Context(), in.TopicID, user.ID, in.PostNumber); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (m *Module) apiPreview(c *gin.Context) {
	var in struct {
		Raw string `json:"raw"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.Invalid(map[string]string{"raw": "内容格式无效"}))
		return
	}
	httpx.OK(c, gin.H{"html": m.svc.RenderMarkdown(in.Raw)})
}

func (m *Module) apiUserCard(c *gin.Context) {
	ctx := c.Request.Context()
	profile, err := m.auth.GetByUsername(ctx, c.Param("username"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	topics, posts, likes, _ := m.svc.UserActivity(ctx, profile.ID)
	following := false
	if user := authn.FromContext(ctx); user != nil && m.engagement != nil {
		following, _ = m.engagement.Following(ctx, user.ID, "user", profile.ID)
	}
	httpx.OK(c, gin.H{
		"id": profile.ID, "username": profile.Username, "bio": profile.Bio,
		"trust_level": profile.TrustLevel, "created_at": profile.CreatedAt,
		"topics": topics, "posts": posts, "likes": likes, "following": following,
	})
}

// apiUserSearch powers @mention autocomplete. It only returns active users
// whose username starts with the typed prefix.
func (m *Module) apiUserSearch(c *gin.Context) {
	q := strings.TrimSpace(strings.ToLower(c.Query("q")))
	if q == "" {
		httpx.OK(c, gin.H{"users": []gin.H{}})
		return
	}
	rows, _, err := m.auth.ListFiltered(c.Request.Context(), q, "active", false, 20, 0)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]gin.H, 0, 8)
	for _, u := range rows {
		if !strings.HasPrefix(strings.ToLower(u.Username), q) {
			continue
		}
		out = append(out, gin.H{
			"username": u.Username, "avatar_url": u.AvatarURL, "trust_level": u.TrustLevel,
		})
		if len(out) >= 8 {
			break
		}
	}
	httpx.OK(c, gin.H{"users": out})
}
