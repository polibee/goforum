package community

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goforum/internal/modules/auth/contracts"
	cmod "goforum/internal/modules/community/contracts"
	"goforum/internal/platform/audit"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/httpx"
	"goforum/internal/platform/markdown"
	"goforum/internal/platform/seo"
	"goforum/internal/platform/view"
)

type feedData struct {
	Heading        string
	Sort           string
	Subheading     string
	Query          string
	SearchType     string
	Feed           string
	Users          []contracts.UserView
	Categories     []Category
	Tags           []Tag
	Topics         []TopicRow
	Total          int64
	Page           int
	PerPage        int
	TotalPages     int
	ActiveCategory *Category
	ActiveTag      *Tag
	BasePath       string
	NoIndex        bool
	Following      bool
	FollowType     string
	FollowID       string
	Trending       []Topic
	Stats          SiteStats
}

type topicPageData struct {
	Detail          *TopicDetail
	Quote           string
	ReplyTo         string
	Related         []Topic
	OnlyOP          bool
	Sort            string
	FirstUnread     int
	CanEditTopic    bool
	FollowingAuthor bool
	AuthorFollowers int64
	AuthorFollowing int64
	CurrentUser     *authn.Principal
	PageCount       int
	CanModerate     bool
	CanPin          bool
	CanFeature      bool
	LikedPosts      map[string]bool
	Bookmarked      bool
	Following       bool
	TipSuccess      bool
	Error           string
}

type composerData struct {
	Categories       []Category
	Tags             []Tag
	SelectedCategory string
	Title            string
	Raw              string
	SelectedTagIDs   []string
	Error            string
}

type editPostData struct {
	Post  *Post
	Error string
}

type editTopicData struct {
	Topic *Topic
	Raw   string
	Error string
}

type userPageData struct {
	Profile        *contracts.UserView
	BioHTML        template.HTML
	Topics         []TopicRow
	Posts          []PostRow
	Users          []contracts.UserSummary
	Badges         []cmod.BadgeView
	Activity       []ActivityDay
	Following      bool
	Tab            string
	Page           int
	TotalPages     int
	TopicTotal     int64
	PostTotal      int64
	LikesTotal     int64
	FollowersCount int64
	FollowingCount int64
	BasePath       string
}

func (m *Module) page(c *gin.Context, title string) view.Data {
	data := view.BaseData(c, m.settings.SiteInfo(), m.version)
	data.SetPageTitle(title, m.settings.GetString("seo.title_template"))
	return data
}

func (m *Module) renderError(c *gin.Context, status int, message string) {
	data := m.page(c, "")
	data.Title = data.Site.Name
	data.Data = view.ErrorData{Code: status, Message: message, RequestID: c.GetString("request_id")}
	m.renderer.Render(c, status, "error", data)
}

