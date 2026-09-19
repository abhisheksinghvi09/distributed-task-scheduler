//go:build integration

package auth

import (
	"context"
	"testing"

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

func TestGenerateVerify_RoundTrips(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.NewString()

	rawKey, keyID, err := Generate(ctx, pool, tenantID, "test key")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if keyID == "" {
		t.Fatal("Generate() returned empty key id")
	}

	gotTenant, err := Verify(ctx, pool, rawKey)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if gotTenant != tenantID {
		t.Errorf("Verify() tenant = %q, want %q", gotTenant, tenantID)
	}
}

func TestVerify_WrongKeyRejected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, _, err := Generate(ctx, pool, uuid.NewString(), "test key"); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if _, err := Verify(ctx, pool, KeyHeaderPrefix+"0000000000000000000000000000000000000000000000"); err != ErrInvalidKey {
		t.Fatalf("Verify() error = %v, want ErrInvalidKey", err)
	}
}

func TestVerify_RevokedKeyRejected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	rawKey, keyID, err := Generate(ctx, pool, uuid.NewString(), "test key")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := Verify(ctx, pool, rawKey); err != nil {
		t.Fatalf("Verify (pre-revoke): %v", err)
	}

	if err := Revoke(ctx, pool, keyID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if _, err := Verify(ctx, pool, rawKey); err != ErrInvalidKey {
		t.Fatalf("Verify() after revoke error = %v, want ErrInvalidKey", err)
	}
}
