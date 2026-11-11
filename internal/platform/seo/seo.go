// Package seo builds metadata and structured data shared by public pages.
package seo

import (
	"encoding/json"
	"html/template"
	"strings"
	"time"

	"goforum/internal/platform/settings"
)

// JSON marshals v into a safe template.JS value. encoding/json escapes <, > and &,
// so the result is safe to embed inside a script tag.
func JSON(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return template.JS(b)
}

// WebSite returns WebSite + SearchAction structured data.
func WebSite(site settings.Site, baseURL string) template.JS {
	base := strings.TrimRight(baseURL, "/")
	name := site.Name
	if name == "" {
		name = "GoForum"
	}
	payload := map[string]any{
		"@context": "https://schema.org",
		"@type":    "WebSite",
		"name":     name,
		"url":      base + "/",
		"potentialAction": map[string]any{
			"@type":       "SearchAction",
			"target":      base + "/search?q={search_term_string}",
			"query-input": "required name=search_term_string",
		},
	}
	if site.Description != "" {
		payload["description"] = site.Description
	}
	return JSON(payload)
}

type Crumb struct {
	Name string
	URL  string
}

// Breadcrumbs returns BreadcrumbList structured data.
func Breadcrumbs(items []Crumb) template.JS {
	list := make([]map[string]any, 0, len(items))
	for i, item := range items {
		list = append(list, map[string]any{
			"@type":    "ListItem",
			"position": i + 1,
			"name":     item.Name,
			"item":     item.URL,
		})
	}
	return JSON(map[string]any{
		"@context":        "https://schema.org",
		"@type":           "BreadcrumbList",
		"itemListElement": list,
	})
}

// Graph combines several JSON-LD documents into a single @graph document.
func Graph(docs ...template.JS) template.JS {
	nodes := make([]json.RawMessage, 0, len(docs))
	for _, doc := range docs {
		if len(doc) > 0 {
			nodes = append(nodes, json.RawMessage(doc))
		}
	}
	if len(nodes) == 0 {
		return ""
	}
	out, err := json.Marshal(map[string]any{
		"@context": "https://schema.org",
		"@graph":   nodes,
	})
	if err != nil {
		return ""
	}
	return template.JS(out)
}

// DiscussionForumPosting describes a topic page.
func DiscussionForumPosting(topicURL, title, text string, authorName, authorURL string, createdAt, updatedAt time.Time, replyCount int) template.JS {
	payload := map[string]any{
		"@context":      "https://schema.org",
		"@type":         "DiscussionForumPosting",
		"headline":      title,
		"url":           topicURL,
		"datePublished": createdAt.UTC().Format(time.RFC3339),
		"dateModified":  updatedAt.UTC().Format(time.RFC3339),
		"commentCount":  replyCount,
		"interactionStatistic": map[string]any{
			"@type":                "InteractionCounter",
			"interactionType":      "https://schema.org/CommentAction",
			"userInteractionCount": replyCount,
		},
	}
	if text != "" {
		payload["text"] = text
	}
	if authorName != "" {
		author := map[string]any{"@type": "Person", "name": authorName}
		if authorURL != "" {
			author["url"] = authorURL
		}
		payload["author"] = author
	}
	return JSON(payload)
}

// Person describes a user profile.
func Person(name, url, bio string) template.JS {
	payload := map[string]any{
		"@context": "https://schema.org",
		"@type":    "Person",
		"name":     name,
		"url":      url,
	}
	if bio != "" {
		payload["description"] = bio
	}
	return JSON(payload)
}

// ProductOffer describes a purchasable plan.
func ProductOffer(name, url, description string, priceMinor int64, currency string) template.JS {
	price := float64(priceMinor) / 100
	if currency == "" {
		currency = "CNY"
	}
	payload := map[string]any{
		"@context":    "https://schema.org",
		"@type":       "Product",
		"name":        name,
		"url":         url,
		"description": description,
		"offers": map[string]any{
			"@type":         "Offer",
			"price":         price,
			"priceCurrency": currency,
			"availability":  "https://schema.org/InStock",
			"url":           url,
		},
	}
	return JSON(payload)
}

// Truncate shortens s to at most n runes and appends an ellipsis.
func Truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}