func (m *Module) home(c *gin.Context) {
	page := parsePage(c)
	perPage := int(m.svc.settingInt("community.list.topics_per_page", 30))
	viewerID := ""
	if current := authn.FromContext(c.Request.Context()); current != nil {
		viewerID = current.ID
	}
	query := strings.TrimSpace(c.Query("q"))
	searchType := c.Query("type")
	if searchType != "topic" && searchType != "post" && searchType != "user" {
		searchType = ""
	}
	sort := c.Query("sort")
	if sort != "latest" {
		sort = ""
	}

	feed := c.Query("feed")
	if feed != "following" || query != "" {
		feed = ""
	}
	var topics []TopicRow
	var feedUsers []contracts.UserView
	var total int64
	var err error
	if feed == "following" {
		if user := authn.FromContext(c.Request.Context()); user != nil && m.engagement != nil {
			authors, cats, tagIDs, ferr := m.engagement.FollowedTargets(c.Request.Context(), user.ID)
			if ferr == nil && len(authors)+len(cats)+len(tagIDs) > 0 {
				topics, total, err = m.svc.ListTopics(c.Request.Context(), TopicFilter{
					Page: page, PerPage: perPage, Sort: c.Query("sort"), ViewerID: viewerID,
					FollowedAuthors: authors, FollowedCategories: cats, FollowedTags: tagIDs,
				})
			}
		}
	} else if query != "" && searchType == "user" {
		users, userTotal, uerr := m.auth.ListFiltered(c.Request.Context(), query, "active", false, perPage, (page-1)*perPage)
		if uerr != nil {
			err = uerr
		} else {
			feedUsers = users
			total = userTotal
		}
	} else {
		if query != "" {
			topics, total, err = m.svc.SearchTopicsAdvanced(c.Request.Context(), query, searchType, sort, page, perPage)
		} else {
			topics, total, err = m.svc.ListTopics(c.Request.Context(), TopicFilter{
				Page: page, PerPage: perPage, Sort: c.Query("sort"), ViewerID: viewerID,
			})
		}
	}
	if err != nil {
		m.log.Error("list topics failed", "error", err)
		m.renderError(c, http.StatusInternalServerError, "加载主题失败")
		return
	}
	categories, _ := m.svc.ListCategories(c.Request.Context())
	tags, _ := m.svc.ListTags(c.Request.Context(), 30)
	trending, _ := m.svc.TrendingTopics(c.Request.Context(), 5)
	stats := m.svc.SiteStats(c.Request.Context())

	heading := "最新主题"
	sub := "看看社区正在讨论什么"
	base := "/"
	if feed == "following" {
		heading = "关注动态"
		sub = "你关注的用户、板块和标签下的最新主题"
		base = "/?feed=following"
	} else if query != "" {
		heading = "搜索：" + query
		sub = "按标题与摘要匹配"
		if searchType == "user" {
			sub = "按用户名与简介匹配"
		}
		values := url.Values{}
		values.Set("q", query)
		if searchType != "" {
			values.Set("type", searchType)
		}
		if sort != "" {
			values.Set("sort", sort)
		}
		base = "/search?" + values.Encode()
	}

	data := m.page(c, heading)
	if query != "" || feed == "following" {
		data.Robots = "noindex,follow"
	}
	if data.Description == "" {
		data.Description = m.settings.GetString("seo.default_description")
	}
	if query == "" {
		data.JSONLD = seo.WebSite(data.Site, data.Site.URL)
	}
	data.Data = feedData{
		Heading: heading, Subheading: sub, Query: query, SearchType: searchType, Feed: feed, Sort: c.Query("sort"),
		Categories: categories, Tags: tags, Topics: topics, Users: feedUsers, Trending: trending, Stats: stats,
		Total: total, Page: page, PerPage: perPage,
		TotalPages: totalPages(total, perPage), BasePath: base, NoIndex: query != "",
	}
	m.renderer.Render(c, http.StatusOK, "feed", data)
}

func (m *Module) searchPage(c *gin.Context) { m.home(c) }

func (m *Module) categoryPage(c *gin.Context) {
	cat, err := m.svc.GetCategoryBySlug(c.Request.Context(), c.Param("slug"))
	if err != nil {
		m.renderError(c, http.StatusNotFound, "板块不存在")
		return
	}
	page := parsePage(c)
	perPage := int(m.svc.settingInt("community.list.topics_per_page", 30))
	catViewerID := ""
	if current := authn.FromContext(c.Request.Context()); current != nil {
		catViewerID = current.ID
	}
	topics, total, err := m.svc.ListTopics(c.Request.Context(), TopicFilter{
		CategoryID: cat.ID, Page: page, PerPage: perPage, Sort: c.Query("sort"), ViewerID: catViewerID,
	})
	if err != nil {
		m.renderError(c, http.StatusInternalServerError, "加载主题失败")
		return
	}
	categories, _ := m.svc.ListCategories(c.Request.Context())
	tags, _ := m.svc.ListTags(c.Request.Context(), 30)
	trending, _ := m.svc.TrendingTopics(c.Request.Context(), 5)
	stats := m.svc.SiteStats(c.Request.Context())

	data := m.page(c, cat.Name)
	data.Description = seo.Truncate(cat.Description, 160)
	if data.Description == "" {
		data.Description = seo.Truncate(m.settings.GetString("seo.default_description"), 160)
	}
	if total == 0 {
		data.Robots = "noindex,follow"
	}
	if data.Site.URL != "" {
		base := strings.TrimRight(data.Site.URL, "/")
		data.JSONLD = seo.Graph(
			seo.Breadcrumbs([]seo.Crumb{
				{Name: data.Site.Name, URL: base + "/"},
				{Name: cat.Name, URL: base + "/c/" + cat.Slug},
			}),
			seo.WebSite(data.Site, data.Site.URL),
		)
	}
	fd := feedData{
		Heading: cat.Name, Subheading: cat.Description, Sort: c.Query("sort"),
		Categories: categories, Tags: tags, Topics: topics, Trending: trending, Stats: stats,
		Total: total, Page: page, PerPage: perPage,
		TotalPages: totalPages(total, perPage), BasePath: "/c/" + cat.Slug,
		ActiveCategory: cat, FollowType: "category", FollowID: cat.ID,
	}
	if user := authn.FromContext(c.Request.Context()); user != nil && m.engagement != nil {
		fd.Following, _ = m.engagement.Following(c.Request.Context(), user.ID, "category", cat.ID)
	}
	data.Data = fd
	m.renderer.Render(c, http.StatusOK, "feed", data)
}

