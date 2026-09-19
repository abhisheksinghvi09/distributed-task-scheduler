package main

import (
	"log"
	"os"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/common"
	"github.com/abhisheksinghvi09/task-scheduler/internal/coordinator"
)

func httpAddr() string {
	if addr := os.Getenv("COORDINATOR_HTTP_ADDR"); addr != "" {
		return addr
	}
	return ":8082"
}

func main() {
	dbConnectString := common.GetDBConnectionString()
	queuedGrace := common.GetDurationSeconds("TASK_QUEUED_GRACE_SECONDS", 60*time.Second)
	server := coordinator.NewServer(dbConnectString, common.GetNATSURL(), httpAddr(), queuedGrace)
	if err := server.Start(); err != nil {
		log.Fatalf("coordinator exited: %v", err)
	}
}
