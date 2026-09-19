package task

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrUnknownType is returned by Run when no handler is registered for the
// task's type -- e.g. a "shell" task submitted while ALLOW_SHELL_TASKS is
// unset.
var ErrUnknownType = errors.New("task: unknown task type")

// Handler executes one task attempt. ctx carries the per-task logger and
// is cancelled when the task's timeout elapses, its lease is lost, or a
// cancellation is requested -- handlers must respect ctx.Done().
type Handler func(ctx context.Context, payload json.RawMessage) error

var registry = map[string]Handler{}

// Register adds a handler for a task type. Called from each handler's
// init(), never at runtime -- the registry is fixed at process start.
func Register(taskType string, h Handler) {
	registry[taskType] = h
}

// Run dispatches to the registered handler for typ, or ErrUnknownType if
// none is registered.
func Run(ctx context.Context, typ string, payload json.RawMessage) error {
	h, ok := registry[typ]
	if !ok {
		return ErrUnknownType
	}
	return h(ctx, payload)
}