func (m *Module) tagPage(c *gin.Context) {
	slug := c.Param("slug")
	page := parsePage(c)
	perPage := int(m.svc.settingInt("community.list.topics_per_page", 30))
	tagViewerID := ""
	if current := authn.FromContext(c.Request.Context()); current != nil {
		tagViewerID = current.ID
	}
	topics, total, err := m.svc.ListTopics(c.Request.Context(), TopicFilter{
		TagSlug: slug, Page: page, PerPage: perPage, Sort: c.Query("sort"), ViewerID: tagViewerID,
	})
	if err != nil {
		m.renderError(c, http.StatusInternalServerError, "加载主题失败")
		return
	}
	categories, _ := m.svc.ListCategories(c.Request.Context())
	tags, _ := m.svc.ListTags(c.Request.Context(), 30)
	trending, _ := m.svc.TrendingTopics(c.Request.Context(), 5)
	stats := m.svc.SiteStats(c.Request.Context())

	data := m.page(c, "#"+slug)
	fd := feedData{
		Heading: "#" + slug, Subheading: "标签下的主题", Sort: c.Query("sort"),
		Categories: categories, Tags: tags, Topics: topics, Trending: trending, Stats: stats,
		Total: total, Page: page, PerPage: perPage,
		TotalPages: totalPages(total, perPage), BasePath: "/tags/" + slug,
	}
	if tag, err := m.svc.GetTagBySlug(c.Request.Context(), slug); err == nil {
		fd.FollowType, fd.FollowID = "tag", tag.ID
		data.Description = seo.Truncate(tag.Description, 160)
		if data.Description == "" {
			data.Description = "浏览标签 #" + tag.Name + " 下的社区主题"
		}
		if data.Site.URL != "" {
			base := strings.TrimRight(data.Site.URL, "/")
			data.JSONLD = seo.Breadcrumbs([]seo.Crumb{
				{Name: data.Site.Name, URL: base + "/"},
				{Name: "#" + tag.Name, URL: base + "/tags/" + tag.Slug},
			})
		}
		if user := authn.FromContext(c.Request.Context()); user != nil && m.engagement != nil {
			fd.Following, _ = m.engagement.Following(c.Request.Context(), user.ID, "tag", tag.ID)
		}
	}
	if total == 0 {
		data.Robots = "noindex,follow"
	}
	data.Data = fd
	m.renderer.Render(c, http.StatusOK, "feed", data)
}

func maxPostNumber(posts []PostRow) int {
	max := 0
	for i := range posts {
		if posts[i].PostNumber > max {
			max = posts[i].PostNumber
		}
		if child := maxPostNumber(posts[i].Children); child > max {
			max = child
		}
	}
	return max
}

