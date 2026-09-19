package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
)

func TestGeminiProvider_GenerateSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("got method %s, want POST", r.Method)
		}
		if key := r.URL.Query().Get("key"); key != "test-key" {
			t.Errorf("got key %s, want test-key", key)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{"text": "Hello from Gemini"}],
					"role": "model"
				},
				"finishReason": "STOP",
				"index": 0
			}],
			"usageMetadata": {
				"promptTokenCount": 10,
				"candidatesTokenCount": 5,
				"totalTokenCount": 15
			},
			"modelVersion": "gemini-3.5-flash-lite"
		}`))
	}))
	defer server.Close()

	provider := NewGeminiProviderWithURL("test-key", server.URL)
	resp, err := provider.Generate(context.Background(), GenerateRequest{
		Prompt: "Say hello",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}

	if resp.Text != "Hello from Gemini" {
		t.Errorf("resp.Text = %q, want %q", resp.Text, "Hello from Gemini")
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("resp.Usage.InputTokens = %d, want 10", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 5 {
		t.Errorf("resp.Usage.OutputTokens = %d, want 5", resp.Usage.OutputTokens)
	}
}

func TestGeminiProvider_SafetyRefusalIsPermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"finishReason": "SAFETY",
				"index": 0
			}]
		}`))
	}))
	defer server.Close()

	provider := NewGeminiProviderWithURL("test-key", server.URL)
	_, err := provider.Generate(context.Background(), GenerateRequest{
		Prompt: "Dangerous prompt",
	})
	if err == nil {
		t.Fatal("Generate() with SAFETY finishReason returned nil error, want error")
	}

	var permErr *task.ErrPermanent
	if !errors.As(err, &permErr) {
		t.Fatalf("Generate() error = %v, want *task.ErrPermanent", err)
	}
}

func TestGeminiProvider_HTTP400IsPermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Invalid argument"}}`))
	}))
	defer server.Close()

	provider := NewGeminiProviderWithURL("test-key", server.URL)
	_, err := provider.Generate(context.Background(), GenerateRequest{Prompt: "Bad request"})
	if err == nil {
		t.Fatal("Generate() on 400 returned nil, want error")
	}

	var permErr *task.ErrPermanent
	if !errors.As(err, &permErr) {
		t.Fatalf("Generate() error = %v, want *task.ErrPermanent", err)
	}
}

func TestGeminiProvider_HTTP404IsPermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": {"code": 404, "message": "Model not found"}}`))
	}))
	defer server.Close()

	provider := NewGeminiProviderWithURL("test-key", server.URL)
	_, err := provider.Generate(context.Background(), GenerateRequest{Prompt: "Unknown model"})
	if err == nil {
		t.Fatal("Generate() on 404 returned nil, want error")
	}

	var permErr *task.ErrPermanent
	if !errors.As(err, &permErr) {
		t.Fatalf("Generate() error = %v, want *task.ErrPermanent", err)
	}
}

func TestGeminiProvider_HTTP429WithRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": {"code": 429, "message": "Resource exhausted"}}`))
	}))
	defer server.Close()

	provider := NewGeminiProviderWithURL("test-key", server.URL)
	_, err := provider.Generate(context.Background(), GenerateRequest{Prompt: "Rate limited"})
	if err == nil {
		t.Fatal("Generate() on 429 returned nil, want error")
	}

	var rae *task.ErrRetryAfter
	if !errors.As(err, &rae) {
		t.Fatalf("Generate() error = %v, want *task.ErrRetryAfter", err)
	}
	if rae.After != 20*time.Second {
		t.Errorf("rae.After = %v, want 20s", rae.After)
	}
}

func TestGeminiProvider_HTTP503IsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error": {"code": 503, "message": "High demand"}}`))
	}))
	defer server.Close()

	provider := NewGeminiProviderWithURL("test-key", server.URL)
	_, err := provider.Generate(context.Background(), GenerateRequest{Prompt: "Unavailable"})
	if err == nil {
		t.Fatal("Generate() on 503 returned nil, want error")
	}

	var permErr *task.ErrPermanent
	if errors.As(err, &permErr) {
		t.Fatalf("Generate() 503 error marked permanent, want retryable")
	}
}
