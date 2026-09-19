package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// modelRates holds $/1M-token pricing as microcents-per-token, integer
// math throughout -- this is money, and float accumulation error compounds
// silently across millions of tasks. 1 microcent = 1/100000 of a dollar.
// Cache-read tokens are billed at a lower rate than fresh input.
type modelRate struct {
	inputPerToken     int64 // microcents per input token
	outputPerToken    int64 // microcents per output token
	cacheReadPerToken int64 // microcents per cache-read token
}

var modelRates = map[string]modelRate{
	// claude-opus-5: $5/1M input, $25/1M output. Cache reads priced at
	// 10% of fresh input, matching Anthropic's standard cache discount.
	"claude-opus-5": {
		inputPerToken:     500,
		outputPerToken:    2500,
		cacheReadPerToken: 50,
	},
	// gemini-3.5-flash-lite: $0.075/1M input, $0.30/1M output, $0.02/1M cache
	"gemini-3.5-flash-lite": {
		inputPerToken:     8,
		outputPerToken:    30,
		cacheReadPerToken: 2,
	},
	// gemini-3.6-flash: $0.10/1M input, $0.40/1M output, $0.025/1M cache
	"gemini-3.6-flash": {
		inputPerToken:     10,
		outputPerToken:    40,
		cacheReadPerToken: 3,
	},
	// gemini-2.5-pro: $1.25/1M input, $5.00/1M output, $0.31/1M cache
	"gemini-2.5-pro": {
		inputPerToken:     125,
		outputPerToken:    500,
		cacheReadPerToken: 31,
	},
	// gemini-3.1-pro: $1.25/1M input, $5.00/1M output, $0.31/1M cache
	"gemini-3.1-pro": {
		inputPerToken:     125,
		outputPerToken:    500,
		cacheReadPerToken: 31,
	},
	// Aliases
	"gemini-flash-latest": {
		inputPerToken:     10,
		outputPerToken:    40,
		cacheReadPerToken: 3,
	},
	"gemini-pro-latest": {
		inputPerToken:     125,
		outputPerToken:    500,
		cacheReadPerToken: 31,
	},
}

// CostMicrocents computes the exact integer cost of one API call's usage
// against the given model's rate. Returns an error for an unknown model
// rather than silently charging zero -- a missing rate entry must be
// visible, not a quiet undercount.
func CostMicrocents(model string, usage Usage) (int64, error) {
	rate, ok := modelRates[model]
	if !ok {
		return 0, fmt.Errorf("ai: no pricing entry for model %q", model)
	}
	return usage.InputTokens*rate.inputPerToken +
		usage.OutputTokens*rate.outputPerToken +
		usage.CacheReadInputTokens*rate.cacheReadPerToken, nil
}

// ErrBudgetExceeded is returned by CheckBudget when a tenant's monthly
// spend would exceed its configured limit.
type ErrBudgetExceeded struct {
	TenantID   string
	SpentSoFar int64
	Limit      int64
}

func (e *ErrBudgetExceeded) Error() string {
	return fmt.Sprintf("ai: tenant %s budget exceeded (spent %d of %d microcents this month)", e.TenantID, e.SpentSoFar, e.Limit)
}

// CheckBudget enforces a tenant's monthly budget at enqueue time, not at
// execution: rejecting here is a 402 with nothing wasted, whereas
// rejecting after claim/dispatch/retry has already burned real work to
// discover the tenant is out of budget. Tenants with no configured budget
// row are unlimited by default -- a budget is an opt-in cap, not a
// mandatory quota.
func CheckBudget(ctx context.Context, db *pgxpool.Pool, tenantID string) error {
	if tenantID == "" {
		return nil // unscoped submissions (legacy path) are never budget-limited
	}

	month := firstOfMonth(time.Now())
	var limit, spent int64
	// limit_microcents > 0 excludes the zero-limit row RecordSpend
	// auto-creates for visibility on first use -- that row must never be
	// mistaken for an explicit zero-budget cap.
	err := db.QueryRow(ctx,
		"SELECT limit_microcents, spent_microcents FROM tenant_budgets WHERE tenant_id = $1 AND month = $2 AND limit_microcents > 0",
		tenantID, month,
	).Scan(&limit, &spent)
	if err != nil {
		return nil // no budget cap configured: unlimited
	}
	if spent >= limit {
		return &ErrBudgetExceeded{TenantID: tenantID, SpentSoFar: spent, Limit: limit}
	}
	return nil
}

// RecordSpend adds cost to a tenant's current-month spend, creating the
// row on first use with a zero limit removed -- RecordSpend never creates
// a budget cap, only CheckBudget's caller (an admin-set limit) does, so a
// tenant with no configured budget accrues spend for visibility without
// ever being blocked by it.
func RecordSpend(ctx context.Context, db *pgxpool.Pool, tenantID string, microcents int64) error {
	if tenantID == "" || microcents == 0 {
		return nil
	}
	month := firstOfMonth(time.Now())
	_, err := db.Exec(ctx, `
INSERT INTO tenant_budgets (tenant_id, month, limit_microcents, spent_microcents)
VALUES ($1, $2, 0, $3)
ON CONFLICT (tenant_id, month) DO UPDATE SET spent_microcents = tenant_budgets.spent_microcents + $3`,
		tenantID, month, microcents)
	if err != nil {
		return fmt.Errorf("record spend for tenant %s: %w", tenantID, err)
	}
	return nil
}

func firstOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}
