package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterRefillsAfterBurst(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(limit{rate: 1, burst: 2})
	l.now = func() time.Time { return now }
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("first request refused")
	}
	l.allow("a")
	ok, wait := l.allow("a")
	if ok || wait != time.Second {
		t.Fatalf("third request: ok=%v wait=%v, want refused for 1s", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("another client was refused")
	}
	now = now.Add(time.Second)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("not refilled after a second")
	}
}

func TestSignInIsLimitedPerClientBehindNginx(t *testing.T) {
	mux := http.NewServeMux()
	routes{}.RegisterRoutes(mux)
	h := cors("https://web.test", rateLimit(mux)) // as New wires it
	login := func(realIP string) *http.Response {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/login", nil)
		req.RemoteAddr = "172.18.0.1:5000" // Docker bridge, as nginx reaches the API
		req.Header.Set("X-Real-IP", realIP)
		req.Header.Set("Origin", "https://web.test")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}
	for i := 0; i < int(authLimit.burst); i++ {
		if res := login("203.0.113.7"); res.StatusCode != http.StatusNoContent {
			t.Fatalf("attempt %d: status %d", i+1, res.StatusCode)
		}
	}
	res := login("203.0.113.7")
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("over the limit: status %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if res := login("198.51.100.9"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("a different student was blocked: %d", res.StatusCode)
	}
}

func TestPublicClientCannotSpoofItsIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1", nil)
	req.RemoteAddr = "203.0.113.7:5000"
	req.Header.Set("X-Real-IP", "1.2.3.4")
	if got := requestIP(req); got != "203.0.113.7" {
		t.Fatalf("requestIP = %q, want the socket address", got)
	}
}

type routes struct{}

func (routes) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/admin/auth/login", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
}
