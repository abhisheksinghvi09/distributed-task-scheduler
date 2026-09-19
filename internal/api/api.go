// Package api implements the /v1 JSON API: the scheduler's programmatic
// interface and the surface the dashboard (a separately deployed Next.js
// app) talks to through its own backend-for-frontend proxy. Every handler
// here runs behind auth.Middleware -- see scheduler.go for how the /v1
// subtree is mounted.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/abhisheksinghvi09/task-scheduler/internal/ai"
	"github.com/abhisheksinghvi09/task-scheduler/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler holds the dependencies every /v1 endpoint needs.
type Handler struct {
	DB           *pgxpool.Pool
	orchestrator *ai.Orchestrator
}

// Mount registers every /v1 route on mux, wrapped in authMiddleware. The
// AI routes (suggest-cron, task search) are always mounted -- the API
// shape doesn't change based on configuration -- but return 501 unless
// an AI provider (GEMINI_API_KEY or ANTHROPIC_API_KEY) is configured.
func Mount(mux *http.ServeMux, db *pgxpool.Pool, authMiddleware func(http.Handler) http.Handler) {
	h := &Handler{DB: db, orchestrator: ai.NewOrchestrator()}

	v1 := http.NewServeMux()
	v1.HandleFunc("POST /tasks", h.SubmitTask)
	v1.HandleFunc("POST /tasks/batch", h.SubmitBatch)
	v1.HandleFunc("POST /tasks/search", h.SearchTasksNL)
	v1.HandleFunc("GET /tasks", h.ListTasks)
	v1.HandleFunc("GET /tasks/{id}", h.GetTask)
	v1.HandleFunc("POST /tasks/{id}/requeue", h.RequeueTask)
	v1.HandleFunc("POST /tasks/{id}/cancel", h.CancelTask)

	v1.HandleFunc("POST /schedules", h.CreateSchedule)
	v1.HandleFunc("POST /schedules/suggest-cron", h.SuggestCron)
	v1.HandleFunc("GET /schedules", h.ListSchedules)
	v1.HandleFunc("GET /schedules/{id}", h.GetSchedule)
	v1.HandleFunc("PATCH /schedules/{id}", h.PatchSchedule)
	v1.HandleFunc("DELETE /schedules/{id}", h.DeleteSchedule)
	v1.HandleFunc("POST /schedules/{id}/trigger", h.TriggerSchedule)

	v1.HandleFunc("GET /workers", h.ListWorkers)
	v1.HandleFunc("GET /stats", h.Stats)
	v1.HandleFunc("GET /stream", h.Stream)

	mux.Handle("/v1/", http.StripPrefix("/v1", authMiddleware(v1)))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func tenantOrUnauthorized(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenantID, ok := auth.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing tenant context")
		return "", false
	}
	return tenantID, true
}
