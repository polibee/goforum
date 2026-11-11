package community

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/modules/auth/contracts"
	"goforum/internal/platform/markdown"
)

type fakeUsers struct{}

func (fakeUsers) GetSummaries(_ context.Context, ids []string) (map[string]contracts.UserSummary, error) {
	out := make(map[string]contracts.UserSummary, len(ids))
	for _, id := range ids {
		out[id] = contracts.UserSummary{ID: id, Username: "user-" + id}
	}
	return out, nil
}

func (fakeUsers) FindByUsernames(_ context.Context, usernames []string) (map[string]contracts.UserSummary, error) {
	out := make(map[string]contracts.UserSummary, len(usernames))
	for _, name := range usernames {
		out[name] = contracts.UserSummary{ID: "u-" + name, Username: name}
	}
	return out, nil
}

func testService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "community.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := NewService(db)
	svc.Configure(nil, nil, markdown.New(), fakeUsers{})
	return svc
}

func TestCreateTopicReplyAndReadState(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()

	if err := svc.EnsureDefaults(ctx); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	cats, err := svc.ListCategories(ctx)
	if err != nil || len(cats) != 1 {
		t.Fatalf("categories = %d, err = %v", len(cats), err)
	}

	topic, first, err := svc.CreateTopic(ctx, CreateTopicInput{
		CategoryID: cats[0].ID,
		UserID:     "u1",
		TrustLevel: 1,
		Title:      "Hello World",
		Raw:        "**hello** world",
	})
	if err != nil {
		t.Fatalf("create topic: %v", err)
	}
	if topic.Slug != "hello-world" {
		t.Fatalf("slug = %q", topic.Slug)
	}
	if first.PostNumber != 1 || topic.PostCount != 1 {
		t.Fatalf("post number = %d, post_count = %d", first.PostNumber, topic.PostCount)
	}
	if topic.Excerpt != "hello world" {
		t.Fatalf("excerpt = %q", topic.Excerpt)
	}

	reply, err := svc.Reply(ctx, ReplyInput{TopicID: topic.ID, UserID: "u2", Raw: "second post"})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if reply.PostNumber != 2 {
		t.Fatalf("reply number = %d", reply.PostNumber)
	}

	detail, err := svc.GetTopicDetail(ctx, topic.ID, 1, 20, "u1", false)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(detail.Posts) != 2 {
		t.Fatalf("posts = %d", len(detail.Posts))
	}
	if detail.Topic.ReplyCount != 1 {
		t.Fatalf("reply_count = %d", detail.Topic.ReplyCount)
	}
	if !detail.CanReply {
		t.Fatal("topic should accept replies")
	}
	if detail.Posts[1].AuthorName != "user-u2" {
		t.Fatalf("author = %q", detail.Posts[1].AuthorName)
	}
	opDetail, err := svc.GetTopicDetailOnlyOP(ctx, topic.ID, 1, 20, "u1", false)
	if err != nil {
		t.Fatalf("only op: %v", err)
	}
	if len(opDetail.Posts) != 1 || opDetail.TotalPosts != 1 || opDetail.Posts[0].UserID != "u1" {
		t.Fatalf("only op posts = %d total = %d", len(opDetail.Posts), opDetail.TotalPosts)
	}
	descDetail, err := svc.GetTopicDetailOrdered(ctx, topic.ID, 1, 20, "u1", false, false, true)
	if err != nil {
		t.Fatalf("desc detail: %v", err)
	}
	if len(descDetail.Posts) != 2 || descDetail.Posts[0].PostNumber != 2 || descDetail.Posts[1].PostNumber != 1 {
		t.Fatalf("desc posts = %+v", descDetail.Posts)
	}

	if err := svc.MarkRead(ctx, topic.ID, "u1", 2); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	detail, _ = svc.GetTopicDetail(ctx, topic.ID, 1, 20, "u1", false)
	if detail.ReadState.LastReadPostNumber != 2 {
		t.Fatalf("read state = %d", detail.ReadState.LastReadPostNumber)
	}

	rows, total, err := svc.ListTopics(ctx, TopicFilter{Page: 1, PerPage: 20})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("list = %d/%d, err = %v", len(rows), total, err)
	}
	if rows[0].AuthorName != "user-u1" || rows[0].CategoryName != "综合讨论" {
		t.Fatalf("row = %+v", rows[0])
	}

	cats, _ = svc.ListCategories(ctx)
	if cats[0].TopicCount != 1 || cats[0].PostCount != 2 {
		t.Fatalf("category counters = %d/%d", cats[0].TopicCount, cats[0].PostCount)
	}
}

