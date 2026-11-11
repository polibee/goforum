package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

// sseChannel carries per-user notification pushes between instances so a
// browser connected to instance A still receives updates produced on B.
const sseChannel = "sse:notify"

// hub fans notification updates out to live SSE connections without polling.
type hub struct {
	mu      sync.RWMutex
	clients map[string]map[chan []byte]struct{}

	client *redis.Client
	log    *slog.Logger
	// instance identifies this process so it can ignore its own broadcasts.
	instance string
	cancel   context.CancelFunc
}

func newHub() *hub {
	return &hub{clients: make(map[string]map[chan []byte]struct{})}
}

// startRedis attaches the hub to Redis: local publishes are mirrored, and
// remote pushes are delivered to locally connected clients.
func (h *hub) startRedis(ctx context.Context, client *redis.Client, instance string, log *slog.Logger) error {
	if client == nil {
		return fmt.Errorf("sse hub requires a redis client")
	}
	subCtx, cancel := context.WithCancel(ctx)
	h.client = client
	h.log = log
	h.instance = instance
	h.cancel = cancel

	pubsub := client.Subscribe(subCtx, sseChannel)
	if _, err := pubsub.Receive(subCtx); err != nil {
		cancel()
		_ = pubsub.Close()
		return fmt.Errorf("sse subscribe failed: %w", err)
	}
	go h.consume(subCtx, pubsub)
	return nil
}

func (h *hub) consume(ctx context.Context, pubsub *redis.PubSub) {
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
			var env sseEnvelope
			if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
				h.log.Warn("sse payload decode failed", "error", err)
				continue
			}
			if env.Origin == h.instance {
				continue // already delivered locally when published
			}
			h.deliver(env.UserID, env.Payload)
		}
	}
}

// deliver writes to the in-process subscribers for one user.
func (h *hub) deliver(userID string, payload []byte) {
	h.mu.RLock()
	set := h.clients[userID]
	channels := make([]chan []byte, 0, len(set))
	for ch := range set {
		channels = append(channels, ch)
	}
	h.mu.RUnlock()
	for _, ch := range channels {
		select {
		case ch <- payload:
		default:
			// Slow client: drop this update; the next one carries current state.
		}
	}
}

func (h *hub) subscribe(userID string) (chan []byte, func()) {
	ch := make(chan []byte, 4)
	h.mu.Lock()
	if h.clients[userID] == nil {
		h.clients[userID] = make(map[chan []byte]struct{})
	}
	h.clients[userID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if set := h.clients[userID]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(h.clients, userID)
			}
		}
		h.mu.Unlock()
		close(ch)
	}
}

// publish delivers to local connections and mirrors to sibling instances.
func (h *hub) publish(userID string, payload []byte) {
	h.deliver(userID, payload)
	if h.client == nil {
		return
	}
	raw, err := json.Marshal(sseEnvelope{UserID: userID, Payload: payload, Origin: h.instance})
	if err != nil {
		return
	}
	// Use a background context: a closed browser must not cancel the broadcast,
	// and this runs from SSE handlers where the request may already be done.
	if err := h.client.Publish(context.Background(), sseChannel, raw).Err(); err != nil {
		if h.log != nil {
			h.log.Warn("sse broadcast failed", "user_id", userID, "error", err)
		}
	}
}

// close stops the Redis subscriber.
func (h *hub) close() {
	if h.cancel != nil {
		h.cancel()
	}
}

// sseEnvelope is one cross-instance push.
type sseEnvelope struct {
	UserID  string `json:"user_id"`
	Payload []byte `json:"payload"`
	Origin  string `json:"origin"`
}
