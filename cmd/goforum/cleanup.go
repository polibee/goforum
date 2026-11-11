package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"goforum/internal/platform/config"
	"goforum/internal/platform/db"
)

// runCleanup removes rows whose parent is gone. Models do not declare foreign
// keys (GORM migrations would need them on every dialect), so orphan rows can
// accumulate after users or topics are deleted. This command is safe to run on
// a schedule; every statement is a bounded DELETE.
func runCleanup(cfgPath string, args []string) int {
	dryRun := len(args) > 0 && (args[0] == "-n" || args[0] == "--dry-run")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	gdb, err := db.Open(cfg.Database)
	if err != nil {
		fmt.Fprintln(os.Stderr, "database error:", err)
		return 1
	}
	defer db.Close(gdb)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Each entry pairs a predicate with its table so the dry run can count the
	// same rows the delete would remove.
	steps := []struct {
		name  string
		table string
		where string
	}{
		{"posts without topic", "posts", `topic_id NOT IN (SELECT id FROM topics)`},
		{"topics without category", "topics", `category_id NOT IN (SELECT id FROM categories)`},
		{"reactions without target", "reactions", `(target_type = 'topic' AND target_id NOT IN (SELECT id FROM topics)) OR (target_type = 'post' AND target_id NOT IN (SELECT id FROM posts))`},
		{"bookmarks without target", "bookmarks", `(target_type = 'topic' AND target_id NOT IN (SELECT id FROM topics)) OR (target_type = 'post' AND target_id NOT IN (SELECT id FROM posts))`},
		{"follow subscriptions without user", "follow_subscriptions", `user_id NOT IN (SELECT id FROM users)`},
		{"sessions without user", "sessions", `user_id NOT IN (SELECT id FROM users)`},
		{"notifications without user", "notifications", `user_id NOT IN (SELECT id FROM users)`},
		{"oauth identities without user", "oauth_identities", `user_id NOT IN (SELECT id FROM users)`},
		{"user groups without group", "user_groups", `group_id NOT IN (SELECT id FROM groups)`},
		{"expired sessions", "sessions", `expires_at < CURRENT_TIMESTAMP`},
	}

	total := int64(0)
	for _, step := range steps {
		if dryRun {
			var n int64
			countSQL := "SELECT COUNT(*) FROM " + step.table + " WHERE " + step.where
			if err := gdb.WithContext(ctx).Raw(countSQL).Scan(&n).Error; err != nil {
				fmt.Fprintf(os.Stderr, "%-32s skipped (%v)\n", step.name, err)
				continue
			}
			fmt.Printf("%-32s %d rows\n", step.name, n)
			total += n
			continue
		}
		res := gdb.WithContext(ctx).Exec("DELETE FROM " + step.table + " WHERE " + step.where)
		if res.Error != nil {
			fmt.Fprintf(os.Stderr, "%-32s failed (%v)\n", step.name, res.Error)
			continue
		}
		if res.RowsAffected > 0 {
			fmt.Printf("%-32s removed %d\n", step.name, res.RowsAffected)
		}
		total += res.RowsAffected
	}
	if dryRun {
		fmt.Printf("\nwould remove %d rows\n", total)
	} else {
		fmt.Printf("\nremoved %d rows\n", total)
	}
	return 0
}
