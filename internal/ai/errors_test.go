package ai

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/anthropics/anthropic-sdk-go"
)

func newAPIError(status int, headers http.Header) error {
	if headers == nil {
		headers = http.Header{}
	}
	return &anthropic.Error{
		StatusCode: status,
		Request:    &http.Request{Method: "POST", URL: mustParseURL("https://api.anthropic.com/v1/messages")},
		Response:   &http.Response{StatusCode: status, Header: headers},
	}
}

func TestClassifyAPIError_BadRequestIsPermanent(t *testing.T) {
	err := classifyAPIError(newAPIError(400, nil))
	var permErr *task.ErrPermanent
	if !errors.As(err, &permErr) {
		t.Fatalf("classifyAPIError(400) = %v, want a *task.ErrPermanent", err)
	}
}

func TestClassifyAPIError_NotFoundIsPermanent(t *testing.T) {
	err := classifyAPIError(newAPIError(404, nil))
	var permErr *task.ErrPermanent
	if !errors.As(err, &permErr) {
		t.Fatalf("classifyAPIError(404) = %v, want a *task.ErrPermanent", err)
	}
}

func TestClassifyAPIError_RateLimitWithRetryAfter(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", "30")
	err := classifyAPIError(newAPIError(429, headers))

	var rae *task.ErrRetryAfter
	if !errors.As(err, &rae) {
		t.Fatalf("classifyAPIError(429 with Retry-After) = %v, want a *task.ErrRetryAfter", err)
	}
	if rae.After != 30*time.Second {
		t.Fatalf("ErrRetryAfter.After = %v, want 30s", rae.After)
	}
}

func TestClassifyAPIError_RateLimitWithoutRetryAfter(t *testing.T) {
	err := classifyAPIError(newAPIError(429, nil))

	var rae *task.ErrRetryAfter
	var permErr *task.ErrPermanent
	if errors.As(err, &rae) {
		t.Fatal("classifyAPIError(429 without Retry-After) returned ErrRetryAfter, want generic retryable error")
	}
	if errors.As(err, &permErr) {
		t.Fatal("classifyAPIError(429) marked permanent, want retryable")
	}
}

func TestClassifyAPIError_ServerErrorIsRetryable(t *testing.T) {
	err := classifyAPIError(newAPIError(503, nil))
	var permErr *task.ErrPermanent
	if errors.As(err, &permErr) {
		t.Fatal("classifyAPIError(503) marked permanent, want retryable")
	}
}

func TestClassifyAPIError_NonAPIErrorPassesThrough(t *testing.T) {
	original := errors.New("connection reset")
	got := classifyAPIError(original)
	if got != original {
		t.Fatalf("classifyAPIError() on a non-API error = %v, want the original error unchanged", got)
	}
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}
