package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ReadyChecker reports whether a dependency is currently reachable.
type ReadyChecker func(ctx context.Context) error

// DBReady returns a ReadyChecker that pings a Postgres pool.
func DBReady(pool *pgxpool.Pool) ReadyChecker {
	return func(ctx context.Context) error { return pool.Ping(ctx) }
}

// NATSReady returns a ReadyChecker that checks an existing connection's
// state -- no round trip needed, the client already tracks this.
func NATSReady(nc *nats.Conn) ReadyChecker {
	return func(ctx context.Context) error {
		if nc.IsConnected() {
			return nil
		}
		return nc.LastError()
	}
}

// Mux returns a standalone http.Handler serving /metrics, /healthz, and
// /readyz -- for a service (coordinator, worker) with no HTTP API of its
// own to share a mux with.
func Mux(checks ...ReadyChecker) http.Handler {
	mux := http.NewServeMux()
	RegisterOn(mux, checks...)
	return mux
}

// RegisterOn adds /metrics, /healthz, and /readyz to an existing mux --
// for a service (the scheduler) that already serves its own API and wants
// observability on the same port rather than a second listener.
//
// /healthz never checks dependencies -- a Postgres blip must not make an
// orchestrator restart every pod simultaneously. /readyz does, so traffic
// drains from an unready instance instead.
func RegisterOn(mux *http.ServeMux, checks ...ReadyChecker) {
	mux.Handle("GET /metrics", promhttp.Handler())

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		for _, check := range checks {
			if err := check(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(err.Error()))
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}
