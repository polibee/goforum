package notification

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"goforum/internal/platform/events"
)

func TestNotifierFiresOnNewNotification(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "notification.db")), &gorm.Config{})
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
	got := make(chan string, 1)
	svc.SetNotifier(func(userID string) { got <- userID })

	payload, _ := json.Marshal(postCreatedPayload{
		TopicID: "t1", PostID: "p1", TopicAuthorID: "u2", ActorID: "u1",
		Title: "hello", PostNumber: 2,
	})
	if err := svc.onPostCreated(context.Background(), events.Event{ID: "e1", Payload: payload}); err != nil {
		t.Fatalf("event: %v", err)
	}
	var count int64
	if err := db.Model(&Notification{}).Where("user_id = ?", "u2").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("notification count = %d", count)
	}
	select {
	case userID := <-got:
		if userID != "u2" {
			t.Fatalf("notifier user = %q", userID)
		}
	case <-time.After(time.Second):
		t.Fatal("notifier was not called")
	}
}

func TestHubPublishesToSubscriber(t *testing.T) {
	h := newHub()
	ch, unsubscribe := h.subscribe("u1")
	defer unsubscribe()
	h.publish("u1", []byte(`{"unread":1}`))
	select {
	case got := <-ch:
		if string(got) != `{"unread":1}` {
			t.Fatalf("payload = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("hub did not deliver")
	}
}

func TestModerationNotification(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "moderation-notify.db")), &gorm.Config{})
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
	payload, _ := json.Marshal(moderationPayload{
		RecipientID: "u1", ModeratorID: "mod1", TargetType: "topic", TargetID: "t1",
		Title: "hello", URL: "/t/1", Action: "hide",
	})
	if err := svc.onModeration(context.Background(), events.Event{ID: "me1", Payload: payload}); err != nil {
		t.Fatalf("moderation event: %v", err)
	}
	var n Notification
	if err := db.Where("user_id = ? AND type = ?", "u1", "moderation").First(&n).Error; err != nil {
		t.Fatalf("notification: %v", err)
	}
	var data map[string]any
	_ = json.Unmarshal([]byte(n.DataJSON), &data)
	if data["action"] != "hide" || data["url"] != "/t/1" {
		t.Fatalf("data = %+v", data)
	}
}

func TestReportResolutionNotification(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "report-notify.db")), &gorm.Config{})
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
	payload, _ := json.Marshal(moderationPayload{
		RecipientID: "reporter1", ModeratorID: "mod1", TargetType: "post", TargetID: "p1",
		Title: "hello", URL: "/t/1#post-p1", Action: "hide",
	})
	if err := svc.onReportResolved(context.Background(), events.Event{ID: "rr1", Payload: payload}); err != nil {
		t.Fatalf("report event: %v", err)
	}
	var n Notification
	if err := db.Where("user_id = ? AND type = ?", "reporter1", "moderation.report").First(&n).Error; err != nil {
		t.Fatalf("notification: %v", err)
	}
}

func TestAppealNotification(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "appeal-notify.db")), &gorm.Config{})
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
	payload, _ := json.Marshal(appealPayload{
		RecipientID: "u1", ModeratorID: "mod1", Username: "user", Title: "账号申诉",
		URL: "/", Action: "approve",
	})
	if err := svc.onAppeal(context.Background(), events.Event{ID: "ap1", Payload: payload}); err != nil {
		t.Fatalf("appeal event: %v", err)
	}
	var n Notification
	if err := db.Where("user_id = ? AND type = ?", "u1", "appeal").First(&n).Error; err != nil {
		t.Fatalf("notification: %v", err)
	}
}

func TestQuotedReplyNotifiesParentAuthor(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "quote-notify.db")), &gorm.Config{})
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
	payload, _ := json.Marshal(postCreatedPayload{
		TopicID: "t1", PostID: "p3", TopicAuthorID: "u2", ActorID: "u1",
		ReplyToPostID: "p2", ReplyToAuthorID: "u3", Title: "hello", PostNumber: 3,
	})
	if err := svc.onPostCreated(context.Background(), events.Event{ID: "e2", Payload: payload}); err != nil {
		t.Fatalf("event: %v", err)
	}
	wantTypes := map[string]string{"u2": "post.reply", "u3": "post.quote"}
	for userID, want := range wantTypes {
		var rows []Notification
		if err := db.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
			t.Fatalf("load %s: %v", userID, err)
		}
		if len(rows) != 1 || rows[0].Type != want {
			t.Fatalf("notification for %s = %+v, want type %s", userID, rows, want)
		}
	}
}

func TestTopicFollowersGetWatchNotification(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "watch-notify.db")), &gorm.Config{})
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
	payload, _ := json.Marshal(postCreatedPayload{
		TopicID: "t1", PostID: "p4", TopicAuthorID: "u2", ActorID: "u1",
		FollowerIDs: []string{"u4", "u1", "u2"}, Title: "hello", PostNumber: 4,
	})
	if err := svc.onPostCreated(context.Background(), events.Event{ID: "e3", Payload: payload}); err != nil {
		t.Fatalf("event: %v", err)
	}
	var watch Notification
	if err := db.Where("user_id = ?", "u4").First(&watch).Error; err != nil || watch.Type != "post.watch" {
		t.Fatalf("follower notification = %+v err=%v", watch, err)
	}
	var actorCount, authorCount int64
	_ = db.Model(&Notification{}).Where("user_id = ?", "u1").Count(&actorCount).Error
	_ = db.Model(&Notification{}).Where("user_id = ? AND type = ?", "u2", "post.reply").Count(&authorCount).Error
	if actorCount != 0 || authorCount != 1 {
		t.Fatalf("actor=%d author=%d", actorCount, authorCount)
	}
}
