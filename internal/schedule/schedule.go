// Package schedule implements recurring (cron) task creation -- the
// feature that makes this a scheduler rather than a plain queue. It uses
// only robfig/cron's parser, never its in-process scheduler: an
// in-process scheduler would be single-node and would fight the
// coordinator's own firing loop, which must be safe across multiple
// coordinator replicas.
package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/robfig/cron/v3"
)

// parser accepts standard 5-field cron plus descriptors (@daily, @hourly).
// Constructed once; robfig's parser is safe for concurrent use.
var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

const maxCatchupFires = 100 // hard cap so a long-down catchup_policy=all schedule can't wedge the firing loop

var (
	ErrInvalidCronExpr = errors.New("schedule: invalid cron expression")
	ErrInvalidTimezone = errors.New("schedule: invalid IANA timezone")
	ErrNotFound        = errors.New("schedule: not found")
)

type CatchupPolicy string

const (
	CatchupSkip CatchupPolicy = "skip"
	CatchupOne  CatchupPolicy = "one"
	CatchupAll  CatchupPolicy = "all"
)

// New describes a schedule creation request.
type New struct {
	Name          string
	CronExpr      string
	Timezone      string // IANA name; "" means UTC
	TaskType      string
	Payload       json.RawMessage
	Priority      int
	MaxAttempts   int
	CatchupPolicy CatchupPolicy
}

// Schedule is a stored recurring schedule.
type Schedule struct {
	ID            string
	Name          string
	CronExpr      string
	Timezone      string
	TaskType      string
	Payload       json.RawMessage
	Priority      int
	MaxAttempts   int
	CatchupPolicy CatchupPolicy
	NextRunAt     time.Time
	LastRunAt     *time.Time
	Paused        bool
}

// ValidateCronExpr parses expr and returns a human-readable description of
// its meaning error -- validated once at creation time so a bad expression
// is never stored. Cron is exactly the domain where a confidently wrong
// answer looks right, so callers (including an eventual NL-to-cron
// feature) should surface this description back to the user before saving.
func ValidateCronExpr(expr string) (cron.Schedule, error) {
	sched, err := parser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidCronExpr, expr, err)
	}
	return sched, nil
}

// ValidateTimezone resolves an IANA timezone name, defaulting to UTC.
func ValidateTimezone(tz string) (*time.Location, error) {
	if tz == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidTimezone, tz, err)
	}
	return loc, nil
}

// Create validates and inserts a new schedule, computing its first
// next_run_at from the current time.
func Create(ctx context.Context, db *pgxpool.Pool, n New) (string, error) {
	sched, err := ValidateCronExpr(n.CronExpr)
	if err != nil {
		return "", err
	}
	loc, err := ValidateTimezone(n.Timezone)
	if err != nil {
		return "", err
	}
	if n.CatchupPolicy == "" {
		n.CatchupPolicy = CatchupSkip
	}
	maxAttempts := n.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	payload := n.Payload
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}
	tz := n.Timezone
	if tz == "" {
		tz = "UTC"
	}

	nextRun := sched.Next(time.Now().In(loc))

	const q = `
INSERT INTO schedules (name, cron_expr, timezone, task_type, payload, priority, max_attempts, catchup_policy, next_run_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id`

	var id string
	err = db.QueryRow(ctx, q, n.Name, n.CronExpr, tz, n.TaskType, payload, n.Priority, maxAttempts, string(n.CatchupPolicy), nextRun).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create schedule: %w", err)
	}
	return id, nil
}

// Get returns one schedule by id.
func Get(ctx context.Context, db *pgxpool.Pool, id string) (*Schedule, error) {
	const q = `
SELECT id, name, cron_expr, timezone, task_type, payload, priority, max_attempts, catchup_policy, next_run_at, last_run_at, paused
FROM schedules WHERE id = $1`

	var s Schedule
	err := db.QueryRow(ctx, q, id).Scan(&s.ID, &s.Name, &s.CronExpr, &s.Timezone, &s.TaskType, &s.Payload,
		&s.Priority, &s.MaxAttempts, &s.CatchupPolicy, &s.NextRunAt, &s.LastRunAt, &s.Paused)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get schedule %s: %w", id, err)
	}
	return &s, nil
}

// List returns every schedule, newest first.
func List(ctx context.Context, db *pgxpool.Pool) ([]Schedule, error) {
	const q = `
SELECT id, name, cron_expr, timezone, task_type, payload, priority, max_attempts, catchup_policy, next_run_at, last_run_at, paused
FROM schedules ORDER BY created_at DESC`

	rows, err := db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()

	var out []Schedule
	for rows.Next() {
		var s Schedule
		if err := rows.Scan(&s.ID, &s.Name, &s.CronExpr, &s.Timezone, &s.TaskType, &s.Payload,
			&s.Priority, &s.MaxAttempts, &s.CatchupPolicy, &s.NextRunAt, &s.LastRunAt, &s.Paused); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetPaused pauses or resumes a schedule. Resuming does not itself catch
// up any missed windows -- that happens naturally on the next firing pass,
// governed by the schedule's own catchup_policy.
func SetPaused(ctx context.Context, db *pgxpool.Pool, id string, paused bool) error {
	tag, err := db.Exec(ctx, "UPDATE schedules SET paused = $2, updated_at = now() WHERE id = $1", id, paused)
	if err != nil {
		return fmt.Errorf("set paused for schedule %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a schedule. Tasks it already created are left alone.
func Delete(ctx context.Context, db *pgxpool.Pool, id string) error {
	tag, err := db.Exec(ctx, "DELETE FROM schedules WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("delete schedule %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
