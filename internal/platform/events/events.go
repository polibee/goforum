package events

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"goforum/internal/platform/id"
)

type Event struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	AggregateType string    `json:"aggregate_type"`
	AggregateID   string    `json:"aggregate_id"`
	Payload       []byte    `json:"payload"`
	OccurredAt    time.Time `json:"occurred_at"`
}

type Handler func(ctx context.Context, event Event) error

type Bus interface {
	Publish(ctx context.Context, event Event) error
	Subscribe(name string, handler Handler) error
}

// NewEvent builds an event with an ID and UTC timestamp.
func NewEvent(name, aggregateType, aggregateID string, payload []byte) Event {
	return Event{
		ID:            id.New(),
		Name:          name,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		Payload:       payload,
		OccurredAt:    time.Now().UTC(),
	}
}

// MemoryBus is the default bus. It dispatches synchronously and recovers handler panics.
// Production replaces it with an outbox-backed dispatcher (see docs/02).
type MemoryBus struct {
	mu   sync.RWMutex
	subs map[string][]Handler
	log  *slog.Logger
}

func NewMemoryBus(log *slog.Logger) *MemoryBus {
	return &MemoryBus{subs: make(map[string][]Handler), log: log}
}

func (b *MemoryBus) Subscribe(name string, handler Handler) error {
	b.mu.Lock()
	b.subs[name] = append(b.subs[name], handler)
	b.mu.Unlock()
	return nil
}

func (b *MemoryBus) Publish(ctx context.Context, event Event) error {
	b.mu.RLock()
	handlers := append([]Handler(nil), b.subs[event.Name]...)
	b.mu.RUnlock()

	for _, h := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					b.log.Error("event handler panic", "event", event.Name, "panic", r)
				}
			}()
			if err := h(ctx, event); err != nil {
				b.log.Error("event handler failed", "event", event.Name, "error", err)
			}
		}()
	}
	return nil
}
