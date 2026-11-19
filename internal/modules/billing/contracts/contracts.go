// Package contracts is the only billing surface other modules may import.
package contracts

import "context"

// Channel is the safe, read-only view of a payment channel.
type Channel struct {
	ID     string
	Name   string
	Driver string
}

// Checkout creates a paid order for a non-product purchase (currently ads).
// It returns the trade number, optional redirect URL and whether the order was
// settled immediately.
type Checkout interface {
	ListEnabledChannels(ctx context.Context) ([]Channel, error)
	CreateAdOrder(ctx context.Context, userID, adID, channelID, subject string, amountMinor int64, currency, clientIP string) (string, string, bool, error)
}
