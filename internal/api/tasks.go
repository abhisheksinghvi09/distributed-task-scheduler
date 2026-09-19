package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/metrics"
	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
)

type submitTaskRequest struct {
	TaskType       string          `json:"task_type"`
	Payload        json.RawMessage `json:"payload"`
	ScheduledAt    string          `json:"scheduled_at"`
	Priority       int             `json:"priority"`
	MaxAttempts    int             `json:"max_attempts"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// SubmitTask handles POST /v1/tasks -- the authenticated, tenant-scoped
// equivalent of the legacy POST /schedule.
func (h *Handler) SubmitTask(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantOrUnauthorized(w, r)
	if !ok {
		return
	}

	var req submitTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TaskType == "" {
		writeError(w, http.StatusBadRequest, "task_type is required")
		return
	}

	scheduledAt := time.Now()
	if req.ScheduledAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.ScheduledAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid scheduled_at, use ISO 8601")
			return
		}
		scheduledAt = parsed
	}
	payload := req.Payload
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}

	id, created, err := task.Enqueue(r.Context(), h.DB, task.New{
		Type:           req.TaskType,
		Payload:        payload,
		ScheduledAt:    scheduledAt,
		Priority:       req.Priority,
		MaxAttempts:    req.MaxAttempts,
		IdempotencyKey: req.IdempotencyKey,
		TenantID:       tenantID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	metrics.TasksSubmittedTotal.WithLabelValues(req.TaskType, strconv.Itoa(req.Priority)).Inc()
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"task_id": id, "created": created})
}

type batchItemRequest struct {
	LocalRef    string          `json:"local_ref"`
	DependsOn   []string        `json:"depends_on"`
	TaskType    string          `json:"task_type"`
	Payload     json.RawMessage `json:"payload"`
	ScheduledAt string          `json:"scheduled_at"`
	Priority    int             `json:"priority"`
	MaxAttempts int             `json:"max_attempts"`
}

// SubmitBatch handles POST /v1/tasks/batch -- the only way to create task
// dependencies, since a client can't know a dependency's real id before it
// exists. Every item is created atomically: either the whole DAG exists
// with correct links, or none of it does.
//
// Batch submission does not carry a tenant_id per item today -- see the
// README's noted limitation; add it if multi-tenant DAGs become a real
// requirement.
func (h *Handler) SubmitBatch(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}

	var reqs []batchItemRequest
	if err := json.NewDecoder(r.Body).Decode(&reqs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(reqs) == 0 {
		writeError(w, http.StatusBadRequest, "batch must contain at least one item")
		return
	}

	items := make([]task.BatchItem, len(reqs))
	for i, req := range reqs {
		if req.TaskType == "" {
			writeError(w, http.StatusBadRequest, "task_type is required for every batch item")
			return
		}
		scheduledAt := time.Now()
		if req.ScheduledAt != "" {
			parsed, err := time.Parse(time.RFC3339, req.ScheduledAt)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid scheduled_at, use ISO 8601")
				return
			}
			scheduledAt = parsed
		}
		payload := req.Payload
		if payload == nil {
			payload = json.RawMessage(`{}`)
		}
		items[i] = task.BatchItem{
			LocalRef:      req.LocalRef,
			DependsOnRefs: req.DependsOn,
			New: task.New{
				Type:        req.TaskType,
				Payload:     payload,
				ScheduledAt: scheduledAt,
				Priority:    req.Priority,
				MaxAttempts: req.MaxAttempts,
			},
		}
	}

	refToID, err := task.EnqueueBatch(r.Context(), h.DB, items)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task_ids": refToID})
}

// ListTasks handles GET /v1/tasks -- filterable, keyset-paginated, always
// scoped to the authenticated tenant.
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantOrUnauthorized(w, r)
	if !ok {
		return
	}

	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))

	page, err := task.List(r.Context(), h.DB, task.ListFilter{
		TenantID: tenantID,
		Status:   q.Get("status"),
		TaskType: q.Get("task_type"),
		Limit:    limit,
		Cursor:   q.Get("cursor"),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":       page.Tasks,
		"next_cursor": page.NextCursor,
	})
}

// GetTask handles GET /v1/tasks/{id}.
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}

	t, err := task.Get(r.Context(), h.DB, r.PathValue("id"))
	if err == task.ErrNotFound {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// RequeueTask handles POST /v1/tasks/{id}/requeue.
func (h *Handler) RequeueTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}

	ok, err := task.Requeue(r.Context(), h.DB, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusConflict, "task not found or not in a terminal state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"requeued": true})
}

// CancelTask handles POST /v1/tasks/{id}/cancel. See task.CancelResult for
// the honest ceiling on how fast this actually takes effect for a running
// task.
func (h *Handler) CancelTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}

	result, err := task.Cancel(r.Context(), h.DB, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	switch result {
	case task.CancelNotFound:
		writeError(w, http.StatusNotFound, "task not found")
	case task.CancelledImmediately:
		writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
	case task.CancelRequested:
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "cancel_requested",
			"note":   "task is running; cancellation may take up to one lease-renewal interval",
		})
	case task.CancelNoop:
		writeError(w, http.StatusConflict, "task is already in a terminal state")
	}
}
