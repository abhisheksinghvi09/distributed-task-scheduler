package ai

import (
	"context"
	"fmt"
)

// allowedQueryColumns bounds what a natural-language dashboard query can
// ever filter on. This -- not prompt wording -- is what makes the feature
// safe: the model produces filter *parameters*, never SQL, and only
// columns named here can ever reach a query, no matter what the model (or
// a task payload it might have read) says.
var allowedQueryColumns = map[string]bool{
	"status": true, "task_type": true, "tenant_id": true,
}

var allowedQueryOperators = map[string]bool{"eq": true, "gt": true, "lt": true, "gte": true, "lte": true}

// QueryFilter is a single, validated column/operator/value triple -- the
// same shape internal/task.ListFilter or a direct SQL WHERE builder can
// consume safely, since every field is checked against a fixed allowlist
// before this type is ever constructed.
type QueryFilter struct {
	Column   string `json:"column"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// InterpretDashboardQuery turns a free-text dashboard search into safe QueryFilters
// using the default Orchestrator.
func InterpretDashboardQuery(ctx context.Context, query string) ([]QueryFilter, error) {
	return NewOrchestrator().InterpretDashboardQuery(ctx, query)
}

// ValidateQueryFilters re-checks every filter against the allowlist even
// though strict schema constraints already constrain the model's output --
// this function is the actual server-side boundary.
func ValidateQueryFilters(filters []QueryFilter) ([]QueryFilter, error) {
	for _, f := range filters {
		if !allowedQueryColumns[f.Column] {
			return nil, fmt.Errorf("ai: column %q is not in the query allowlist", f.Column)
		}
		if !allowedQueryOperators[f.Operator] {
			return nil, fmt.Errorf("ai: operator %q is not in the query allowlist", f.Operator)
		}
	}
	return filters, nil
}
