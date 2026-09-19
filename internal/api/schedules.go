package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/schedule"
	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
)

type createScheduleRequest struct {
	Name          string          `json:"name"`
	CronExpr      string          `json:"cron_expr"`
	Timezone      string          `json:"timezone"`
	TaskType      string          `json:"task_type"`
	Payload       json.RawMessage `json:"payload"`
	Priority      int             `json:"priority"`
	MaxAttempts   int             `json:"max_attempts"`
	CatchupPolicy string          `json:"catchup_policy"`
}

// CreateSchedule handles POST /v1/schedules. The cron expression and
// timezone are validated before anything is stored -- cron is exactly the
// domain where a confidently wrong answer looks right.
func (h *Handler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}

	var req createScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" || req.CronExpr == "" || req.TaskType == "" {
		writeError(w, http.StatusBadRequest, "name, cron_expr, and task_type are required")
		return
	}

	id, err := schedule.Create(r.Context(), h.DB, schedule.New{
		Name:          req.Name,
		CronExpr:      req.CronExpr,
		Timezone:      req.Timezone,
		TaskType:      req.TaskType,
		Payload:       req.Payload,
		Priority:      req.Priority,
		MaxAttempts:   req.MaxAttempts,
		CatchupPolicy: schedule.CatchupPolicy(req.CatchupPolicy),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"schedule_id": id})
}

// ListSchedules handles GET /v1/schedules.
func (h *Handler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	schedules, err := schedule.List(r.Context(), h.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": schedules})
}

// GetSchedule handles GET /v1/schedules/{id}.
func (h *Handler) GetSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	s, err := schedule.Get(r.Context(), h.DB, r.PathValue("id"))
	if err == schedule.ErrNotFound {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s)
}

type patchScheduleRequest struct {
	Paused *bool `json:"paused"`
}

// PatchSchedule handles PATCH /v1/schedules/{id} -- today only pause/resume.
func (h *Handler) PatchSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}

	var req patchScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Paused == nil {
		writeError(w, http.StatusBadRequest, "paused is the only patchable field")
		return
	}

	if err := schedule.SetPaused(r.Context(), h.DB, r.PathValue("id"), *req.Paused); err == schedule.ErrNotFound {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": *req.Paused})
}

// DeleteSchedule handles DELETE /v1/schedules/{id}.
func (h *Handler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	if err := schedule.Delete(r.Context(), h.DB, r.PathValue("id")); err == schedule.ErrNotFound {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TriggerSchedule handles POST /v1/schedules/{id}/trigger -- fires the
// schedule's task type immediately, independent of its cron timing. A
// great demo button, and useful for testing a schedule's payload without
// waiting for its next occurrence.
//
// Uses EnqueueScheduled (tagging the new task with schedule_id) rather
// than a plain Enqueue, so the same one-active-run-per-schedule overlap
// constraint applies here too -- triggering manually while a cron-fired
// run is still active is correctly rejected, not silently allowed to race
// it.
func (h *Handler) TriggerSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}

	s, err := schedule.Get(r.Context(), h.DB, r.PathValue("id"))
	if err == schedule.ErrNotFound {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	id, overlapped, err := task.EnqueueScheduled(r.Context(), h.DB, task.New{
		Type:        s.TaskType,
		Payload:     s.Payload,
		ScheduledAt: time.Now(),
		Priority:    s.Priority,
		MaxAttempts: s.MaxAttempts,
	}, s.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if overlapped {
		writeError(w, http.StatusConflict, "a run for this schedule is already active")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"task_id": id})
}
