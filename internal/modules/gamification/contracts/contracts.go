package contracts

import "context"

// Points is implemented by gamification and consumed by billing.
type Points interface {
	CreditExternal(ctx context.Context, userID string, amount int64, refType, refID, description string) error
	SpendExternal(ctx context.Context, userID string, amount int64, refType, refID, description string) error
}
