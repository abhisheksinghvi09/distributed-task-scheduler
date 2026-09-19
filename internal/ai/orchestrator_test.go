package ai

import (
	"context"
	"testing"
)

type mockProvider struct {
	name         string
	defaultModel string
	generateFunc func(ctx context.Context, req GenerateRequest) (*GenerateResponse, error)
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) DefaultModel() string {
	return m.defaultModel
}

func (m *mockProvider) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, req)
	}
	return &GenerateResponse{
		Text:         "ok",
		Usage:        Usage{InputTokens: 5, OutputTokens: 5},
		FinishReason: "STOP",
		Model:        m.defaultModel,
	}, nil
}

func TestOrchestrator_SuggestCron_Success(t *testing.T) {
	mock := &mockProvider{
		name:         "mock",
		defaultModel: "test-model",
		generateFunc: func(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
			return &GenerateResponse{
				Text: `{"cron_expr": "0 9 * * 1-5", "timezone": "America/New_York", "explanation": "Weekdays at 9am"}`,
			}, nil
		},
	}

	orch := NewOrchestratorWithProvider(mock)
	sugg, err := orch.SuggestCron(context.Background(), "every weekday at 9am Eastern")
	if err != nil {
		t.Fatalf("SuggestCron() error = %v, want nil", err)
	}

	if sugg.CronExpr != "0 9 * * 1-5" {
		t.Errorf("sugg.CronExpr = %q, want '0 9 * * 1-5'", sugg.CronExpr)
	}
	if sugg.Timezone != "America/New_York" {
		t.Errorf("sugg.Timezone = %q, want 'America/New_York'", sugg.Timezone)
	}
}

func TestOrchestrator_SuggestCron_RetriesOnInvalidCron(t *testing.T) {
	callCount := 0
	mock := &mockProvider{
		name:         "mock",
		defaultModel: "test-model",
		generateFunc: func(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
			callCount++
			if callCount == 1 {
				// Invalid 4-field cron
				return &GenerateResponse{
					Text: `{"cron_expr": "0 9 *", "timezone": "UTC", "explanation": "bad"}`,
				}, nil
			}
			// Valid 5-field cron on retry
			return &GenerateResponse{
				Text: `{"cron_expr": "0 9 * * *", "timezone": "UTC", "explanation": "daily at 9am"}`,
			}, nil
		},
	}

	orch := NewOrchestratorWithProvider(mock)
	sugg, err := orch.SuggestCron(context.Background(), "every day at 9am")
	if err != nil {
		t.Fatalf("SuggestCron() error = %v, want nil", err)
	}

	if callCount != 2 {
		t.Errorf("callCount = %d, want 2", callCount)
	}
	if sugg.CronExpr != "0 9 * * *" {
		t.Errorf("sugg.CronExpr = %q, want '0 9 * * *'", sugg.CronExpr)
	}
}

func TestOrchestrator_InterpretQuery_Success(t *testing.T) {
	mock := &mockProvider{
		name:         "mock",
		defaultModel: "test-model",
		generateFunc: func(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
			return &GenerateResponse{
				Text: `{"filters": [{"column": "status", "operator": "eq", "value": "failed"}], "since_relative": "24h"}`,
			}, nil
		},
	}

	orch := NewOrchestratorWithProvider(mock)
	filters, err := orch.InterpretDashboardQuery(context.Background(), "failed tasks")
	if err != nil {
		t.Fatalf("InterpretDashboardQuery() error = %v, want nil", err)
	}

	if len(filters) != 1 {
		t.Fatalf("len(filters) = %d, want 1", len(filters))
	}
	if filters[0].Column != "status" || filters[0].Value != "failed" {
		t.Errorf("unexpected filter: %+v", filters[0])
	}
}

func TestOrchestrator_InterpretQuery_RejectsNonAllowlistedColumn(t *testing.T) {
	mock := &mockProvider{
		name:         "mock",
		defaultModel: "test-model",
		generateFunc: func(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
			return &GenerateResponse{
				Text: `{"filters": [{"column": "drop table tasks;", "operator": "eq", "value": "evil"}], "since_relative": ""}`,
			}, nil
		},
	}

	orch := NewOrchestratorWithProvider(mock)
	_, err := orch.InterpretDashboardQuery(context.Background(), "malicious query")
	if err == nil {
		t.Fatal("InterpretDashboardQuery() accepted malicious column, want error")
	}
}
