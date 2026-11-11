package contracts

import "context"

type MessageMeta struct {
	ID             string
	ConversationID string
	SenderID       string
	SenderName     string
	Preview        string
}

// Reader is the read surface used by moderation.
type Reader interface {
	MessageMeta(ctx context.Context, messageID string) (*MessageMeta, error)
}

// Reporter is implemented by moderation and consumed by messaging UI.
type Reporter interface {
	Flag(ctx context.Context, reporterID, targetType, targetID, reasonCode, reasonText string) error
}