func (m *Module) topicPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}
	isMod := canModerate(user)
	page := parsePage(c)
	perPage := 20

	onlyOP := c.Query("op") == "1"
	newestFirst := c.Query("sort") == "desc"
	sortOrder := ""
	if newestFirst {
		sortOrder = "desc"
	}
	resolvedID, resolveErr := m.svc.ResolveTopicIdent(c.Request.Context(), c.Param("ident"))
	if resolveErr != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在或不可见")
		return
	}
	detail, err := m.svc.GetTopicDetailOrdered(c.Request.Context(), resolvedID, page, perPage, userID, isMod, onlyOP, newestFirst)
	if err != nil {
		status := http.StatusNotFound
		if e := httpx.ErrorOf(err); e.Status == http.StatusGone {
			status = http.StatusGone
		}
		m.renderError(c, status, httpx.ErrorOf(err).Message)
		return
	}
	if detail.Topic.Number > 0 && c.Param("ident") != strconv.FormatInt(detail.Topic.Number, 10) {
		target := "/t/" + strconv.FormatInt(detail.Topic.Number, 10)
		if q := c.Request.URL.RawQuery; q != "" {
			target += "?" + q
		}
		c.Redirect(http.StatusMovedPermanently, target)
		return
	}
	m.svc.IncrementView(c.Request.Context(), detail.Topic.ID)
	if userID != "" && len(detail.Posts) > 0 {
		_ = m.svc.MarkRead(c.Request.Context(), detail.Topic.ID, userID, maxPostNumber(detail.Posts))
	}

	data := m.page(c, detail.Topic.Title)
	data.Description = seo.Truncate(detail.Topic.Excerpt, 160)
	if data.Description == "" {
		data.Description = seo.Truncate(detail.Topic.Title, 160)
	}
	data.OGType = "article"
	created := detail.Topic.CreatedAt
	updated := detail.Topic.UpdatedAt
	data.PublishedAt = &created
	data.ModifiedAt = &updated
	if detail.Topic.Status == "pending" || detail.Topic.Status == "hidden" {
		data.Robots = "noindex,nofollow"
	}
	if data.Site.URL != "" {
		base := strings.TrimRight(data.Site.URL, "/")
		topicURL := data.Canonical
		if topicURL == "" {
			topicURL = base + c.Request.URL.Path
		}
		docs := []template.JS{seo.DiscussionForumPosting(
			topicURL, detail.Topic.Title, detail.Topic.Excerpt,
			detail.AuthorName, base+"/u/"+detail.AuthorName,
			created, updated, int(detail.Topic.ReplyCount),
		)}
		crumbs := []seo.Crumb{{Name: data.Site.Name, URL: base + "/"}}
		if detail.Category.Name != "" {
			crumbs = append(crumbs, seo.Crumb{Name: detail.Category.Name, URL: base + "/c/" + detail.Category.Slug})
		}
		crumbs = append(crumbs, seo.Crumb{Name: detail.Topic.Title, URL: topicURL})
		docs = append(docs, seo.Breadcrumbs(crumbs))
		data.JSONLD = seo.Graph(docs...)
	}
	quote := ""
	if quoteID := strings.TrimSpace(c.Query("quote")); quoteID != "" {
		if quoted, err := m.svc.GetPost(c.Request.Context(), quoteID); err == nil {
			author := ""
			if u, err := m.auth.GetByID(c.Request.Context(), quoted.UserID); err == nil {
				author = u.Username
			}
			var b strings.Builder
			if author != "" {
				b.WriteString("> @" + author + "\n")
			}
			for _, line := range strings.Split(strings.TrimSpace(quoted.Raw), "\n") {
				b.WriteString("> " + line + "\n")
			}
			b.WriteString("\n")
			quote = b.String()
		}
	}
	related, _ := m.svc.RelatedTopics(c.Request.Context(), detail.Topic.ID, detail.Topic.CategoryID, 5)
	firstUnread := 0
	if detail.ReadState.LastReadPostNumber > 0 && sortOrder == "" {
		for _, post := range detail.Posts {
			if post.PostNumber > detail.ReadState.LastReadPostNumber {
				firstUnread = post.PostNumber
				break
			}
		}
	}
	canEditTopic := isMod
	if !canEditTopic && user != nil && detail.Topic.UserID == user.ID {
		window := time.Duration(m.svc.settingInt("community.post.edit_window_minutes", 30)) * time.Minute
		canEditTopic = time.Since(detail.Topic.CreatedAt) <= window
	}
	tp := topicPageData{
		Detail:       detail,
		Quote:        quote,
		ReplyTo:      strings.TrimSpace(c.Query("reply_to")),
		Related:      related,
		OnlyOP:       onlyOP,
		Sort:         sortOrder,
		FirstUnread:  firstUnread,
		CanEditTopic: canEditTopic,
		CurrentUser:  user,
		PageCount:    totalPages(detail.TotalPosts, perPage),
		CanModerate:  isMod,
		CanPin:       isMod && user != nil && (user.SuperAdmin || user.Can("topic.pin")),
		CanFeature:   isMod && user != nil && (user.SuperAdmin || user.Can("topic.feature")),
		TipSuccess:   c.Query("tip") == "1",
	}
	if m.engagement != nil {
		tp.AuthorFollowers, tp.AuthorFollowing, _ = m.engagement.FollowStats(c.Request.Context(), detail.Topic.UserID)
	}
	if userID != "" && m.engagement != nil {
		ids := make([]string, 0, len(detail.Posts))
		for _, p := range detail.Posts {
			ids = append(ids, p.ID)
		}
		tp.LikedPosts, tp.Bookmarked, tp.Following, _ = m.engagement.TopicState(c.Request.Context(), userID, detail.Topic.ID, ids)
		tp.FollowingAuthor, _ = m.engagement.Following(c.Request.Context(), userID, "user", detail.Topic.UserID)
	}
	data.Data = tp
	m.renderer.Render(c, http.StatusOK, "topic", data)
}

