package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type statsSnapshot struct {
	QueueDepth map[string]int `json:"queue_depth"`
	Throughput float64        `json:"throughput_per_sec"`
	P95Seconds float64        `json:"p95_duration_seconds"`
}

func (h *Handler) collectStats(ctx context.Context) (statsSnapshot, error) {
	depth := make(map[string]int)
	rows, err := h.DB.Query(ctx, "SELECT status, count(*) FROM tasks GROUP BY status")
	if err != nil {
		return statsSnapshot{}, err
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return statsSnapshot{}, err
		}
		depth[status] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return statsSnapshot{}, err
	}

	var throughput float64
	err = h.DB.QueryRow(ctx,
		"SELECT count(*)::float / 60 FROM tasks WHERE status = 'succeeded' AND completed_at > now() - interval '1 minute'",
	).Scan(&throughput)
	if err != nil {
		return statsSnapshot{}, err
	}

	var p95 float64
	err = h.DB.QueryRow(ctx, `
SELECT COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM completed_at - started_at)), 0)
FROM tasks WHERE status = 'succeeded' AND completed_at > now() - interval '5 minutes'`,
	).Scan(&p95)
	if err != nil {
		return statsSnapshot{}, err
	}

	return statsSnapshot{QueueDepth: depth, Throughput: throughput, P95Seconds: p95}, nil
}

// Stats handles GET /v1/stats -- a lightweight JSON summary for the
// dashboard's non-Prometheus consumption. /metrics remains the source of
// truth for anything Grafana-shaped; this is deliberately simpler.
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	stats, err := h.collectStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// Stream handles GET /v1/stream -- Server-Sent Events, emitting a stats
// snapshot every 5 seconds. Deliberately one-way and stdlib-only: a
// dashboard's live view needs a push channel for aggregate numbers, not a
// bidirectional WebSocket it would never use the other direction of.
// Streams stats only -- the task list itself is refreshed by the client
// invalidating its own query on each tick, not by streaming rows here.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		stats, err := h.collectStats(r.Context())
		if err == nil {
			if body, err := json.Marshal(stats); err == nil {
				fmt.Fprintf(w, "data: %s\n\n", body)
				flusher.Flush()
			}
		}

		select {
		case <-ticker.C:
			continue
		case <-r.Context().Done():
			return
		}
	}
}
