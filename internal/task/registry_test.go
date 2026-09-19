package task

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestRegistry_RunsRegisteredHandler(t *testing.T) {
	const typ = "test_registry_echo"
	called := false
	Register(typ, func(ctx context.Context, payload json.RawMessage) error {
		called = true
		return nil
	})
	defer delete(registry, typ)

	if err := Run(context.Background(), typ, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if !called {
		t.Fatal("registered handler was not called")
	}
}

func TestRegistry_UnknownType(t *testing.T) {
	err := Run(context.Background(), "test_registry_does_not_exist", json.RawMessage(`{}`))
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("Run() error = %v, want ErrUnknownType", err)
	}
}
