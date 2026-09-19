//go:build integration

package ai

import (
	"context"
	"testing"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/common"
	"github.com/abhisheksinghvi09/task-scheduler/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := common.ConnectToDatabase(context.Background(), common.GetDBConnectionString())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestCheckBudget_UnconfiguredTenantIsUnlimited(t *testing.T) {
	pool := testPool(t)
	if err := CheckBudget(context.Background(), pool, uuid.NewString()); err != nil {
		t.Fatalf("CheckBudget() for a tenant with no budget row = %v, want nil (unlimited)", err)
	}
}

func TestCheckBudget_EmptyTenantNeverLimited(t *testing.T) {
	if err := CheckBudget(context.Background(), nil, ""); err != nil {
		t.Fatalf("CheckBudget(\"\") = %v, want nil", err)
	}
}

func TestRecordSpend_DoesNotCreateAnImplicitCap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.NewString()

	// Recording spend with no configured limit must never cause a
	// subsequent CheckBudget to reject -- see the CheckBudget comment
	// about the zero-limit row this creates for visibility only.
	if err := RecordSpend(ctx, pool, tenantID, 5_000_000); err != nil {
		t.Fatalf("RecordSpend: %v", err)
	}
	if err := CheckBudget(ctx, pool, tenantID); err != nil {
		t.Fatalf("CheckBudget() after RecordSpend with no configured limit = %v, want nil", err)
	}
}

func TestCheckBudget_RejectsWhenLimitReached(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.NewString()

	month := firstOfMonth(time.Now())
	if _, err := pool.Exec(ctx,
		"INSERT INTO tenant_budgets (tenant_id, month, limit_microcents, spent_microcents) VALUES ($1, $2, 1000, 1000)",
		tenantID, month); err != nil {
		t.Fatalf("seed budget: %v", err)
	}

	err := CheckBudget(ctx, pool, tenantID)
	if err == nil {
		t.Fatal("CheckBudget() with spent==limit returned nil, want ErrBudgetExceeded")
	}
	var budgetErr *ErrBudgetExceeded
	if got, ok := err.(*ErrBudgetExceeded); !ok {
		t.Fatalf("CheckBudget() error type = %T, want *ErrBudgetExceeded", err)
	} else {
		budgetErr = got
	}
	if budgetErr.TenantID != tenantID {
		t.Fatalf("ErrBudgetExceeded.TenantID = %q, want %q", budgetErr.TenantID, tenantID)
	}
}

func TestCheckBudget_AllowsUnderLimit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.NewString()

	month := firstOfMonth(time.Now())
	if _, err := pool.Exec(ctx,
		"INSERT INTO tenant_budgets (tenant_id, month, limit_microcents, spent_microcents) VALUES ($1, $2, 1000, 500)",
		tenantID, month); err != nil {
		t.Fatalf("seed budget: %v", err)
	}

	if err := CheckBudget(ctx, pool, tenantID); err != nil {
		t.Fatalf("CheckBudget() under limit = %v, want nil", err)
	}
}

func TestRecordSpend_AccumulatesAcrossCalls(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	month := firstOfMonth(time.Now())

	if _, err := pool.Exec(ctx,
		"INSERT INTO tenant_budgets (tenant_id, month, limit_microcents, spent_microcents) VALUES ($1, $2, 1000000, 0)",
		tenantID, month); err != nil {
		t.Fatalf("seed budget: %v", err)
	}

	if err := RecordSpend(ctx, pool, tenantID, 100); err != nil {
		t.Fatalf("RecordSpend (1): %v", err)
	}
	if err := RecordSpend(ctx, pool, tenantID, 250); err != nil {
		t.Fatalf("RecordSpend (2): %v", err)
	}

	var spent int64
	if err := pool.QueryRow(ctx, "SELECT spent_microcents FROM tenant_budgets WHERE tenant_id = $1 AND month = $2", tenantID, month).Scan(&spent); err != nil {
		t.Fatalf("query spent: %v", err)
	}
	if spent != 350 {
		t.Fatalf("spent_microcents = %d, want 350", spent)
	}
}
