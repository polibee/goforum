package events

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

// An event published on one bus must reach handlers registered on another bus
// sharing the same Redis, which is what makes a multi-instance deployment work.
func TestRedisBusCrossInstance(t *testing.T) {
	_, client := testRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	busA, err := NewRedisBus(ctx, NewMemoryBus(testLogger()), client, testLogger())
	if err != nil {
		t.Fatalf("bus A: %v", err)
	}
	defer busA.Close()
	busB, err := NewRedisBus(ctx, NewMemoryBus(testLogger()), client, testLogger())
	if err != nil {
		t.Fatalf("bus B: %v", err)
	}
	defer busB.Close()

	received := make(chan Event, 1)
	if err := busB.Subscribe("topic.replied", func(_ context.Context, e Event) error {
		received <- e
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	event := NewEvent("topic.replied", "topic", "t1", []byte(`{"n":1}`))
	if err := busA.Publish(ctx, event); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-received:
		if got.ID != event.ID {
			t.Fatalf("event id = %q, want %q", got.ID, event.ID)
		}
		if string(got.Payload) != `{"n":1}` {
			t.Fatalf("payload = %s", got.Payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event never reached the other instance")
	}
}

// Publishing must not deliver the same event twice on the publishing instance
// (local dispatch plus its own Redis echo).
func TestRedisBusNoDuplicateLocalDelivery(t *testing.T) {
	_, client := testRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus, err := NewRedisBus(ctx, NewMemoryBus(testLogger()), client, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	var count int
	done := make(chan struct{}, 2)
	if err := bus.Subscribe("ping", func(context.Context, Event) error {
		count++
		done <- struct{}{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := bus.Publish(ctx, NewEvent("ping", "x", "1", nil)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("local handler never ran")
	}
	// Give the echo a chance to arrive; it must be suppressed.
	select {
	case <-done:
		t.Fatalf("event dispatched %d times, want 1", count)
	case <-time.After(600 * time.Millisecond):
	}
}

// With no Redis client the wrapper is not used, but the memory bus must still
// dispatch locally (regression guard for the plain single-instance path).
func TestMemoryBusStillWorks(t *testing.T) {
	bus := NewMemoryBus(testLogger())
	got := make(chan Event, 1)
	_ = bus.Subscribe("evt", func(_ context.Context, e Event) error {
		got <- e
		return nil
	})
	ev := NewEvent("evt", "x", "1", []byte("p"))
	if err := bus.Publish(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		if e.ID != ev.ID {
			t.Fatal("wrong event")
		}
	case <-time.After(time.Second):
		t.Fatal("memory bus did not dispatch")
	}
}

func TestRedisBusRequiresClient(t *testing.T) {
	if _, err := NewRedisBus(context.Background(), NewMemoryBus(testLogger()), nil, testLogger()); err == nil {
		t.Fatal("nil client should error")
	}
}
