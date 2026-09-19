package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"
)

func init() {
	Register("noop", handleNoop)
	Register("http_request", handleHTTPRequest)

	// Off by default: today's `command TEXT` design is remote code
	// execution behind an unauthenticated submit endpoint. Requiring an
	// explicit opt-in makes that an operator decision, not a default.
	if os.Getenv("ALLOW_SHELL_TASKS") == "1" {
		Register("shell", handleShell)
	}
}

type noopPayload struct {
	SleepMS int  `json:"sleep_ms"`
	Fail    bool `json:"fail"`
}

// handleNoop is the test workhorse: sleeps, then optionally fails so the
// retry path can be exercised deterministically.
func handleNoop(ctx context.Context, raw json.RawMessage) error {
	var p noopPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("noop: invalid payload: %w", err)
	}

	if p.SleepMS > 0 {
		select {
		case <-time.After(time.Duration(p.SleepMS) * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if p.Fail {
		return errors.New("noop: fail requested")
	}
	return nil
}

type httpRequestPayload struct {
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers"`
	Body         string            `json:"body"`
	ExpectStatus int               `json:"expect_status"`
}

// handleHTTPRequest makes the scheduler useful as a webhook/callback
// runner. A non-2xx response (or a mismatch against ExpectStatus) is an
// error, which puts it on the normal retry path.
func handleHTTPRequest(ctx context.Context, raw json.RawMessage) error {
	var p httpRequestPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("http_request: invalid payload: %w", err)
	}
	if p.Method == "" {
		p.Method = http.MethodGet
	}

	req, err := http.NewRequestWithContext(ctx, p.Method, p.URL, bytes.NewReader([]byte(p.Body)))
	if err != nil {
		return fmt.Errorf("http_request: build request: %w", err)
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http_request: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	want := p.ExpectStatus
	if want == 0 {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("http_request: unexpected status %d", resp.StatusCode)
		}
		return nil
	}
	if resp.StatusCode != want {
		return fmt.Errorf("http_request: expected status %d, got %d", want, resp.StatusCode)
	}
	return nil
}

type shellPayload struct {
	Argv      []string `json:"argv"`
	TimeoutMS int      `json:"timeout_ms"`
}

// handleShell runs argv directly via exec, deliberately never through a
// shell -- "sh -c" is what turns a task payload into arbitrary command
// injection. A single argv element containing shell metacharacters
// executes as a literal argument, not as a command.
func handleShell(ctx context.Context, raw json.RawMessage) error {
	var p shellPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("shell: invalid payload: %w", err)
	}
	if len(p.Argv) == 0 {
		return errors.New("shell: argv must not be empty")
	}

	runCtx := ctx
	if p.TimeoutMS > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(p.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, p.Argv[0], p.Argv[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("shell: %w: %s", err, output)
	}
	return nil
}