func (m *Module) newTopicPage(c *gin.Context) {
	categories, _ := m.svc.ListCategories(c.Request.Context())
	tags, _ := m.svc.ListTags(c.Request.Context(), 100)
	data := m.page(c, "发布主题")
	data.Data = composerData{
		Categories: categories, Tags: tags,
		SelectedCategory: c.Query("category"),
	}
	m.renderer.Render(c, http.StatusOK, "composer", data)
}

func (m *Module) createTopic(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	categoryID := c.PostForm("category_id")
	title := c.PostForm("title")
	raw := c.PostForm("raw")
	tagIDs := c.PostFormArray("tags")
	pollQuestion := strings.TrimSpace(c.PostForm("poll_question"))
	pollOptions := make([]string, 0, 10)
	if rawOptions := c.PostForm("poll_options"); rawOptions != "" {
		for _, line := range strings.Split(rawOptions, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				pollOptions = append(pollOptions, line)
			}
		}
	}
	if pollQuestion != "" && len(pollOptions) < 2 {
		categories, _ := m.svc.ListCategories(c.Request.Context())
		tags, _ := m.svc.ListTags(c.Request.Context(), 100)
		data := m.page(c, "发布主题")
		data.Data = composerData{Categories: categories, Tags: tags, Title: title, Raw: raw,
			SelectedCategory: categoryID, Error: "投票至少需要两个选项"}
		m.renderer.Render(c, http.StatusUnprocessableEntity, "composer", data)
		return
	}

	topic, _, err := m.svc.CreateTopic(c.Request.Context(), CreateTopicInput{
		CategoryID:   categoryID,
		PollQuestion: pollQuestion,
		PollOptions:  pollOptions,
		PollMultiple: c.PostForm("poll_multiple") != "",
		UserID:       user.ID,
		TrustLevel:   user.TrustLevel,
		Title:        title,
		Raw:          raw,
		TagIDs:       tagIDs,
		IP:           c.ClientIP(),
		UserAgent:    c.Request.UserAgent(),
	})
	if err != nil {
		categories, _ := m.svc.ListCategories(c.Request.Context())
		tags, _ := m.svc.ListTags(c.Request.Context(), 100)
		data := m.page(c, "发布主题")
		data.Data = composerData{
			Categories: categories, Tags: tags,
			SelectedCategory: categoryID, Title: title, Raw: raw,
			SelectedTagIDs: tagIDs, Error: errorText(err),
		}
		m.renderer.Render(c, http.StatusUnprocessableEntity, "composer", data)
		return
	}
	c.Redirect(http.StatusFound, "/t/"+strconv.FormatInt(topic.Number, 10))
}

func (m *Module) replyPost(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	ident := c.Param("ident")
	post, err := m.svc.Reply(c.Request.Context(), ReplyInput{
		TopicID:       topicID(ident),
		UserID:        user.ID,
		Raw:           c.PostForm("raw"),
		ReplyToPostID: c.PostForm("reply_to_post_id"),
		IP:            c.ClientIP(),
		UserAgent:     c.Request.UserAgent(),
	})
	if err != nil {
		m.renderTopicWithError(c, ident, errorText(err))
		return
	}
	c.Redirect(http.StatusFound, "/t/"+ident+"#post-"+post.ID)
}

