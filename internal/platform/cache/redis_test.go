package cache

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// startRedis spins up an in-process Redis so the Redis driver can be exercised
// in CI without an external service.
func startRedis(t *testing.T) (*miniredis.Miniredis, *Redis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := NewRedis(context.Background(), "redis://"+mr.Addr(), "test:", time.Minute)
	if err != nil {
		t.Fatalf("connect redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return mr, c
}

// The Redis driver must satisfy the same contract as the memory driver so the
// two can be swapped by configuration alone.
func TestRedisContract(t *testing.T) {
	_, c := startRedis(t)
	contract(t, c)
}

func TestRedisPrefixIsolation(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()
	a, err := NewRedis(ctx, "redis://"+mr.Addr(), "site-a:", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewRedis(ctx, "redis://"+mr.Addr(), "site-b:", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer b.Close()

	_ = a.Set(ctx, "shared", []byte("a"), time.Minute)
	if _, ok := b.Get(ctx, "shared"); ok {
		t.Fatal("prefix must isolate keys between deployments")
	}
	_ = b.Set(ctx, "shared", []byte("b"), time.Minute)
	if v, _ := a.Get(ctx, "shared"); string(v) != "a" {
		t.Fatalf("site-a value overwritten: %q", v)
	}
}

// Incr must be atomic across separate clients, which is what makes distributed
// rate limiting correct.
func TestRedisIncrAcrossClients(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()
	const clients = 5
	const perClient = 20

	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := NewRedis(ctx, "redis://"+mr.Addr(), "shared:", time.Minute)
			if err != nil {
				t.Errorf("connect: %v", err)
				return
			}
			defer c.Close()
			for j := 0; j < perClient; j++ {
				if _, err := c.Incr(ctx, "hits", 1, time.Minute); err != nil {
					t.Errorf("incr: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	c, err := NewRedis(ctx, "redis://"+mr.Addr(), "shared:", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	n, err := c.Incr(ctx, "hits", 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(clients * perClient); n != want {
		t.Fatalf("counter = %d, want %d", n, want)
	}
}

// Add is used as a distributed lock: exactly one caller may win.
func TestRedisAddIsLock(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()
	const racers = 10

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := NewRedis(ctx, "redis://"+mr.Addr(), "lock:", time.Minute)
			if err != nil {
				t.Errorf("connect: %v", err)
				return
			}
			defer c.Close()
			ok, err := c.Add(ctx, "job", []byte("1"), time.Minute)
			if err != nil {
				t.Errorf("add: %v", err)
				return
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("lock winners = %d, want 1", wins)
	}
}

func TestRedisExpiry(t *testing.T) {
	mr, c := startRedis(t)
	ctx := context.Background()
	_ = c.Set(ctx, "short", []byte("v"), time.Minute)
	if _, ok := c.Get(ctx, "short"); !ok {
		t.Fatal("value should exist")
	}
	mr.FastForward(2 * time.Minute)
	if _, ok := c.Get(ctx, "short"); ok {
		t.Fatal("value should expire")
	}
}

func TestRedisUnreachable(t *testing.T) {
	// Port 1 is never a Redis server; NewRedis must fail rather than hang.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if _, err := NewRedis(ctx, "redis://127.0.0.1:1", "", time.Minute); err == nil {
		t.Fatal("unreachable redis should error")
	}
}
