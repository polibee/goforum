package cache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis backs the cache with a shared server so every instance sees the same
// counters, rate limits and locks.
type Redis struct {
	client     *redis.Client
	prefix     string
	defaultTTL time.Duration
}

// NewRedis connects to url (redis://user:pass@host:port/db) and verifies the
// connection. prefix namespaces every key so one Redis can serve several
// deployments.
func NewRedis(ctx context.Context, url, prefix string, defaultTTL time.Duration) (*Redis, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("invalid redis url: %w", err)
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 3 * time.Second
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = 3 * time.Second
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = 3 * time.Second
	}
	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis unreachable: %w", err)
	}
	if defaultTTL <= 0 {
		defaultTTL = 5 * time.Minute
	}
	if prefix != "" && !strings.HasSuffix(prefix, ":") {
		prefix += ":"
	}
	return &Redis{client: client, prefix: prefix, defaultTTL: defaultTTL}, nil
}

func (r *Redis) Driver() string { return "redis" }

// RedisClient returns the shared client for the event bus and SSE hub.
func (r *Redis) RedisClient() *redis.Client { return r.client }

func (r *Redis) Close() error { return r.client.Close() }

func (r *Redis) key(k string) string { return r.prefix + k }

func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool) {
	val, err := r.client.Get(ctx, r.key(key)).Bytes()
	if err != nil {
		return nil, false
	}
	return val, true
}

func (r *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = r.defaultTTL
	}
	if err := r.client.Set(ctx, r.key(key), value, ttl).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (r *Redis) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, r.key(key)).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// Incr uses Redis INCRBY so the counter is atomic across instances. The TTL is
// only applied when the key is created, so a window does not slide per call.
func (r *Redis) Incr(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	full := r.key(key)
	pipe := r.client.TxPipeline()
	incr := pipe.IncrBy(ctx, full, delta)
	if ttl > 0 {
		pipe.ExpireNX(ctx, full, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return incr.Val(), nil
}

func (r *Redis) Expire(ctx context.Context, key string, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	if err := r.client.Expire(ctx, r.key(key), ttl).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// Add maps to SET NX, which is a single atomic operation in Redis and is
// therefore safe as a distributed lock.
func (r *Redis) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = r.defaultTTL
	}
	ok, err := r.client.SetNX(ctx, r.key(key), value, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return ok, nil
}

// DeletePrefix walks keys with SCAN rather than KEYS so a large keyspace does
// not block Redis.
func (r *Redis) DeletePrefix(ctx context.Context, prefix string) error {
	pattern := r.key(prefix) + "*"
	var cursor uint64
	for {
		keys, next, err := r.client.Scan(ctx, cursor, pattern, 500).Result()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		if len(keys) > 0 {
			if err := r.client.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("%w: %v", ErrUnavailable, err)
			}
		}
		if next == 0 {
			return nil
		}
		cursor = next
	}
}

// Stats reports cache usage for the admin system page.
func (r *Redis) Stats(ctx context.Context) (keys int64, memory string, ok bool) {
	info, err := r.client.Info(ctx, "keyspace").Result()
	if err != nil {
		return 0, "", false
	}
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "db") {
			continue
		}
		// db0:keys=12,expires=3,avg_ttl=0
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		for _, field := range strings.Split(parts[1], ",") {
			if strings.HasPrefix(field, "keys=") {
				if n, err := parseInt64(strings.TrimPrefix(field, "keys=")); err == nil {
					keys += n
				}
			}
		}
	}
	return keys, "", true
}

func parseInt64(s string) (int64, error) {
	var n int64
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}

// Open builds a cache from the configured driver. Empty or "memory" yields the
// process-local cache; "redis" requires a reachable server.
func Open(ctx context.Context, driver, redisURL, prefix string, defaultTTL time.Duration) (Cache, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "", "memory":
		return NewMemory(defaultTTL), nil
	case "redis":
		if strings.TrimSpace(redisURL) == "" {
			return nil, errors.New("cache driver is redis but REDIS_URL is empty")
		}
		return NewRedis(ctx, redisURL, prefix, defaultTTL)
	default:
		return nil, fmt.Errorf("unknown cache driver %q (want memory|redis)", driver)
	}
}
