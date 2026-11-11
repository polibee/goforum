// Package metrics exposes a small Prometheus-compatible metrics registry.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type requestKey struct {
	method string
	route  string
	status int
}

type Registry struct {
	mu        sync.Mutex
	requests  map[requestKey]uint64
	duration  map[requestKey]float64
	startedAt time.Time
}

func New() *Registry {
	return &Registry{
		requests:  make(map[requestKey]uint64),
		duration:  make(map[requestKey]float64),
		startedAt: time.Now(),
	}
}

func (r *Registry) Observe(method, route string, status int, d time.Duration) {
	key := requestKey{method: method, route: route, status: status}
	r.mu.Lock()
	r.requests[key]++
	r.duration[key] += d.Seconds()
	r.mu.Unlock()
}

// Middleware records request counts and latency by route pattern.
func Middleware(r *Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		if r == nil {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		if path == "/metrics" || strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/uploads/") {
			c.Next()
			return
		}
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		r.Observe(c.Request.Method, route, c.Writer.Status(), time.Since(start))
	}
}

// Handler renders metrics in Prometheus text exposition format.
func (r *Registry) Handler(statsFn func() DBStats, version string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var b strings.Builder
		b.WriteString("# HELP goforum_http_requests_total HTTP requests by route and status.\n")
		b.WriteString("# TYPE goforum_http_requests_total counter\n")
		r.mu.Lock()
		keys := make([]requestKey, 0, len(r.requests))
		for k := range r.requests {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if a.method != b.method {
				return a.method < b.method
			}
			if a.route != b.route {
				return a.route < b.route
			}
			return a.status < b.status
		})
		for _, k := range keys {
			fmt.Fprintf(&b, "goforum_http_requests_total{method=%q,route=%q,status=%q} %d\n",
				k.method, k.route, strconv.Itoa(k.status), r.requests[k])
		}
		b.WriteString("# HELP goforum_http_request_duration_seconds_sum Total request latency.\n")
		b.WriteString("# TYPE goforum_http_request_duration_seconds_sum counter\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "goforum_http_request_duration_seconds_sum{method=%q,route=%q,status=%q} %.6f\n",
				k.method, k.route, strconv.Itoa(k.status), r.duration[k])
		}
		b.WriteString("# HELP goforum_http_request_duration_seconds_count Request latency sample count.\n")
		b.WriteString("# TYPE goforum_http_request_duration_seconds_count counter\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "goforum_http_request_duration_seconds_count{method=%q,route=%q,status=%q} %d\n",
				k.method, k.route, strconv.Itoa(k.status), r.requests[k])
		}
		r.mu.Unlock()

		fmt.Fprintf(&b, "# HELP goforum_uptime_seconds Process uptime.\n# TYPE goforum_uptime_seconds gauge\ngoforum_uptime_seconds %.0f\n",
			time.Since(r.startedAt).Seconds())
		fmt.Fprintf(&b, "# HELP goforum_build_info Build information.\n# TYPE goforum_build_info gauge\ngoforum_build_info{version=%q} 1\n", version)

		if statsFn != nil {
			stats := statsFn()
			fmt.Fprintf(&b, "# HELP goforum_db_open_connections Open database connections.\n# TYPE goforum_db_open_connections gauge\ngoforum_db_open_connections %d\n", stats.OpenConnections)
			fmt.Fprintf(&b, "# HELP goforum_db_in_use_connections In-use database connections.\n# TYPE goforum_db_in_use_connections gauge\ngoforum_db_in_use_connections %d\n", stats.InUse)
			fmt.Fprintf(&b, "# HELP goforum_db_wait_count Total waits for a database connection.\n# TYPE goforum_db_wait_count counter\ngoforum_db_wait_count %d\n", stats.WaitCount)
		}

		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.String(http.StatusOK, b.String())
	}
}

// DBStats mirrors the fields used from database/sql.DBStats without importing it.
type DBStats struct {
	OpenConnections int
	InUse           int
	WaitCount       int64
}
