package api

import (
	"encoding/json"
	"net/http"

	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
)

type suggestCronRequest struct {
	Description string `json:"description"`
}

// SuggestCron handles POST /v1/schedules/suggest-cron -- natural language
// to a validated cron expression. Never stores anything; the caller reviews
// the readback and then calls the normal CreateSchedule.
func (h *Handler) SuggestCron(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	if h.orchestrator == nil || !h.orchestrator.IsConfigured() {
		writeError(w, http.StatusNotImplemented, "AI features are not configured on this deployment")
		return
	}

	var req suggestCronRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Description == "" {
		writeError(w, http.StatusBadRequest, "description is required")
		return
	}

	suggestion, err := h.orchestrator.SuggestCron(r.Context(), req.Description)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, suggestion)
}

type nlSearchRequest struct {
	Query string `json:"query"`
}

// SearchTasksNL handles POST /v1/tasks/search -- natural language to
// filter parameters (never SQL), then runs the exact same task.List path
// the regular filter UI uses.
func (h *Handler) SearchTasksNL(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantOrUnauthorized(w, r)
	if !ok {
		return
	}
	if h.orchestrator == nil || !h.orchestrator.IsConfigured() {
		writeError(w, http.StatusNotImplemented, "AI features are not configured on this deployment")
		return
	}

	var req nlSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}

	filters, err := h.orchestrator.InterpretDashboardQuery(r.Context(), req.Query)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	listFilter := task.ListFilter{TenantID: tenantID, Limit: 50}
	for _, f := range filters {
		switch f.Column {
		case "status":
			listFilter.Status = f.Value
		case "task_type":
			listFilter.TaskType = f.Value
		}
	}

	page, err := task.List(r.Context(), h.DB, listFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":          page.Tasks,
		"next_cursor":    page.NextCursor,
		"interpreted_as": filters,
	})
}
