// Package coordinator relays pending tasks onto NATS JetStream and recovers
// work abandoned by a dead worker or a lost dispatch message. Postgres is
// the sole source of truth for every task's state; JetStream is a wake-up
// signal carrying a task id, never the payload, never the claim -- see
// internal/queue and internal/task for the full architecture rationale.
package coordinator

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/common"
	"github.com/abhisheksinghvi09/task-scheduler/internal/db"
	"github.com/abhisheksinghvi09/task-scheduler/internal/metrics"
	"github.com/abhisheksinghvi09/task-scheduler/internal/queue"
	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	relayInterval      = 1 * time.Second
	relayBatchSize     = 100
	reapInterval       = 10 * time.Second
	queueDepthInterval = 5 * time.Second
	cronInterval       = 1 * time.Second
	cronAdvisoryLock   = 424243
	// defaultQueuedGrace is how long a task may sit 'queued' (published,
	// not yet claimed) before the reaper assumes the dispatch was lost and
	// returns it to pending. Kept on the same order as NATS's own AckWait
	// (see internal/queue), not several times longer -- ClaimByID's atomic
	// "WHERE status='queued'" check makes it safe for this reaper pass and
	// a stale, still-in-flight redelivery to race: whichever arrives second
	// simply finds the row no longer claimable and is a no-op.
	defaultQueuedGrace = 60 * time.Second
	reaperAdvisoryLock = 424242
)

type CoordinatorServer struct {
	dbConnectionString string
	natsURL            string
	httpAddr           string
	queuedGrace        time.Duration
	dbPool             *pgxpool.Pool
	natsClient         *queue.Client
	httpServer         *http.Server
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
}

// NewServer initializes and returns a new Server instance.
func NewServer(dbConnectionString, natsURL, httpAddr string, queuedGrace time.Duration) *CoordinatorServer {
	ctx, cancel := context.WithCancel(context.Background())
	if queuedGrace <= 0 {
		queuedGrace = defaultQueuedGrace
	}
	return &CoordinatorServer{
		dbConnectionString: dbConnectionString,
		natsURL:            natsURL,
		httpAddr:           httpAddr,
		queuedGrace:        queuedGrace,
		ctx:                ctx,
		cancel:             cancel,
	}
}

// Start initiates the server's operation.
func (s *CoordinatorServer) Start() error {
	var err error
	s.dbPool, err = common.ConnectToDatabase(s.ctx, s.dbConnectionString)
	if err != nil {
		return err
	}

	if err := db.Migrate(s.ctx, s.dbPool); err != nil {
		return err
	}

	s.natsClient, err = queue.Connect(s.ctx, s.natsURL)
	if err != nil {
		return err
	}
	if err := s.natsClient.EnsureStream(s.ctx); err != nil {
		return err
	}

	s.startHTTPServer()
	go s.relayLoop()
	go s.reaperLoop()
	go s.queueDepthLoop()
	go s.cronLoop()

	return s.awaitShutdown()
}

func (s *CoordinatorServer) startHTTPServer() {
	mux := metrics.Mux(metrics.DBReady(s.dbPool))
	s.httpServer = &http.Server{Addr: s.httpAddr, Handler: mux}
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("coordinator: metrics server failed", "error", err)
		}
	}()
	slog.Info("coordinator: metrics server listening", "addr", s.httpAddr)
}

func (s *CoordinatorServer) awaitShutdown() error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	return s.Stop()
}

// Stop gracefully shuts down the server.
func (s *CoordinatorServer) Stop() error {
	s.cancel()
	s.wg.Wait()

	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.httpServer.Shutdown(ctx)
	}
	if s.natsClient != nil {
		s.natsClient.Close()
	}
	if s.dbPool != nil {
		s.dbPool.Close()
	}

	return nil
}

// relayLoop moves eligible tasks from pending to queued and publishes each
// to NATS. Publish happens after MarkQueued's transaction has committed,
// never inside it. A publish failure leaves the row in queued; the
// reaper's queued-timeout pass returns it to pending within one reaper
// interval -- a bounded, self-healing gap, not a bug to solve with a
// distributed transaction.
func (s *CoordinatorServer) relayLoop() {
	s.wg.Add(1)
	defer s.wg.Done()

	ticker := time.NewTicker(relayInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.relayOnce()
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *CoordinatorServer) relayOnce() {
	claimed, err := task.MarkQueued(s.ctx, s.dbPool, relayBatchSize)
	if err != nil {
		slog.Error("relay: mark queued failed", "error", err)
		return
	}

	for _, c := range claimed {
		if err := s.natsClient.Publish(s.ctx, c.Priority, c.ID, c.TaskType); err != nil {
			slog.Warn("relay: publish failed", "task_id", c.ID, "error", err)
			metrics.NATSPublishErrorsTotal.Inc()
			if _, unqErr := task.Unqueue(s.ctx, s.dbPool, c.ID); unqErr != nil {
				slog.Error("relay: failed to unqueue task after publish failure", "task_id", c.ID, "error", unqErr)
			}
		}
	}
}

// reaperLoop recovers tasks abandoned by a dead worker (expired lease) and
// tasks queued for dispatch but never claimed -- a lost or failed publish.
// Guarded by a Postgres advisory lock so multiple coordinator instances
// don't reap the same rows redundantly.
func (s *CoordinatorServer) reaperLoop() {
	s.wg.Add(1)
	defer s.wg.Done()

	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.reapOnce()
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *CoordinatorServer) reapOnce() {
	start := time.Now()

	var locked bool
	if err := s.dbPool.QueryRow(s.ctx, "SELECT pg_try_advisory_lock($1)", reaperAdvisoryLock).Scan(&locked); err != nil {
		slog.Error("reaper: failed to acquire advisory lock", "error", err)
		return
	}
	if !locked {
		return
	}
	defer s.dbPool.Exec(s.ctx, "SELECT pg_advisory_unlock($1)", reaperAdvisoryLock)

	result, err := task.ReapExpired(s.ctx, s.dbPool, s.queuedGrace)
	metrics.ReaperRunDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		slog.Error("reaper", "error", err)
		return
	}

	if result.RequeuedFromRunning > 0 {
		metrics.ReaperReclaimedTotal.WithLabelValues("running").Add(float64(result.RequeuedFromRunning))
	}
	if result.RequeuedFromQueued > 0 {
		metrics.ReaperReclaimedTotal.WithLabelValues("queued").Add(float64(result.RequeuedFromQueued))
	}
	if result.DeadLettered > 0 {
		metrics.ReaperDeadLetteredTotal.Add(float64(result.DeadLettered))
	}
	if result.RequeuedFromRunning > 0 || result.RequeuedFromQueued > 0 || result.DeadLettered > 0 {
		slog.Info("reaper recovered tasks",
			"requeued_from_running", result.RequeuedFromRunning,
			"requeued_from_queued", result.RequeuedFromQueued,
			"dead_lettered", result.DeadLettered)
	}

	blocked, err := task.BlockOrphaned(s.ctx, s.dbPool)
	if err != nil {
		slog.Error("reaper: block orphaned dependents", "error", err)
		return
	}
	if blocked > 0 {
		slog.Info("reaper blocked tasks with a failed dependency", "count", blocked)
	}
}

func (s *CoordinatorServer) queueDepthLoop() {
	s.wg.Add(1)
	defer s.wg.Done()

	ticker := time.NewTicker(queueDepthInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			metrics.RefreshQueueDepth(s.ctx, s.dbPool)
		case <-s.ctx.Done():
			return
		}
	}
}
