package scheduler

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CommandRequest represents the structure of the request body
type CommandRequest struct {
	Command			string 	`json:"command"`
	ScheduledAt 	string	`json:"scheduled_at"` // ISO 8601 format
}

type Task struct {
	Id				string
	Command 		string
	ScheduledAt		pgtype.Timestamp
	PickedAt		pgtype.Timestamp
	StartedAt		pgtype.Timestamp
	CompletedAt		pgtype.Timestamp
	FailedAit		pgtype.Timestamp
}

// Scheduler represents an HTTP server that manages tasks.
type ScheulerServer struct {
	serverPort			string
	dbConnectionString	string
	dbPool				*pgxpool.Pool
	ctx 				context.Context
	cancel				context.CancelFunc
	httpServer			*http.Server
}

// NewServer creates and returns a new SchedulerServer
func NewServer(port string, dbConnectionString string) *ScheulerServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &ScheulerServer{
		serverPort: port,
		dbConnectionString: dbConnectionString,
		ctx: ctx,
		cancel: cancel,
	}
}

// Start