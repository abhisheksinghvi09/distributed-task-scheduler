package ai

import (
	"context"
)

// CronSuggestion is the validated result of translating a natural-language
// schedule description into a cron expression.
type CronSuggestion struct {
	CronExpr    string `json:"cron_expr"`
	Timezone    string `json:"timezone"`
	Explanation string `json:"explanation"` // plain-English readback, shown before saving
}

// SuggestCron translates a natural-language schedule description into a cron
// expression using the default Orchestrator.
func SuggestCron(ctx context.Context, description string) (*CronSuggestion, error) {
	return NewOrchestrator().SuggestCron(ctx, description)
}
