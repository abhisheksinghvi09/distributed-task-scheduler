package ai

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveGeminiProvider_Generate(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY is not set, skipping live test")
	}

	provider := NewGeminiProvider(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resp, err := provider.Generate(ctx, GenerateRequest{
		Prompt: "Say 'scheduler-ok' and nothing else.",
	})
	if err != nil {
		t.Fatalf("Live Gemini Generate failed: %v", err)
	}

	if resp.Text == "" {
		t.Fatal("expected non-empty response text")
	}
	if resp.Usage.InputTokens == 0 {
		t.Errorf("expected InputTokens > 0, got %d", resp.Usage.InputTokens)
	}
}

func TestLiveOrchestrator_SuggestCron(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY is not set, skipping live test")
	}

	orch := NewOrchestrator()
	if !orch.IsConfigured() {
		t.Fatal("expected orchestrator to be configured with GEMINI_API_KEY")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	suggestion, err := orch.SuggestCron(ctx, "every Monday at 8am UTC")
	if err != nil {
		t.Fatalf("SuggestCron failed: %v", err)
	}

	if suggestion.CronExpr != "0 8 * * 1" {
		t.Errorf("suggestion.CronExpr = %q, want '0 8 * * 1'", suggestion.CronExpr)
	}
}

func TestLiveOrchestrator_InterpretDashboardQuery(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY is not set, skipping live test")
	}

	orch := NewOrchestrator()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	filters, err := orch.InterpretDashboardQuery(ctx, "tasks that failed")
	if err != nil {
		t.Fatalf("InterpretDashboardQuery failed: %v", err)
	}

	if len(filters) == 0 {
		t.Fatal("expected at least one filter for failed tasks")
	}
	foundStatus := false
	for _, f := range filters {
		if f.Column == "status" && f.Value == "failed" {
			foundStatus = true
		}
	}
	if !foundStatus {
		t.Errorf("expected filter {status eq failed}, got %+v", filters)
	}
}
