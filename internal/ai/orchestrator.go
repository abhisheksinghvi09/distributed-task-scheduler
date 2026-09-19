package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/abhisheksinghvi09/task-scheduler/internal/schedule"
	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Orchestrator coordinates LLM provider interactions, safety constraints,
// tenant budgets, structured schema extraction, and task queue registration.
type Orchestrator struct {
	provider LLMProvider
}

// NewOrchestrator creates an Orchestrator inspecting environment variables:
// 1. GEMINI_API_KEY takes precedence.
// 2. ANTHROPIC_API_KEY is used as fallback if configured.
// If neither is present, an unconfigured orchestrator is returned (safe 501s).
func NewOrchestrator() *Orchestrator {
	if geminiKey := os.Getenv("GEMINI_API_KEY"); geminiKey != "" {
		return NewOrchestratorWithProvider(NewGeminiProvider(geminiKey))
	}
	if anthropicKey := os.Getenv("ANTHROPIC_API_KEY"); anthropicKey != "" {
		return NewOrchestratorWithProvider(NewAnthropicProvider(anthropicKey))
	}
	return &Orchestrator{provider: nil}
}

// NewOrchestratorWithProvider initializes an Orchestrator with an explicit provider.
func NewOrchestratorWithProvider(p LLMProvider) *Orchestrator {
	return &Orchestrator{provider: p}
}

// IsConfigured returns true if a valid LLM provider is configured.
func (o *Orchestrator) IsConfigured() bool {
	return o.provider != nil
}

// ProviderName returns the name of the underlying active provider.
func (o *Orchestrator) ProviderName() string {
	if o.provider == nil {
		return ""
	}
	return o.provider.Name()
}

// DefaultModel returns the default model for the configured provider.
func (o *Orchestrator) DefaultModel() string {
	if o.provider == nil {
		return ""
	}
	return o.provider.DefaultModel()
}

// RegisterTasks wires "llm_task" and "ai_triage" into the scheduler's task registry.
func (o *Orchestrator) RegisterTasks(db *pgxpool.Pool) {
	if !o.IsConfigured() {
		return
	}
	task.Register("llm_task", func(ctx context.Context, raw json.RawMessage) error {
		return o.ExecuteLLMTask(ctx, db, raw)
	})
	task.Register("ai_triage", func(ctx context.Context, raw json.RawMessage) error {
		return o.ExecuteTriage(ctx, db)
	})
}

// ExecuteLLMTask validates budget, limits, executes against the provider,
// and records usage/spend in Postgres.
func (o *Orchestrator) ExecuteLLMTask(ctx context.Context, db *pgxpool.Pool, raw json.RawMessage) error {
	if !o.IsConfigured() {
		return &task.ErrPermanent{Err: errors.New("ai: no LLM provider configured")}
	}

	var p llmTaskPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return &task.ErrPermanent{Err: fmt.Errorf("llm_task: invalid payload: %w", err)}
	}
	if p.Prompt == "" {
		return &task.ErrPermanent{Err: errors.New("llm_task: prompt is required")}
	}
	if len(p.Prompt) > 100_000 {
		return &task.ErrPermanent{Err: errors.New("llm_task: prompt exceeds maximum length of 100,000 characters")}
	}

	if err := CheckBudget(ctx, db, p.TenantID); err != nil {
		return &task.ErrPermanent{Err: err}
	}

	model := p.Model
	if model == "" {
		model = o.DefaultModel()
	}

	resp, err := o.provider.Generate(ctx, GenerateRequest{
		Model:     model,
		System:    p.System,
		Prompt:    p.Prompt,
		MaxTokens: p.MaxTokens,
	})
	if err != nil {
		return err
	}

	cost, err := CostMicrocents(model, resp.Usage)
	if err != nil {
		cost = 0 // pricing gap should not fail a successful generation
	}

	if err := RecordSpend(ctx, db, p.TenantID, cost); err != nil {
		return fmt.Errorf("llm_task: record spend: %w", err)
	}

	if taskID, ok := task.TaskIDFromContext(ctx); ok {
		if err := recordTaskUsage(ctx, db, taskID, resp.Usage, cost); err != nil {
			return fmt.Errorf("llm_task: record usage: %w", err)
		}
	}

	return nil
}

func recordTaskUsage(ctx context.Context, db *pgxpool.Pool, taskID string, usage Usage, costMicrocents int64) error {
	_, err := db.Exec(ctx, `
UPDATE tasks SET input_tokens = $2, output_tokens = $3, cache_read_tokens = $4, cost_microcents = $5
WHERE id = $1`,
		taskID, usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens, costMicrocents)
	return err
}

