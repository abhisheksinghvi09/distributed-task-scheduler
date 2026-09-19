package task

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestHandleNoop_Success(t *testing.T) {
	err := handleNoop(context.Background(), json.RawMessage(`{"sleep_ms": 1}`))
	if err != nil {
		t.Fatalf("handleNoop() error = %v, want nil", err)
	}
}

func TestHandleNoop_Fail(t *testing.T) {
	err := handleNoop(context.Background(), json.RawMessage(`{"fail": true}`))
	if err == nil {
		t.Fatal("handleNoop() with fail=true returned nil error, want an error")
	}
}

func TestHandleNoop_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	err := handleNoop(ctx, json.RawMessage(`{"sleep_ms": 5000}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("handleNoop() error = %v, want context.DeadlineExceeded", err)
	}
}

func TestHandleHTTPRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload, _ := json.Marshal(httpRequestPayload{Method: "GET", URL: srv.URL})
	if err := handleHTTPRequest(context.Background(), payload); err != nil {
		t.Fatalf("handleHTTPRequest() error = %v, want nil", err)
	}
}

func TestHandleHTTPRequest_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	payload, _ := json.Marshal(httpRequestPayload{Method: "GET", URL: srv.URL})
	if err := handleHTTPRequest(context.Background(), payload); err == nil {
		t.Fatal("handleHTTPRequest() with a 500 response returned nil error, want an error")
	}
}

func TestHandleHTTPRequest_ExpectStatusMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	payload, _ := json.Marshal(httpRequestPayload{Method: "GET", URL: srv.URL, ExpectStatus: http.StatusOK})
	if err := handleHTTPRequest(context.Background(), payload); err == nil {
		t.Fatal("handleHTTPRequest() with a status mismatch returned nil error, want an error")
	}
}

func TestHandleHTTPRequest_DeadlineExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	payload, _ := json.Marshal(httpRequestPayload{Method: "GET", URL: srv.URL})
	err := handleHTTPRequest(ctx, payload)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("handleHTTPRequest() error = %v, want a deadline-exceeded error", err)
	}
}

// TestHandleShell_DoesNotInvokeAShell is the regression test that matters:
// a single argv element containing shell metacharacters must execute as a
// literal argument, never be interpreted. If this test starts failing
// because someone "simplified" handleShell back to exec.Command("sh", "-c",
// ...), that is the metacharacter-injection vulnerability returning.
func TestHandleShell_DoesNotInvokeAShell(t *testing.T) {
	if _, err := exec.LookPath("touch"); err != nil {
		t.Skip("touch not available on this system")
	}

	dir := t.TempDir()
	canary := dir + "/should-not-exist"

	// If this ever runs through a shell, "; touch <canary>" would create
	// the file. Run as a single literal argv element to `echo` instead.
	payload, _ := json.Marshal(shellPayload{
		Argv: []string{"echo", "hello; touch " + canary},
	})

	if err := handleShell(context.Background(), payload); err != nil {
		t.Fatalf("handleShell() error = %v, want nil", err)
	}

	if _, err := os.Stat(canary); err == nil {
		t.Fatal("shell metacharacters were interpreted -- handleShell is invoking a shell")
	}
}

func TestHandleShell_EmptyArgvRejected(t *testing.T) {
	payload, _ := json.Marshal(shellPayload{Argv: nil})
	if err := handleShell(context.Background(), payload); err == nil {
		t.Fatal("handleShell() with empty argv returned nil error, want an error")
	}
}

func TestHandleShell_CommandFailurePropagates(t *testing.T) {
	payload, _ := json.Marshal(shellPayload{Argv: []string{"false"}})
	if err := handleShell(context.Background(), payload); err == nil {
		t.Fatal("handleShell() for a failing command returned nil error, want an error")
	}
}
