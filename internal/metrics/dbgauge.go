package metrics

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// allStatuses is listed explicitly (rather than derived from a SELECT
// DISTINCT) so a status with zero rows still reports 0 instead of leaving
// a stale prior value on the gauge.
var allStatuses = []string{
	"pending", "queued", "running", "succeeded", "failed",
	"dead_letter", "blocked", "cancelled",
}

// RefreshQueueDepth updates the queue-depth and oldest-pending-age gauges
// from a single query pass. Intended to run on a short ticker (a few
// seconds) from one instance -- the gauges are a point-in-time snapshot,
// not something every service needs to compute redundantly.
func RefreshQueueDepth(ctx context.Context, pool *pgxpool.Pool) {
	counts := make(map[string]int, len(allStatuses))
	rows, err := pool.Query(ctx, "SELECT status, count(*) FROM tasks GROUP BY status")
	if err != nil {
		slog.Error("metrics: refresh queue depth", "error", err)
		return
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			slog.Error("metrics: scan queue depth row", "error", err)
			return
		}
		counts[status] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Error("metrics: iterate queue depth rows", "error", err)
		return
	}

	for _, status := range allStatuses {
		TasksQueueDepth.WithLabelValues(status).Set(float64(counts[status]))
	}

	var oldestAgeSeconds float64
	err = pool.QueryRow(ctx,
		"SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(scheduled_at)), 0) FROM tasks WHERE status = 'pending'",
	).Scan(&oldestAgeSeconds)
	if err != nil {
		slog.Error("metrics: oldest pending age", "error", err)
		return
	}
	TasksOldestPendingAgeSeconds.Set(oldestAgeSeconds)
}