func (m *Module) renderTopicWithError(c *gin.Context, ident, message string) {
	user := authn.FromContext(c.Request.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}
	isMod := canModerate(user)
	resolved, rerr := m.svc.ResolveTopicIdent(c.Request.Context(), ident)
	if rerr != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在或不可见")
		return
	}
	detail, err := m.svc.GetTopicDetail(c.Request.Context(), resolved, parsePage(c), 20, userID, isMod)
	if err != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在或不可见")
		return
	}
	data := m.page(c, detail.Topic.Title)
	data.Data = topicPageData{
		Detail: detail, CurrentUser: user,
		PageCount: totalPages(detail.TotalPosts, 20), CanModerate: isMod, Error: message,
		CanPin:     isMod && user != nil && (user.SuperAdmin || user.Can("topic.pin")),
		CanFeature: isMod && user != nil && (user.SuperAdmin || user.Can("topic.feature")),
	}
	m.renderer.Render(c, http.StatusUnprocessableEntity, "topic", data)
}

// topicModerate toggles pin/feature/lock from the topic page. It reuses the
// admin flag whitelist but requires topic.moderate and writes an audit entry.
func (m *Module) topicModerate(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	if !canModerate(user) {
		m.renderError(c, http.StatusForbidden, "没有权限管理该主题")
		return
	}
	resolved, err := m.svc.ResolveTopicIdent(c.Request.Context(), c.Param("ident"))
	if err != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在或不可见")
		return
	}
	action := c.PostForm("action")
	var updates map[string]any
	switch action {
	case "pin":
		updates = map[string]any{"is_pinned": true}
	case "unpin":
		updates = map[string]any{"is_pinned": false}
	case "feature":
		updates = map[string]any{"is_featured": true}
	case "unfeature":
		updates = map[string]any{"is_featured": false}
	case "lock":
		updates = map[string]any{"is_locked": true}
	case "unlock":
		updates = map[string]any{"is_locked": false}
	default:
		m.renderError(c, http.StatusBadRequest, "未知操作")
		return
	}
	if err := m.svc.SetTopicFlags(c.Request.Context(), resolved, updates); err != nil {
		m.renderError(c, http.StatusInternalServerError, errorText(err))
		return
	}
	if m.audit != nil {
		detail, derr := m.svc.GetTopicDetail(c.Request.Context(), resolved, 1, 1, user.ID, true)
		target := resolved
		if derr == nil {
			target = detail.Topic.ID
		}
		m.audit.Write(c.Request.Context(), audit.Entry{
			ActorID: user.ID, Action: "topic." + action, TargetType: "topic", TargetID: target,
			Reason: "前端页面操作", IP: c.ClientIP(), RequestID: c.GetString("request_id"),
		})
	}
	back := c.PostForm("back")
	if back == "" || !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/t/" + c.Param("ident")
	}
	c.Redirect(http.StatusFound, back)
}

func (m *Module) markRead(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	postNumber, _ := strconv.Atoi(c.PostForm("post_number"))
	if err := m.svc.MarkRead(c.Request.Context(), topicID(c.Param("ident")), user.ID, postNumber); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (m *Module) legacyPostEditRedirect(c *gin.Context) {
	c.Redirect(http.StatusMovedPermanently, "/posts/"+c.Param("id")+"/edit")
}

func (m *Module) editPostPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	post, err := m.svc.GetPost(c.Request.Context(), c.Param("id"))
	if err != nil {
		m.renderError(c, http.StatusNotFound, "回复不存在")
		return
	}
	if !canModerate(user) && post.UserID != user.ID {
		m.renderError(c, http.StatusForbidden, "没有权限编辑该回复")
		return
	}
	data := m.page(c, "编辑回复")
	data.Data = editPostData{Post: post}
	m.renderer.Render(c, http.StatusOK, "edit_post", data)
}

func (m *Module) editPostSave(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	post, err := m.svc.EditPost(c.Request.Context(), EditPostInput{
		PostID: c.Param("id"), UserID: user.ID, Raw: c.PostForm("raw"), IsModerator: canModerate(user),
	})
	if err != nil {
		current, _ := m.svc.GetPost(c.Request.Context(), c.Param("id"))
		data := m.page(c, "编辑回复")
		data.Data = editPostData{Post: current, Error: errorText(err)}
		m.renderer.Render(c, http.StatusUnprocessableEntity, "edit_post", data)
		return
	}
	c.Redirect(http.StatusFound, "/t/"+post.TopicID+"#post-"+post.ID)
}

