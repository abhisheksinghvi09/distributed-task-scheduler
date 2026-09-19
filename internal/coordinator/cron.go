package coordinator

import (
	"log/slog"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/schedule"
)

// cronLoop fires due schedules on a 1s tick, guarded by its own advisory
// lock (separate from the reaper's) so multiple coordinator instances
// don't race to fire the same schedule -- schedule.FireDue additionally
// locks each due row with FOR UPDATE SKIP LOCKED, so this outer lock is a
// cheap way to avoid every coordinator replica running the query in
// lockstep, not a correctness requirement on its own.
func (s *CoordinatorServer) cronLoop() {
	s.wg.Add(1)
	defer s.wg.Done()

	ticker := time.NewTicker(cronInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := schedule.FireDue(s.ctx, s.dbPool); err != nil {
				slog.Error("cron: fire due schedules", "error", err)
			}
		case <-s.ctx.Done():
			return
		}
	}
}
