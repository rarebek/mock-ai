package httpserver

import (
	"encoding/json"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// limit is a token bucket: rate tokens per second, up to burst.
type limit struct {
	rate  float64
	burst float64
}

var (
	// Sign-in routes are what a password or token guesser hits.
	authLimit = limit{rate: 10.0 / 60, burst: 10}
	// Everything else under /api/. Generous because a learning centre's
	// classroom can share one public IP.
	apiLimit = limit{rate: 30, burst: 200}
)

type bucket struct {
	tokens float64
	seen   time.Time
}

// shortcut: buckets live in this process only, so limits are per API instance; move to Redis if the API runs more than one replica.
type limiter struct {
	limit
	mu      sync.Mutex
	buckets map[string]*bucket
	sweep   time.Time
	now     func() time.Time
}

func newLimiter(l limit) *limiter {
	return &limiter{limit: l, buckets: map[string]*bucket{}, now: time.Now}
}

// allow takes a token for key, or says how long until one is free.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.sweep) > time.Minute {
		full := time.Duration(l.burst / l.rate * float64(time.Second))
		for k, b := range l.buckets {
			if now.Sub(b.seen) > full {
				delete(l.buckets, k) // refilled anyway; forget it
			}
		}
		l.sweep = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, seen: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.seen).Seconds()*l.rate)
	b.seen = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

// rateLimit throttles every /api/ request per client IP, with a tighter
// bucket for the student and admin sign-in routes.
func rateLimit(next http.Handler) http.Handler {
	auth, api := newLimiter(authLimit), newLimiter(apiLimit)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		l := api
		if strings.Contains(r.URL.Path, "/auth/") && !strings.HasSuffix(r.URL.Path, "/logout") {
			l = auth
		}
		if ok, wait := l.allow(requestIP(r)); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
				"code": "rate_limited", "message": "Too many requests. Please wait a moment and try again.",
			}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestIP is the client address. X-Real-IP is set by our nginx and is
// trusted only when the connection itself comes from nginx or the Docker
// bridge, never from the open internet.
func requestIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if real := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); real != nil {
			return real.String()
		}
	}
	return host
}