func (m *Module) editTopicPage(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	m.renderEditTopic(c, user, http.StatusOK, "")
}

func (m *Module) renderEditTopic(c *gin.Context, user *authn.Principal, status int, message string) {
	resolved, err := m.svc.ResolveTopicIdent(c.Request.Context(), c.Param("ident"))
	if err != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在或不可见")
		return
	}
	detail, err := m.svc.GetTopicDetail(c.Request.Context(), resolved, 1, 1, user.ID, canModerate(user))
	if err != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在或不可见")
		return
	}
	if !canModerate(user) && detail.Topic.UserID != user.ID {
		m.renderError(c, http.StatusForbidden, "没有权限编辑该主题")
		return
	}
	raw := ""
	if len(detail.Posts) > 0 {
		raw = detail.Posts[0].Raw
	}
	data := m.page(c, "编辑主题")
	data.Robots = "noindex,nofollow"
	data.Data = editTopicData{Topic: &detail.Topic, Raw: raw, Error: message}
	m.renderer.Render(c, status, "edit_topic", data)
}

func (m *Module) editTopicSave(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	resolved, err := m.svc.ResolveTopicIdent(c.Request.Context(), c.Param("ident"))
	if err != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在或不可见")
		return
	}
	topic, _, err := m.svc.EditTopic(c.Request.Context(), EditTopicInput{
		TopicID: resolved, UserID: user.ID,
		Title: c.PostForm("title"), Raw: c.PostForm("raw"), IsModerator: canModerate(user),
	})
	if err != nil {
		m.renderEditTopic(c, user, http.StatusUnprocessableEntity, errorText(err))
		return
	}
	c.Redirect(http.StatusFound, "/t/"+strconv.FormatInt(topic.Number, 10))
}

func (m *Module) userPage(c *gin.Context) {
	profile, err := m.auth.GetByUsername(c.Request.Context(), c.Param("username"))
	if err != nil {
		m.renderError(c, http.StatusNotFound, "用户不存在")
		return
	}
	page := parsePage(c)
	const perPage = 20
	topics, topicTotal, _ := m.svc.ListUserTopics(c.Request.Context(), profile.ID, page, perPage)
	posts, postTotal, _ := m.svc.ListUserPosts(c.Request.Context(), profile.ID, page, perPage)
	data := m.page(c, profile.Username)
	bioText := markdown.Excerpt(profile.Bio, 160)
	data.Description = seo.Truncate(bioText, 160)
	if data.Description == "" {
		data.Description = profile.Username + " 的社区主页"
	}
	if !m.settings.GetBool("seo.index_users") || profile.Status != "active" {
		data.Robots = "noindex,follow"
	}
	if data.Site.URL != "" {
		base := strings.TrimRight(data.Site.URL, "/")
		data.JSONLD = seo.Person(profile.Username, base+"/u/"+profile.Username, bioText)
	}
	tab := c.Query("tab")
	switch tab {
	case "posts", "following", "followers":
	default:
		tab = "topics"
	}
	total := topicTotal
	if tab == "posts" {
		total = postTotal
	}
	var followersCount, followingCount int64
	if m.engagement != nil {
		followersCount, followingCount, _ = m.engagement.FollowStats(c.Request.Context(), profile.ID)
	}
	var users []contracts.UserSummary
	if tab == "following" || tab == "followers" {
		if m.engagement != nil {
			var ids []string
			if tab == "followers" {
				ids, total, _ = m.engagement.ListFollowers(c.Request.Context(), profile.ID, perPage, (page-1)*perPage)
			} else {
				ids, total, _ = m.engagement.ListFollowing(c.Request.Context(), profile.ID, perPage, (page-1)*perPage)
			}
			if len(ids) > 0 {
				if sums, err := m.auth.GetSummaries(c.Request.Context(), ids); err == nil {
					users = make([]contracts.UserSummary, 0, len(ids))
					for _, id := range ids {
						if u, ok := sums[id]; ok {
							users = append(users, u)
						}
					}
				}
			}
		}
	}
	pages := int((total + perPage - 1) / perPage)
	if pages < 1 {
		pages = 1
	}
	_, _, likes, _ := m.svc.UserActivity(c.Request.Context(), profile.ID)
	up := userPageData{Profile: profile, Topics: topics, Posts: posts, Users: users, Tab: tab,
		Page: page, TotalPages: pages, TopicTotal: topicTotal, PostTotal: postTotal, LikesTotal: likes,
		FollowersCount: followersCount, FollowingCount: followingCount,
		BasePath: "/u/" + profile.Username + "?tab=" + tab}
	if profile.BioEnabled && profile.Bio != "" {
		up.BioHTML = template.HTML(m.svc.RenderMarkdown(profile.Bio))
	}
	if m.badges != nil {
		up.Badges, _ = m.badges.UserBadges(c.Request.Context(), profile.ID)
	}
	up.Activity, _ = m.svc.UserDailyActivity(c.Request.Context(), profile.ID, 84)
	if user := authn.FromContext(c.Request.Context()); user != nil && m.engagement != nil {
		up.Following, _ = m.engagement.Following(c.Request.Context(), user.ID, "user", profile.ID)
	}
	data.Data = up
	m.renderer.Render(c, http.StatusOK, "user", data)
}

