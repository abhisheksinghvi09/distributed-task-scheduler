package ai

import (
	"context"
	"encoding/json"

	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/jackc/pgx/v5/pgxpool"
)

// llmTaskPayload is the "llm_task" task type's payload shape.
type llmTaskPayload struct {
	Model     string `json:"model"`  // "" uses DefaultModel
	System    string `json:"system"` // "" omits the system prompt
	Prompt    string `json:"prompt"`
	MaxTokens int64  `json:"max_tokens"` // 0 uses provider default
	TenantID  string `json:"tenant_id"`  // for cost attribution and budget enforcement
}

// RegisterLLMTask wires the "llm_task" handler into the task registry.
func RegisterLLMTask(db *pgxpool.Pool) {
	orch := NewOrchestrator()
	if !orch.IsConfigured() {
		return
	}
	task.Register("llm_task", func(ctx context.Context, raw json.RawMessage) error {
		return orch.ExecuteLLMTask(ctx, db, raw)
	})
}
