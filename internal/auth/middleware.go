package auth

import (
	"context"
	"net/http"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

type tenantKey struct{}

// TenantFromContext returns the authenticated tenant id set by Middleware.
func TenantFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(tenantKey{}).(string)
	return id, ok
}

// Middleware requires a valid "Authorization: Bearer tsk_..." API key on
// every request, injecting the resolved tenant id into the request
// context. Also rate-limits per key: not about fairness between tenants,
// but about a leaked key not being able to enqueue unbounded work.
func Middleware(db *pgxpool.Pool) func(http.Handler) http.Handler {
	limiter := newKeyLimiter()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := bearerToken(r)
			if rawKey == "" {
				http.Error(w, "missing Authorization: Bearer <api key>", http.StatusUnauthorized)
				return
			}

			tenantID, err := Verify(r.Context(), db, rawKey)
			if err != nil {
				http.Error(w, "invalid or revoked API key", http.StatusUnauthorized)
				return
			}

			if !limiter.allow(rawKey) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}

			ctx := context.WithValue(r.Context(), tenantKey{}, tenantID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return ""
	}
	return h[len(prefix):]
}

// keyLimiter holds one token bucket per API key, in-process. A single
// scheduler instance is the entire deployment's submission path today, so
// this needs no shared/distributed state -- add one (Redis-backed) only if
// the scheduler is ever horizontally scaled behind a load balancer.
type keyLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

func newKeyLimiter() *keyLimiter {
	return &keyLimiter{limiters: make(map[string]*rate.Limiter)}
}

// allow permits up to 20 requests/sec per key with a burst of 40 -- high
// enough not to bother a legitimate integration, low enough that a leaked
// key can't flood the queue.
func (kl *keyLimiter) allow(key string) bool {
	kl.mu.Lock()
	lim, ok := kl.limiters[key]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(20), 40)
		kl.limiters[key] = lim
	}
	kl.mu.Unlock()
	return lim.Allow()
}
