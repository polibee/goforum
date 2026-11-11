package cache

import (
	"context"
	"sync"
	"testing"
	"time"
)

// contract asserts the behaviours the rest of the app depends on, so memory
// and redis drivers can be swapped without surprises.
func contract(t *testing.T, c Cache) {
	t.Helper()
	ctx := context.Background()

	if _, ok := c.Get(ctx, "missing"); ok {
		t.Fatal("missing key should not be present")
	}
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	if v, ok := c.Get(ctx, "k"); !ok || string(v) != "v" {
		t.Fatalf("get = %q,%v", v, ok)
	}
	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := c.Get(ctx, "k"); ok {
		t.Fatal("deleted key still present")
	}

	// Incr starts from zero and accumulates.
	n, err := c.Incr(ctx, "counter", 1, time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("first incr = %d,%v", n, err)
	}
	n, err = c.Incr(ctx, "counter", 5, time.Minute)
	if err != nil || n != 6 {
		t.Fatalf("second incr = %d,%v", n, err)
	}

	// Add is set-if-absent, used as a cross-instance lock.
	ok, err := c.Add(ctx, "lock", []byte("1"), time.Minute)
	if err != nil || !ok {
		t.Fatalf("first add = %v,%v", ok, err)
	}
	ok, err = c.Add(ctx, "lock", []byte("1"), time.Minute)
	if err != nil || ok {
		t.Fatalf("second add = %v,%v (want false)", ok, err)
	}

	// DeletePrefix removes only the matching family.
	_ = c.Set(ctx, "fam:a", []byte("1"), time.Minute)
	_ = c.Set(ctx, "fam:b", []byte("1"), time.Minute)
	_ = c.Set(ctx, "other", []byte("1"), time.Minute)
	if err := c.DeletePrefix(ctx, "fam:"); err != nil {
		t.Fatalf("delete prefix: %v", err)
	}
	for _, k := range []string{"fam:a", "fam:b"} {
		if _, ok := c.Get(ctx, k); ok {
			t.Fatalf("%s should be deleted", k)
		}
	}
	if _, ok := c.Get(ctx, "other"); !ok {
		t.Fatal("unrelated key was deleted")
	}
}

func TestMemoryContract(t *testing.T) {
	contract(t, NewMemory(time.Minute))
}

func TestMemoryIncrIsAtomic(t *testing.T) {
	c := NewMemory(time.Minute)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Incr(ctx, "hot", 1, time.Minute)
		}()
	}
	wg.Wait()
	n, err := c.Incr(ctx, "hot", 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Fatalf("counter = %d, want 50", n)
	}
}

// A non-numeric value must not be parsed as a large negative number (the old
// implementation walked bytes as digits).
func TestMemoryIncrOnNonNumeric(t *testing.T) {
	c := NewMemory(time.Minute)
	ctx := context.Background()
	_ = c.Set(ctx, "weird", []byte("not-a-number"), time.Minute)
	n, err := c.Incr(ctx, "weird", 3, time.Minute)
	if err != nil || n != 3 {
		t.Fatalf("incr on non-numeric = %d,%v (want 3)", n, err)
	}
}

func TestMemoryExpiry(t *testing.T) {
	c := NewMemory(time.Minute)
	ctx := context.Background()
	_ = c.Set(ctx, "short", []byte("v"), 10*time.Millisecond)
	if _, ok := c.Get(ctx, "short"); !ok {
		t.Fatal("value should exist before expiry")
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok := c.Get(ctx, "short"); ok {
		t.Fatal("value should expire")
	}
}

func TestOpenUnknownDriver(t *testing.T) {
	if _, err := Open(context.Background(), "memcached", "", "", time.Minute); err == nil {
		t.Fatal("unknown driver should error")
	}
}

func TestOpenMemoryDefault(t *testing.T) {
	for _, driver := range []string{"", "memory", "MEMORY"} {
		c, err := Open(context.Background(), driver, "", "", time.Minute)
		if err != nil {
			t.Fatalf("driver %q: %v", driver, err)
		}
		if c.Driver() != "memory" {
			t.Fatalf("driver %q resolved to %q", driver, c.Driver())
		}
	}
}

func TestOpenRedisWithoutURL(t *testing.T) {
	if _, err := Open(context.Background(), "redis", "", "", time.Minute); err == nil {
		t.Fatal("redis without URL should error")
	}
}
