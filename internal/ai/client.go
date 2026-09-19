// Package ai is the scheduler's LLM-workload surface: it treats the
// scheduler as infrastructure for running Claude calls, not a chatbot
// bolted onto a task queue. Every handler here is a bounded, verifiable
// transformation (run a prompt, parse cron, classify a filter) -- nothing
// makes scheduling or retry decisions with the model, because the entire
// value of the durability core (internal/task) is that recovery behavior
// is provable, and "the LLM probably retries sensibly" is not a test.
package ai

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultModel is used unless a task's payload names a different one.
const DefaultModel = "claude-opus-5"

// NewClient builds an Anthropic client with SDK-level retries disabled.
// This is the load-bearing decision in this package: without it, the SDK
// would retry a 429 silently inside one worker process -- invisible to
// metrics, uncounted in the dashboard, and lost entirely if that worker
// dies mid-retry. Handlers instead classify the error (see errors.go) and
// return it to the caller, which reports it through task.Fail with
// task.ErrRetryAfter when the API gave a precise delay -- making every
// retry durable, visible, and survivable across worker restarts. The
// queue owns retries; the SDK owns exactly one attempt.
func NewClient() anthropic.Client {
	return anthropic.NewClient(option.WithMaxRetries(0))
}
