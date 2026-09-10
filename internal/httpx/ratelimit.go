package httpx

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
)

// RateLimiter enforces a fixed number of requests per minute per caller.
//
// Emarsys documents no header names for this, so the mock emits the widely used
// X-RateLimit-* set plus Retry-After. What matters for a client is the 429 and
// the recovery, not the exact spelling -- and lowering the limit in CI is how a
// retry and backoff path gets exercised without sending a thousand requests.
type RateLimiter struct {
	limit int
	mu    sync.Mutex
	// windows maps a caller to its current fixed window.
	windows map[string]*window
	now     func() time.Time
}

type window struct {
	count   int
	resetAt time.Time
}

func NewRateLimiter(limit int) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		windows: map[string]*window{},
		now:     time.Now,
	}
}

// Allow records a request and reports whether it fits in the caller's window.
func (rl *RateLimiter) Allow(key string) (allowed bool, remaining int, resetAt time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	w, ok := rl.windows[key]
	if !ok || now.After(w.resetAt) {
		w = &window{resetAt: now.Add(time.Minute)}
		rl.windows[key] = w

		// The map only grows if callers keep changing, which in a mock means a
		// test suite churning credentials. Sweeping expired windows on rollover
		// keeps that bounded without a background goroutine.
		if len(rl.windows) > 1024 {
			for k, v := range rl.windows {
				if now.After(v.resetAt) {
					delete(rl.windows, k)
				}
			}
		}
	}

	w.count++
	remaining = rl.limit - w.count
	if remaining < 0 {
		remaining = 0
	}
	return w.count <= rl.limit, remaining, w.resetAt
}

// Middleware applies the limit to requests whose path passes the guard.
func (rl *RateLimiter) Middleware(keyOf func(*http.Request) string, applies func(string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rl == nil || rl.limit <= 0 || !applies(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			allowed, remaining, resetAt := rl.Allow(keyOf(r))
			header := w.Header()
			header.Set("X-RateLimit-Limit", strconv.Itoa(rl.limit))
			header.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			header.Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))

			if !allowed {
				retryAfter := int(time.Until(resetAt).Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				header.Set("Retry-After", strconv.Itoa(retryAfter))
				api.ErrorStatus(w, http.StatusTooManyRequests, api.CodeInternalError,
					"Rate limit exceeded: "+strconv.Itoa(rl.limit)+" requests per minute")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