func parsePage(c *gin.Context) int {
	page, _ := strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	return page
}

func topicID(ident string) string {
	if i := strings.IndexByte(ident, '-'); i > 0 {
		return ident[:i]
	}
	return ident
}

func canModerate(p *authn.Principal) bool {
	return p != nil && (p.SuperAdmin || p.Can("topic.moderate"))
}

func totalPages(total int64, perPage int) int {
	if perPage <= 0 {
		perPage = 20
	}
	n := int((total + int64(perPage) - 1) / int64(perPage))
	if n < 1 {
		n = 1
	}
	return n
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

func errorText(err error) string {
	e := httpx.ErrorOf(err)
	if len(e.Fields) > 0 {
		for _, v := range e.Fields {
			return v
		}
	}
	if e.Message != "" {
		return e.Message
	}
	return "操作失败，请稍后重试"
}

func (m *Module) acceptAnswer(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	ident := c.Param("ident")
	resolved, err := m.svc.ResolveTopicIdent(c.Request.Context(), ident)
	if err != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在")
		return
	}
	if err := m.svc.AcceptAnswer(c.Request.Context(), resolved, c.PostForm("post_id"), user.ID, canModerate(user)); err != nil {
		m.renderError(c, http.StatusForbidden, httpx.ErrorOf(err).Message)
		return
	}
	c.Redirect(http.StatusFound, "/t/"+ident+"#post-"+c.PostForm("post_id"))
}

func (m *Module) votePoll(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	resolved, err := m.svc.ResolveTopicIdent(c.Request.Context(), c.Param("ident"))
	if err != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在")
		return
	}
	if err := m.svc.VotePoll(c.Request.Context(), resolved, user.ID, c.PostFormArray("option")); err != nil {
		m.renderError(c, http.StatusUnprocessableEntity, httpx.ErrorOf(err).Message)
		return
	}
	c.Redirect(http.StatusFound, "/t/"+c.Param("ident"))
}

func (m *Module) tipTopic(c *gin.Context) {
	user := authn.FromContext(c.Request.Context())
	resolved, err := m.svc.ResolveTopicIdent(c.Request.Context(), c.Param("ident"))
	if err != nil {
		m.renderError(c, http.StatusNotFound, "主题不存在")
		return
	}
	points, _ := strconv.ParseInt(c.PostForm("points"), 10, 64)
	var tipErr error
	if postID := strings.TrimSpace(c.PostForm("post_id")); postID != "" {
		tipErr = m.svc.TipPost(c.Request.Context(), user.ID, postID, points)
	} else {
		tipErr = m.svc.TipTopic(c.Request.Context(), user.ID, resolved, points)
	}
	if tipErr != nil {
		m.renderError(c, http.StatusUnprocessableEntity, httpx.ErrorOf(tipErr).Message)
		return
	}
	c.Redirect(http.StatusFound, "/t/"+c.Param("ident")+"?tip=1")
}