// ExecuteTriage summarizes dead-lettered failures and stores the report.
func (o *Orchestrator) ExecuteTriage(ctx context.Context, db *pgxpool.Pool) error {
	if !o.IsConfigured() {
		return &task.ErrPermanent{Err: errors.New("ai: no LLM provider configured")}
	}

	errMsgs, err := recentDeadLetterErrors(ctx, db, 200)
	if err != nil {
		return fmt.Errorf("ai_triage: fetch dead-lettered errors: %w", err)
	}
	if len(errMsgs) == 0 {
		return nil
	}

	prompt := "Failure messages from the last batch of dead-lettered tasks:\n\n" + strings.Join(errMsgs, "\n---\n")
	resp, err := o.provider.Generate(ctx, GenerateRequest{
		System: triageSystemPrompt,
		Prompt: prompt,
	})
	if err != nil {
		return err
	}

	if strings.TrimSpace(resp.Text) == "" {
		return errors.New("ai_triage: model returned empty summary")
	}

	_, err = db.Exec(ctx, "INSERT INTO triage_reports (summary, task_count) VALUES ($1, $2)", resp.Text, len(errMsgs))
	if err != nil {
		return fmt.Errorf("ai_triage: store report: %w", err)
	}
	return nil
}

// SuggestCron translates a natural language schedule into a validated CronSuggestion.
func (o *Orchestrator) SuggestCron(ctx context.Context, description string) (*CronSuggestion, error) {
	if !o.IsConfigured() {
		return nil, errors.New("ai: no LLM provider configured")
	}

	schema := map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"cron_expr":   map[string]any{"type": "STRING", "description": "Standard 5-field cron expression, e.g. '0 9 * * 1-5'."},
			"timezone":    map[string]any{"type": "STRING", "description": "IANA timezone name, e.g. 'America/New_York'. Default 'UTC'."},
			"explanation": map[string]any{"type": "STRING", "description": "One sentence description."},
		},
		"required": []string{"cron_expr", "timezone", "explanation"},
	}

	systemInstruction := "You translate natural-language schedule descriptions into a standard 5-field cron expression, IANA timezone, and a brief explanation. Return ONLY a JSON object."
	prompt := fmt.Sprintf("Translate this schedule description into a cron expression: %q", description)

	for attempt := 0; attempt < 2; attempt++ {
		resp, err := o.provider.Generate(ctx, GenerateRequest{
			System:         systemInstruction,
			Prompt:         prompt,
			ResponseSchema: schema,
		})
		if err != nil {
			return nil, err
		}

		var input struct {
			CronExpr    string `json:"cron_expr"`
			Timezone    string `json:"timezone"`
			Explanation string `json:"explanation"`
		}

		clean := cleanJSON(resp.Text)
		if err := json.Unmarshal([]byte(clean), &input); err != nil {
			prompt = fmt.Sprintf("Translate this schedule description into a cron expression: %q. Previous attempt was not valid JSON: %v. Output valid JSON.", description, err)
			continue
		}

		if _, err := schedule.ValidateCronExpr(input.CronExpr); err != nil {
			prompt = fmt.Sprintf("Translate this schedule description into a cron expression: %q. Previous cron expression %q was invalid: %v. Try again.", description, input.CronExpr, err)
			continue
		}
		if _, err := schedule.ValidateTimezone(input.Timezone); err != nil {
			prompt = fmt.Sprintf("Translate this schedule description into a cron expression: %q. Previous timezone %q was invalid: %v. Try again.", description, input.Timezone, err)
			continue
		}

		return &CronSuggestion{
			CronExpr:    input.CronExpr,
			Timezone:    input.Timezone,
			Explanation: input.Explanation,
		}, nil
	}

	return nil, fmt.Errorf("ai: could not produce a valid cron expression for %q after retry", description)
}

// InterpretDashboardQuery turns a free-text dashboard search into safe QueryFilters.
func (o *Orchestrator) InterpretDashboardQuery(ctx context.Context, query string) ([]QueryFilter, error) {
	if !o.IsConfigured() {
		return nil, errors.New("ai: no LLM provider configured")
	}

	schema := map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"filters": map[string]any{
				"type": "ARRAY",
				"items": map[string]any{
					"type": "OBJECT",
					"properties": map[string]any{
						"column":   map[string]any{"type": "STRING"},
						"operator": map[string]any{"type": "STRING"},
						"value":    map[string]any{"type": "STRING"},
					},
					"required": []string{"column", "operator", "value"},
				},
			},
			"since_relative": map[string]any{"type": "STRING"},
		},
		"required": []string{"filters", "since_relative"},
	}

	systemInstruction := "Extract structured filters from a natural-language task search. Only use column names: status, task_type, tenant_id. Only use operators: eq, gt, lt, gte, lte. Return ONLY valid JSON."
	resp, err := o.provider.Generate(ctx, GenerateRequest{
		System:         systemInstruction,
		Prompt:         query,
		ResponseSchema: schema,
	})
	if err != nil {
		return nil, err
	}

	var input struct {
		Filters       []QueryFilter `json:"filters"`
		SinceRelative string        `json:"since_relative"`
	}

	clean := cleanJSON(resp.Text)
	if err := json.Unmarshal([]byte(clean), &input); err != nil {
		return nil, &task.ErrPermanent{Err: fmt.Errorf("ai: parse filter response: %w", err)}
	}

	return ValidateQueryFilters(input.Filters)
}

func cleanJSON(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}
