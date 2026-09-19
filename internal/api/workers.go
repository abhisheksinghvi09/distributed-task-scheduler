package api

import "net/http"

type workerSummary struct {
	WorkerID     string `json:"worker_id"`
	RunningCount int    `json:"running_count"`
}

// ListWorkers handles GET /v1/workers. Deliberately not backed by a
// separate workers table with its own heartbeat/upsert machinery -- worker
// identity already lives on every running task's worker_id column, which
// is all a dashboard's "what's active right now" view needs. Add a real
// registry only if per-worker metadata (hostname, version, uptime) becomes
// a real requirement.
func (h *Handler) ListWorkers(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}

	rows, err := h.DB.Query(r.Context(),
		"SELECT worker_id, count(*) FROM tasks WHERE status = 'running' AND worker_id IS NOT NULL GROUP BY worker_id")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var workers []workerSummary
	for rows.Next() {
		var ws workerSummary
		if err := rows.Scan(&ws.WorkerID, &ws.RunningCount); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		workers = append(workers, ws)
	}

	writeJSON(w, http.StatusOK, map[string]any{"workers": workers})
}
