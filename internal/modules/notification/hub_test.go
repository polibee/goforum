package notification

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func hubLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func hubRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// A push produced on one instance must reach a browser connected to another.
func TestSSECrossInstance(t *testing.T) {
	client := hubRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hubA := newHub()
	if err := hubA.startRedis(ctx, client, "instance-a", hubLogger()); err != nil {
		t.Fatalf("hub A: %v", err)
	}
	defer hubA.close()

	hubB := newHub()
	if err := hubB.startRedis(ctx, client, "instance-b", hubLogger()); err != nil {
		t.Fatalf("hub B: %v", err)
	}
	defer hubB.close()

	// Browser is connected to instance B.
	ch, unsubscribe := hubB.subscribe("user-1")
	defer unsubscribe()

	// Notification is produced on instance A.
	hubA.publish("user-1", []byte(`{"unread":3}`))

	select {
	case got := <-ch:
		if string(got) != `{"unread":3}` {
			t.Fatalf("payload = %s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("push never reached the client on the other instance")
	}
}

// Local subscribers must receive a push exactly once, not again via the echo.
func TestSSENoDuplicateLocalPush(t *testing.T) {
	client := hubRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHub()
	if err := h.startRedis(ctx, client, "solo", hubLogger()); err != nil {
		t.Fatal(err)
	}
	defer h.close()

	ch, unsubscribe := h.subscribe("user-2")
	defer unsubscribe()

	h.publish("user-2", []byte("one"))

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("local push missing")
	}
	select {
	case extra := <-ch:
		t.Fatalf("duplicate push: %s", extra)
	case <-time.After(600 * time.Millisecond):
	}
}

// Pushes for other users must not leak across users on the same instance.
func TestSSEUserIsolation(t *testing.T) {
	h := newHub()
	chA, unsubA := h.subscribe("alice")
	defer unsubA()
	chB, unsubB := h.subscribe("bob")
	defer unsubB()

	h.publish("alice", []byte("for-alice"))

	select {
	case got := <-chA:
		if string(got) != "for-alice" {
			t.Fatalf("alice got %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("alice did not receive her push")
	}
	select {
	case got := <-chB:
		t.Fatalf("bob received alice's push: %s", got)
	case <-time.After(300 * time.Millisecond):
	}
}

// Without Redis the hub still works locally (default single-instance mode).
func TestSSELocalOnly(t *testing.T) {
	h := newHub()
	ch, unsubscribe := h.subscribe("u3")
	defer unsubscribe()
	h.publish("u3", []byte("local"))
	select {
	case got := <-ch:
		if string(got) != "local" {
			t.Fatalf("got %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("local push missing")
	}
}

func TestSSERedisRequiresClient(t *testing.T) {
	h := newHub()
	if err := h.startRedis(context.Background(), nil, "x", hubLogger()); err == nil {
		t.Fatal("nil client should error")
	}
}
