package ai

import (
	"context"
	"encoding/json"

	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/jackc/pgx/v5/pgxpool"
)

const triageSystemPrompt = `You analyze failed background task errors for an operations team.
Given a list of error messages, identify the distinct failure modes, how many tasks each
affected, and classify each as "retryable" (transient: timeouts, rate limits, connection
resets) or "permanent" (needs a code or config fix: bad input, missing config, logic errors).
Be concise -- this is read by an on-call engineer, not a report audience.`

// RegisterTriageTask wires the "ai_triage" handler into the task registry.
func RegisterTriageTask(db *pgxpool.Pool) {
	orch := NewOrchestrator()
	if !orch.IsConfigured() {
		return
	}
	task.Register("ai_triage", func(ctx context.Context, raw json.RawMessage) error {
		return orch.ExecuteTriage(ctx, db)
	})
}

func recentDeadLetterErrors(ctx context.Context, db *pgxpool.Pool, limit int) ([]string, error) {
	rows, err := db.Query(ctx,
		"SELECT COALESCE(last_error, '') FROM tasks WHERE status = 'dead_letter' ORDER BY updated_at DESC LIMIT $1",
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return nil, err
		}
		if msg != "" {
			out = append(out, msg)
		}
	}
	return out, rows.Err()
}
