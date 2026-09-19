// Package task owns the durable execution model for the scheduler: the
// status machine, claim/lease semantics, and retry backoff. Postgres is the
// single source of truth for every task's state -- nothing in this package
// depends on how a task is dispatched (gRPC today, NATS JetStream later).
package task

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"time"
)

// Status is one of the eight states a task can occupy. Defined as a full
// set up front (including queued/blocked/cancelled, unused until later
// phases) because widening a CHECK-constrained status column later means
// touching every query that filters on it.
type Status string

const (
	StatusPending    Status = "pending"
	StatusQueued     Status = "queued"
	StatusRunning    Status = "running"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusDeadLetter Status = "dead_letter"
	StatusBlocked    Status = "blocked"
	StatusCancelled  Status = "cancelled"
)

// ErrAlreadyClaimed is returned by ClaimByID when the task is no longer in
// a claimable state -- it was claimed by another worker, already finished,
// or was reaped. The caller should treat this as a no-op, not a failure.
var ErrAlreadyClaimed = errors.New("task: already claimed or not claimable")

// ErrNotFound is returned by Get when no task exists with the given id.
var ErrNotFound = errors.New("task: not found")

// ErrRetryAfter lets a handler override the computed exponential backoff
// with a precise delay -- e.g. a 429 response's Retry-After header, which
// is a known-good instruction rather than a guess. Fail() checks for this
// via errors.As before falling back to Backoff.
type ErrRetryAfter struct {
	After time.Duration
	Err   error
}

func (e *ErrRetryAfter) Error() string { return e.Err.Error() }
func (e *ErrRetryAfter) Unwrap() error { return e.Err }

// ErrPermanent marks a failure that retrying can never fix -- a malformed
// request, an unknown resource, anything where the input is the problem,
// not transient conditions. The worker checks for this via errors.As and
// calls FailPermanent instead of Fail, dead-lettering immediately rather
// than burning max_attempts retries on something that will fail identically
// every time.
type ErrPermanent struct{ Err error }

func (e *ErrPermanent) Error() string { return e.Err.Error() }
func (e *ErrPermanent) Unwrap() error { return e.Err }

// New describes a task submission.
type New struct {
	Type           string
	Payload        json.RawMessage
	ScheduledAt    time.Time
	Priority       int
	MaxAttempts    int
	IdempotencyKey string // "" means no dedupe
	TenantID       string // "" means unscoped -- the legacy, unauthenticated submit path
}

// Claimed is the row state returned to whoever just claimed a task for
// execution -- the coordinator's relay (pending->queued) or a worker
// (queued->running).
type Claimed struct {
	ID          string
	TaskType    string
	Payload     json.RawMessage
	Priority    int
	Attempt     int32
	MaxAttempts int32
	ScheduledAt time.Time // when the task became eligible to run
}

// Task is the full row, used for status lookups.
type Task struct {
	ID          string
	Status      Status
	TaskType    string
	Payload     json.RawMessage
	Attempts    int32
	MaxAttempts int32
	Priority    int
	ScheduledAt time.Time
	LastError   string
	TenantID    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const (
	backoffBase = 5 * time.Second
	backoffCap  = 10 * time.Minute
)

// Backoff returns the delay before retrying the given attempt number,
// exponential with cap and +/-50% jitter. Mirrored in SQL (see store.go)
// because the reaper must compute the same delay for tasks whose worker is
// dead and can't be asked anything -- the two are asserted to agree in
// bounds, not exactly, since jitter is intentionally nondeterministic.
func Backoff(attempt int32) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := float64(backoffBase) * math.Pow(2, float64(attempt-1))
	if d > float64(backoffCap) {
		d = float64(backoffCap)
	}
	jitter := 0.5 + rand.Float64() // [0.5, 1.5)
	return time.Duration(d * jitter)
}