func TestNestedReplyGrouping(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	if err := svc.EnsureDefaults(ctx); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	cats, _ := svc.ListCategories(ctx)
	topic, first, err := svc.CreateTopic(ctx, CreateTopicInput{
		CategoryID: cats[0].ID, UserID: "u1", TrustLevel: 1, Title: "Nested thread", Raw: "root",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	parent, err := svc.Reply(ctx, ReplyInput{TopicID: topic.ID, UserID: "u2", Raw: "parent"})
	if err != nil {
		t.Fatalf("parent: %v", err)
	}
	child, err := svc.Reply(ctx, ReplyInput{TopicID: topic.ID, UserID: "u3", Raw: "child", ReplyToPostID: parent.ID})
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	if _, err := svc.Reply(ctx, ReplyInput{TopicID: topic.ID, UserID: "u4", Raw: "grandchild", ReplyToPostID: child.ID}); err != nil {
		t.Fatalf("grandchild: %v", err)
	}
	detail, err := svc.GetTopicDetail(ctx, topic.ID, 1, 20, "u1", false)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(detail.Posts) != 2 {
		t.Fatalf("top-level posts = %d, want 2", len(detail.Posts))
	}
	if detail.Posts[0].ID != first.ID || len(detail.Posts[0].Children) != 0 {
		t.Fatalf("root post = %+v", detail.Posts[0])
	}
	if len(detail.Posts[1].Children) != 1 || detail.Posts[1].Children[0].Raw != "child" {
		t.Fatalf("children = %+v", detail.Posts[1].Children)
	}
	grand := detail.Posts[1].Children[0].Children
	if len(grand) != 1 || grand[0].Raw != "grandchild" {
		t.Fatalf("grandchildren = %+v", grand)
	}
}

func TestEditPostAndRevisions(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	_ = svc.EnsureDefaults(ctx)
	cats, _ := svc.ListCategories(ctx)
	topic, _, err := svc.CreateTopic(ctx, CreateTopicInput{
		CategoryID: cats[0].ID, UserID: "u1", TrustLevel: 1,
		Title: "Editable topic", Raw: "original",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	reply, err := svc.Reply(ctx, ReplyInput{TopicID: topic.ID, UserID: "u2", Raw: "before"})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}

	edited, err := svc.EditPost(ctx, EditPostInput{PostID: reply.ID, UserID: "u2", Raw: "**after**"})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.EditCount != 1 || edited.EditedAt == nil {
		t.Fatalf("edit metadata = %d/%v", edited.EditCount, edited.EditedAt)
	}

	var revisions int64
	if err := svc.db.Model(&PostRevision{}).Where("post_id = ?", reply.ID).Count(&revisions).Error; err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if revisions != 1 {
		t.Fatalf("revisions = %d", revisions)
	}

	if _, err := svc.EditPost(ctx, EditPostInput{PostID: reply.ID, UserID: "u3", Raw: "hijack"}); err == nil {
		t.Fatal("non-author edit must fail")
	}
}

func TestTrendingTopics(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	_ = svc.EnsureDefaults(ctx)
	cats, _ := svc.ListCategories(ctx)
	topic, _, err := svc.CreateTopic(ctx, CreateTopicInput{
		CategoryID: cats[0].ID, UserID: "u1", TrustLevel: 1,
		Title: "Trending candidate", Raw: "body",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Reply(ctx, ReplyInput{TopicID: topic.ID, UserID: "u2", Raw: "reply"}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	rows, err := svc.TrendingTopics(ctx, 5)
	if err != nil || len(rows) == 0 || rows[0].ID != topic.ID {
		t.Fatalf("trending = %+v err=%v", rows, err)
	}
}

func TestEditTopicTitleAndBody(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	_ = svc.EnsureDefaults(ctx)
	cats, _ := svc.ListCategories(ctx)
	topic, _, err := svc.CreateTopic(ctx, CreateTopicInput{
		CategoryID: cats[0].ID, UserID: "u1", TrustLevel: 1,
		Title: "Original title", Raw: "original body",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, post, err := svc.EditTopic(ctx, EditTopicInput{
		TopicID: topic.ID, UserID: "u1", Title: "Updated title", Raw: "**updated** body",
	})
	if err != nil {
		t.Fatalf("edit topic: %v", err)
	}
	if updated.Title != "Updated title" || updated.Excerpt != "updated body" {
		t.Fatalf("topic = %+v", updated)
	}
	if post.EditCount != 1 || post.Raw != "**updated** body" {
		t.Fatalf("post = %+v", post)
	}
	var count int64
	if err := svc.db.Model(&PostRevision{}).Where("post_id = ?", post.ID).Count(&count).Error; err != nil {
		t.Fatalf("revisions: %v", err)
	}
	if count != 1 {
		t.Fatalf("revisions = %d", count)
	}
	if _, _, err := svc.EditTopic(ctx, EditTopicInput{TopicID: topic.ID, UserID: "u2", Title: "Hijacked", Raw: "nope"}); err == nil {
		t.Fatal("non-author topic edit must fail")
	}
}

func TestLockedTopicRejectsReply(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	_ = svc.EnsureDefaults(ctx)
	cats, _ := svc.ListCategories(ctx)
	topic, _, err := svc.CreateTopic(ctx, CreateTopicInput{
		CategoryID: cats[0].ID, UserID: "u1", TrustLevel: 1,
		Title: "Locked topic", Raw: "body",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.SetTopicFlags(ctx, topic.ID, map[string]any{"is_locked": true}); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if _, err := svc.Reply(ctx, ReplyInput{TopicID: topic.ID, UserID: "u2", Raw: "nope"}); err == nil {
		t.Fatal("locked topic accepted reply")
	}
}

func TestTopicListUnreadState(t *testing.T) {
	svc := testService(t)
	ctx := context.Background()
	_ = svc.EnsureDefaults(ctx)
	cats, _ := svc.ListCategories(ctx)
	topic, _, err := svc.CreateTopic(ctx, CreateTopicInput{
		CategoryID: cats[0].ID, UserID: "u1", TrustLevel: 1,
		Title: "Unread marker", Raw: "body",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Reply(ctx, ReplyInput{TopicID: topic.ID, UserID: "u1", Raw: "second"}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	rows, _, err := svc.ListTopics(ctx, TopicFilter{Page: 1, PerPage: 20, ViewerID: "u2"})
	if err != nil || len(rows) != 1 || !rows[0].Unread {
		t.Fatalf("guest read state = %+v err=%v", rows, err)
	}
	if err := svc.MarkRead(ctx, topic.ID, "u2", 2); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	rows, _, err = svc.ListTopics(ctx, TopicFilter{Page: 1, PerPage: 20, ViewerID: "u2"})
	if err != nil || len(rows) != 1 || rows[0].Unread {
		t.Fatalf("read state = %+v err=%v", rows, err)
	}
}

func TestMaxPostNumberIncludesNested(t *testing.T) {
	posts := []PostRow{
		{PostNumber: 1, Children: []PostRow{{PostNumber: 3, Children: []PostRow{{PostNumber: 4}}}}},
		{PostNumber: 2},
	}
	if got := maxPostNumber(posts); got != 4 {
		t.Fatalf("maxPostNumber = %d", got)
	}
}
