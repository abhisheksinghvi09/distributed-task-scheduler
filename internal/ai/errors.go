package ai

import (
	"errors"
	"strconv"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/anthropics/anthropic-sdk-go"
)

// classifyAPIError turns an Anthropic API error into either a retryable
// failure (optionally with a precise delay from the Retry-After header)
// or a permanent one that should dead-letter without burning the retry
// budget on something that will never succeed. Returned as a plain error
// (possibly wrapping *task.ErrRetryAfter) so callers just return it from
// their handler like any other error -- the worker's existing
// errors.As(err, &rae) check in reportFailure does the rest.
func classifyAPIError(err error) error {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return err // network/transport error -- treat as retryable via generic backoff
	}

	switch {
	case apiErr.StatusCode == 400, apiErr.StatusCode == 404:
		// Malformed request or unknown resource (e.g. a bad model name):
		// retrying identical input can never succeed.
		return &task.ErrPermanent{Err: err}
	case apiErr.StatusCode == 429, apiErr.StatusCode == 529, apiErr.StatusCode >= 500:
		if d, ok := retryAfterDelay(apiErr); ok {
			return &task.ErrRetryAfter{After: d, Err: err}
		}
		return err
	default:
		return err
	}
}

func retryAfterDelay(apiErr *anthropic.Error) (time.Duration, bool) {
	if apiErr.Response == nil {
		return 0, false
	}
	raw := apiErr.Response.Header.Get("Retry-After")
	if raw == "" {
		return 0, false
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}
