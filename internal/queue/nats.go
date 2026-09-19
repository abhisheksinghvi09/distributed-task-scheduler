// Package queue wraps NATS JetStream as the dispatch transport between the
// coordinator and workers. It is deliberately thin: a message carries only
// a task id (see Envelope), never the payload or the claim. Postgres
// remains the sole source of truth -- see internal/task. JetStream's job
// ends the moment a worker's claim attempt is resolved; AckWait exists to
// bound the delivery-to-claim window, not to serve as a lease.
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	StreamName = "TASKS"

	// AckWait covers only the gap between delivery and the claim
	// transaction committing -- a few milliseconds in the healthy case.
	// It is intentionally short and MaxDeliver intentionally low: if a
	// worker can't complete one claim attempt in three tries, the
	// reaper's queued-timeout pass is the correct backstop, not
	// redelivery pressure from the broker.
	AckWait    = 30 * time.Second
	MaxDeliver = 3

	// MaxAckPending bounds how many messages a consumer can hold
	// unacknowledged at once -- real backpressure. It should track a
	// worker's execution capacity (see internal/worker's claim-buffer
	// sizing rationale), not be left unbounded.
	MaxAckPending = 64
)

// Tier buckets tasks by priority into one of three subjects. JetStream's
// WorkQueuePolicy retention forbids overlapping consumer filter subjects,
// so per-tier subjects (rather than one consumer per worker) is what makes
// priority-ordered fan-out to N workers actually work.
type Tier string

const (
	TierHigh    Tier = "high"
	TierDefault Tier = "default"
	TierLow     Tier = "low"
)

// TierFor buckets a priority value into a subject tier. Thresholds are
// deliberately coarse -- three tiers, not N -- because JetStream consumers
// (and their AckWait/MaxDeliver/MaxAckPending config) are provisioned per
// tier, not per distinct priority value.
func TierFor(priority int) Tier {
	switch {
	case priority >= 10:
		return TierHigh
	case priority < 0:
		return TierLow
	default:
		return TierDefault
	}
}

func subject(tier Tier, taskType string) string {
	return fmt.Sprintf("tasks.%s.%s", tier, taskType)
}

// Envelope is the entire message body. The payload lives in Postgres;
// shipping it twice invites drift between what was dispatched and what a
// worker actually claims and runs.
type Envelope struct {
	TaskID   string `json:"task_id"`
	TaskType string `json:"task_type"`
}

// Client owns a JetStream connection used for both publishing (coordinator)
// and consuming (worker).
type Client struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// Connect dials NATS and returns a JetStream-capable client. Retries with
// backoff -- mirroring common.ConnectToDatabase -- since NATS and the
// coordinator/worker processes typically start concurrently in compose.
func Connect(ctx context.Context, url string) (*Client, error) {
	var nc *nats.Conn
	var err error

	backoff := time.Second
	for attempt := 0; attempt < 10; attempt++ {
		nc, err = nats.Connect(url, nats.Timeout(5*time.Second))
		if err == nil {
			break
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if backoff < 16*time.Second {
			backoff *= 2
		}
	}
	if err != nil {
		return nil, fmt.Errorf("connect to nats after retries: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}

	return &Client{nc: nc, js: js}, nil
}

func (c *Client) Close() {
	if c.nc != nil {
		c.nc.Close()
	}
}

// EnsureStream creates the TASKS stream if it does not already exist.
// Idempotent -- safe to call from every service on startup, same pattern
// as internal/db.Migrate.
func (c *Client) EnsureStream(ctx context.Context) error {
	_, err := c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      StreamName,
		Subjects:  []string{"tasks.>"},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.FileStorage,
		Discard:   jetstream.DiscardOld,
		MaxMsgs:   1_000_000,
	})
	if err != nil {
		return fmt.Errorf("ensure stream %s: %w", StreamName, err)
	}
	return nil
}

// Publish sends a task-id envelope to the subject for its tier. Called
// after the relay's claiming transaction has already committed -- never
// from inside it. If publish fails, the row is left in 'queued' and the
// reaper's queued-timeout pass recovers it; that is the deliberate,
// bounded, self-healing dual-write gap described in the architecture
// notes, not a bug to route around with a distributed transaction.
func (c *Client) Publish(ctx context.Context, priority int, taskID, taskType string) error {
	env := Envelope{TaskID: taskID, TaskType: taskType}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	subj := subject(TierFor(priority), taskType)
	_, err = c.js.Publish(ctx, subj, data, jetstream.WithMsgID(taskID))
	if err != nil {
		return fmt.Errorf("publish task %s: %w", taskID, err)
	}
	return nil
}

// EnsureConsumer creates (or reuses) a durable pull consumer for one
// priority tier.
func (c *Client) EnsureConsumer(ctx context.Context, tier Tier) (jetstream.Consumer, error) {
	name := "WORKERS_" + string(tier)
	cons, err := c.js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:       name,
		FilterSubject: fmt.Sprintf("tasks.%s.>", tier),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       AckWait,
		MaxDeliver:    MaxDeliver,
		MaxAckPending: MaxAckPending,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure consumer for tier %s: %w", tier, err)
	}
	return cons, nil
}
