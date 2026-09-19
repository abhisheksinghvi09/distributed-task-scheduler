package task

import "context"

type taskIDKey struct{}

// WithTaskID attaches the executing task's id to ctx so a handler can
// reference it -- for logging, or (as in the chaos test's ledger handler)
// to record its own identity without threading it through every payload.
func WithTaskID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, taskIDKey{}, id)
}

// TaskIDFromContext returns the id set by WithTaskID, if any.
func TaskIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(taskIDKey{}).(string)
	return id, ok
}
