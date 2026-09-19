package main

import (
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/ai"
	"github.com/abhisheksinghvi09/task-scheduler/internal/common"
	"github.com/abhisheksinghvi09/task-scheduler/internal/worker"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dbConnectString := common.GetDBConnectionString()
	lease := common.GetDurationSeconds("TASK_LEASE_SECONDS", 300*time.Second)
	server := worker.NewServer(dbConnectString, common.GetNATSURL(), httpAddr(), lease)

	// AI task types are opt-in: registering them with no API key configured
	// would just dead-letter every llm_task/ai_triage submission on first
	// use, which is a worse failure mode than not offering the type at all.
	orchestrator := ai.NewOrchestrator()
	if orchestrator.IsConfigured() {
		server.OnConnected = func(pool *pgxpool.Pool) {
			orchestrator.RegisterTasks(pool)
			slog.Info("worker: AI task types registered",
				"provider", orchestrator.ProviderName(),
				"default_model", orchestrator.DefaultModel(),
				"types", []string{"llm_task", "ai_triage"})
		}
	}

	if err := server.Start(); err != nil {
		log.Fatalf("worker exited: %v", err)
	}
}

func httpAddr() string {
	return os.Getenv("WORKER_HTTP_ADDR") // "" is valid: metrics server is optional
}
