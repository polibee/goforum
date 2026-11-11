package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"goforum/internal/platform/id"
)

// redisChannel carries events between instances. One channel is enough: the
// event name travels inside the envelope.
const redisChannel = "events:broadcast"

// RediBus wraps a local bus and mirrors every published event to Redis so
// sibling instances dispatch it too. Handlers still run locally in-process,
// which keeps ordering and error handling identical to the memory bus.
type RediBus struct {
	local  Bus
	client *redis.Client
	log    *slog.Logger

	mu       sync.Mutex
	cancel   context.CancelFunc
	instance string
	// seen guards against dispatching our own broadcast twice (we publish, then
	// receive the echo) and against duplicate delivery.
	seen   map[string]time.Time
	seenMu sync.Mutex
}

// NewRedisBus connects a bus to Redis. The returned bus starts a background
// subscriber immediately; call Close to stop it.
func NewRedisBus(ctx context.Context, local Bus, client *redis.Client, log *slog.Logger) (*RediBus, error) {
	if client == nil {
		return nil, fmt.Errorf("redis bus requires a client")
	}
	b := &RediBus{
		local: local, client: client, log: log,
		seen: make(map[string]time.Time), instance: id.New(),
	}
	subCtx, cancel := context.WithCancel(ctx)
	b.cancel = cancel

	// Verify connectivity before reporting success so a misconfiguration fails
	// at startup instead of silently dropping cross-instance events.
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		cancel()
		return nil, fmt.Errorf("redis bus unreachable: %w", err)
	}

	pubsub := client.Subscribe(subCtx, redisChannel)
	if _, err := pubsub.Receive(subCtx); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, fmt.Errorf("redis subscribe failed: %w", err)
	}
	go b.consume(subCtx, pubsub)
	go b.pruneSeen(subCtx)
	return b, nil
}

// consume dispatches events broadcast by other instances.
func (b *RediBus) consume(ctx context.Context, pubsub *redis.PubSub) {
	defer pubsub.Close()
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var env eventEnvelope
			if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
				b.log.Warn("redis event decode failed", "error", err)
				continue
			}
			if b.markSeen(env.Event.ID) {
				continue // our own echo
			}
			b.dispatchLocal(ctx, env.Event)
		}
	}
}

// publishRemote mirrors an event to sibling instances.
func (b *RediBus) publishRemote(ctx context.Context, event Event) {
	raw, err := json.Marshal(eventEnvelope{Event: event, Origin: b.origin()})
	if err != nil {
		return
	}
	if err := b.client.Publish(ctx, redisChannel, raw).Err(); err != nil {
		b.log.Warn("redis event publish failed", "event", event.Name, "error", err)
	}
}

// origin is a stable per-process marker assigned at construction.
func (b *RediBus) origin() string { return b.instance }

// markSeen records an event ID and reports whether it was already handled.
func (b *RediBus) markSeen(eventID string) bool {
	if eventID == "" {
		return false
	}
	b.seenMu.Lock()
	defer b.seenMu.Unlock()
	if _, ok := b.seen[eventID]; ok {
		return true
	}
	b.seen[eventID] = time.Now()
	return false
}

// pruneSeen keeps the dedupe map small; a 30 minute window is far longer than
// the round trip for an echo.
func (b *RediBus) pruneSeen(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-30 * time.Minute)
			b.seenMu.Lock()
			for k, at := range b.seen {
				if at.Before(cutoff) {
					delete(b.seen, k)
				}
			}
			b.seenMu.Unlock()
		}
	}
}

func (b *RediBus) dispatchLocal(ctx context.Context, event Event) {
	if err := b.local.Publish(ctx, event); err != nil {
		b.log.Error("event handler failed", "event", event.Name, "error", err)
	}
}

// Subscribe registers a handler on the local dispatcher.
func (b *RediBus) Subscribe(name string, handler Handler) error {
	return b.local.Subscribe(name, handler)
}

// Publish runs local handlers first (so the acting instance stays
// synchronous), then broadcasts to siblings.
func (b *RediBus) Publish(ctx context.Context, event Event) error {
	b.markSeen(event.ID)
	err := b.local.Publish(ctx, event)
	b.publishRemote(ctx, event)
	return err
}

// Close stops the subscriber.
func (b *RediBus) Close() error {
	if b.cancel != nil {
		b.cancel()
	}
	return nil
}

// eventEnvelope carries the event plus an instance marker.
type eventEnvelope struct {
	Event  Event  `json:"event"`
	Origin string `json:"origin"`
}

// Ensure RediBus satisfies the Bus contract.
var _ Bus = (*RediBus)(nil)
