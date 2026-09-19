// Package scheduler exposes the HTTP submission API. It is a thin
// translation layer over internal/task -- the same Enqueue call the
// coordinator's gRPC SubmitTask uses, which is what makes the two submit
// paths equivalent.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/api"
	"github.com/abhisheksinghvi09/task-scheduler/internal/auth"
	"github.com/abhisheksinghvi09/task-scheduler/internal/common"
	"github.com/abhisheksinghvi09/task-scheduler/internal/db"
	"github.com/abhisheksinghvi09/task-scheduler/internal/metrics"
	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scheduleRequest is the POST /schedule request body.
type scheduleRequest struct {
	TaskType       string          `json:"task_type"`
	Payload        json.RawMessage `json:"payload"`
	ScheduledAt    string          `json:"scheduled_at"` // ISO 8601; empty means "now"
	Priority       int             `json:"priority"`
	MaxAttempts    int             `json:"max_attempts"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// SchedulerServer is an HTTP server that accepts task submissions.
type SchedulerServer struct {
	serverPort         string
	dbConnectionString string
	dbPool             *pgxpool.Pool
	ctx                context.Context
	cancel             context.CancelFunc
	httpServer         *http.Server
}

// NewServer creates and returns a new SchedulerServer.
func NewServer(port string, dbConnectionString string) *SchedulerServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &SchedulerServer{
		serverPort:         port,
		dbConnectionString: dbConnectionString,
		ctx:                ctx,
		cancel:             cancel,
	}
}

// Start initializes and starts the SchedulerServer.
func (s *SchedulerServer) Start() error {
	var err error
	s.dbPool, err = common.ConnectToDatabase(s.ctx, s.dbConnectionString)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	if err := db.Migrate(s.ctx, s.dbPool); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	mux := http.NewServeMux()
	// Legacy, unauthenticated path -- kept as an alias for existing
	// integrations. New clients should use the authenticated /v1/tasks.
	mux.HandleFunc("POST /schedule", s.handleScheduleTask)
	mux.HandleFunc("GET /status/{id}", s.handleGetTaskStatus)
	metrics.RegisterOn(mux, metrics.DBReady(s.dbPool))
	api.Mount(mux, s.dbPool, auth.Middleware(s.dbPool))

	s.httpServer = &http.Server{
		Addr:    s.serverPort,
		Handler: mux,
	}

	slog.Info("scheduler starting", "addr", s.serverPort)

	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("scheduler: server failed", "error", err)
		}
	}()

	return s.awaitShutdown()
}

func (s *SchedulerServer) handleScheduleTask(w http.ResponseWriter, r *http.Request) {
	var req scheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.TaskType == "" {
		http.Error(w, "task_type is required", http.StatusBadRequest)
		return
	}

	scheduledAt := time.Now()
	if req.ScheduledAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.ScheduledAt)
		if err != nil {
			http.Error(w, "Invalid date format, use ISO 8601", http.StatusBadRequest)
			return
		}
		scheduledAt = parsed
	}

	payload := req.Payload
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}

	id, created, err := task.Enqueue(r.Context(), s.dbPool, task.New{
		Type:           req.TaskType,
		Payload:        payload,
		ScheduledAt:    scheduledAt,
		Priority:       req.Priority,
		MaxAttempts:    req.MaxAttempts,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to submit task: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	metrics.TasksSubmittedTotal.WithLabelValues(req.TaskType, strconv.Itoa(req.Priority)).Inc()

	writeJSON(w, http.StatusOK, map[string]any{
		"task_id": id,
		"created": created,
	})
}

func (s *SchedulerServer) handleGetTaskStatus(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		http.Error(w, "task id is required", http.StatusBadRequest)
		return
	}

	t, err := task.Get(r.Context(), s.dbPool, taskID)
	if err == task.ErrNotFound {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get task status: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":      t.ID,
		"status":       t.Status,
		"task_type":    t.TaskType,
		"attempts":     t.Attempts,
		"max_attempts": t.MaxAttempts,
		"scheduled_at": t.ScheduledAt,
		"last_error":   t.LastError,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("scheduler: failed to write JSON response", "error", err)
	}
}

func (s *SchedulerServer) awaitShutdown() error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	return s.Stop()
}

// Stop gracefully shuts down the SchedulerServer and the database
// connection pool. The HTTP server is drained before the pool is closed --
// otherwise an in-flight request could hit an already-closed pool.
func (s *SchedulerServer) Stop() error {
	s.cancel()

	var shutdownErr error
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr = s.httpServer.Shutdown(ctx)
	}

	if s.dbPool != nil {
		s.dbPool.Close()
	}

	slog.Info("scheduler stopped")
	return shutdownErr
}
