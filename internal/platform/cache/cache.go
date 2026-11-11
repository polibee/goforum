// Package cache provides the shared cache used for counters, settings and
// rate limiting. The memory driver is process-local; the redis driver shares
// state across instances so a horizontally scaled deployment behaves the same.
package cache

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache is the minimal contract the rest of the app relies on. Every method
// must be safe for concurrent use.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error

	// Incr atomically adds delta and returns the new value. It creates the key
	// when missing. Callers use it for counters and rate limiting, so it has
	// to be atomic across processes when backed by Redis.
	Incr(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error)

	// Expire resets the TTL of an existing key.
	Expire(ctx context.Context, key string, ttl time.Duration) error

	// Add stores value only when the key is absent. It returns false when the
	// key already existed, which makes it usable as a cross-instance lock.
	Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)

	// DeletePrefix removes every key starting with prefix. It is used to
	// invalidate families of cached values (for example all topic lists).
	DeletePrefix(ctx context.Context, prefix string) error

	// RedisClient exposes the underlying client so other subsystems (event bus,
	// SSE) can share the same connection pool. It returns nil for memory.
	RedisClient() *redis.Client

	// Driver reports the backend name for the admin system page.
	Driver() string

	// Close releases connections.
	Close() error
}

// ErrUnavailable is returned when the backend cannot be reached.
var ErrUnavailable = errors.New("cache unavailable")

// entry is one memory-cache value.
type entry struct {
	value   []byte
	expires time.Time
}

// Memory is a process-local cache. It is the default driver and needs no
// external service; it does not share state between instances.
type Memory struct {
	mu         sync.RWMutex
	items      map[string]entry
	defaultTTL time.Duration
}

// NewMemory builds a memory cache with a fallback TTL for Set calls that pass 0.
func NewMemory(defaultTTL time.Duration) *Memory {
	if defaultTTL <= 0 {
		defaultTTL = 5 * time.Minute
	}
	return &Memory{items: make(map[string]entry), defaultTTL: defaultTTL}
}

func (m *Memory) Driver() string { return "memory" }

// RedisClient is nil for the memory driver.
func (m *Memory) RedisClient() *redis.Client { return nil }

func (m *Memory) Close() error { return nil }

func (m *Memory) Get(_ context.Context, key string) ([]byte, bool) {
	m.mu.RLock()
	e, ok := m.items[key]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if !e.expires.IsZero() && time.Now().After(e.expires) {
		m.mu.Lock()
		delete(m.items, key)
		m.mu.Unlock()
		return nil, false
	}
	return e.value, true
}

func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl == 0 {
		ttl = m.defaultTTL
	}
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.mu.Lock()
	m.items[key] = entry{value: value, expires: exp}
	m.mu.Unlock()
	return nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.items, key)
	m.mu.Unlock()
	return nil
}

// Incr adds delta to a decimal counter. Non-numeric values are replaced by the
// delta rather than silently treated as zero, which previously corrupted keys
// that did not hold numbers.
func (m *Memory) Incr(_ context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.items[key]
	if ok && !e.expires.IsZero() && time.Now().After(e.expires) {
		delete(m.items, key)
		ok = false
	}
	var n int64
	if ok {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(string(e.value)), 10, 64); err == nil {
			n = parsed
		}
	}
	n += delta

	exp := e.expires
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	} else if exp.IsZero() {
		exp = time.Now().Add(m.defaultTTL)
	}
	m.items[key] = entry{value: []byte(strconv.FormatInt(n, 10)), expires: exp}
	return n, nil
}

func (m *Memory) Expire(_ context.Context, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		return nil
	}
	if ttl > 0 {
		e.expires = time.Now().Add(ttl)
	} else {
		e.expires = time.Time{}
	}
	m.items[key] = e
	return nil
}

// Add implements set-if-absent under the same lock as the rest of the map.
func (m *Memory) Add(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.items[key]; ok {
		if e.expires.IsZero() || time.Now().Before(e.expires) {
			return false, nil
		}
	}
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.items[key] = entry{value: value, expires: exp}
	return true, nil
}

func (m *Memory) DeletePrefix(_ context.Context, prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.items {
		if strings.HasPrefix(k, prefix) {
			delete(m.items, k)
		}
	}
	return nil
}

// Len reports the number of live entries; the admin system page uses it.
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.items)
}
