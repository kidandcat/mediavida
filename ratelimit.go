package main

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter implements a per-clientID token-bucket rate limiter without any
// external dependency. Each client gets its own bucket that refills lazily based
// on elapsed wall-clock time (no background goroutine). The map of buckets mirrors
// the RWMutex-guarded style of SessionStore in session.go.
//
// Defaults (see NewRateLimiter callers): ~5 req/s sustained, burst 20 — enough
// for interactive bursts (loading a thread, batch fetches) while protecting the
// upstream MV scraper from a single client hammering the API.
type RateLimiter struct {
	rate  float64 // tokens added per second
	burst float64 // maximum tokens a bucket can hold

	mu      sync.RWMutex
	buckets map[string]*bucket // clientID → bucket
}

// bucket is a single client's token reservoir. tokens is the current credit;
// last is the timestamp of the most recent refill computation.
type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter builds a limiter allowing ratePerSec sustained requests per
// client with the given burst capacity.
func NewRateLimiter(ratePerSec float64, burst int) *RateLimiter {
	return &RateLimiter{
		rate:    ratePerSec,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
	}
}

// allow consumes one token for clientID, returning whether the request is within
// budget and, if not, the suggested Retry-After duration until the next token is
// available.
func (rl *RateLimiter) allow(clientID string) (bool, time.Duration) {
	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b := rl.buckets[clientID]
	if b == nil {
		// NOTE: buckets are never evicted. For the current client count (a handful
		// of API tokens + per-device app logins) an unbounded map is fine. If the
		// token space ever grows unbounded, add lazy cleanup of idle buckets here.
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[clientID] = b
	}

	// Refill by elapsed time since the last computation, capped at burst.
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * rl.rate
		if b.tokens > rl.burst {
			b.tokens = rl.burst
		}
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	// Time until one full token is available again.
	if rl.rate <= 0 {
		return false, time.Second
	}
	wait := time.Duration((1 - b.tokens) / rl.rate * float64(time.Second))
	return false, wait
}

// Middleware wraps next with per-client rate limiting. It must sit AFTER auth so
// the clientID is present in the request context. Requests over budget get a 429
// with a Retry-After header. The SSE stream (/events) is exempt because it is a
// single long-lived connection, not a per-request operation; requests with no
// clientID are passed through untouched (auth will reject them elsewhere).
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exempt the long-lived SSE stream — it is one connection, not a request.
		if r.URL.Path == "/events" {
			next.ServeHTTP(w, r)
			return
		}

		clientID := ClientIDFromContext(r.Context())
		if clientID == "" {
			next.ServeHTTP(w, r)
			return
		}

		if ok, retryAfter := rl.allow(clientID); !ok {
			secs := int(retryAfter.Seconds())
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}
