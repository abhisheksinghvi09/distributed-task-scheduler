//go:build chaos

package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// ledgerPayload configures the chaos test's effect-ledger handler. Compiled
// into the worker binary only under the chaos build tag -- entirely absent
// from production builds.
type ledgerPayload struct {
	DSN string `json:"dsn"`
}

func init() {
	Register("ledger", handleLedger)
}

// handleLedger proves at-least-once delivery empirically: it always
// records an attempt (chaos_attempts, append-only), then records the side
// effect exactly once (chaos_effects, primary-keyed, no ON CONFLICT -- a
// genuine duplicate execution throws a unique violation instead of being
// silently absorbed). Uses database/sql directly so the chaos test's
// assertions are independent of the task package's own claim machinery.
func handleLedger(ctx context.Context, raw json.RawMessage) error {
	var p ledgerPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("ledger: invalid payload: %w", err)
	}

	taskID, ok := TaskIDFromContext(ctx)
	if !ok {
		return fmt.Errorf("ledger: no task id in context")
	}

	db, err := sql.Open("pgx", p.DSN)
	if err != nil {
		return fmt.Errorf("ledger: open: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "INSERT INTO chaos_attempts (task_id) VALUES ($1)", taskID); err != nil {
		return fmt.Errorf("ledger: record attempt: %w", err)
	}

	select {
	case <-time.After(time.Duration(300+rand.Intn(500)) * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}

	if _, err := db.ExecContext(ctx, "INSERT INTO chaos_effects (task_id) VALUES ($1)", taskID); err != nil {
		return fmt.Errorf("ledger: record effect (likely a genuine duplicate execution): %w", err)
	}

	return nil
}
