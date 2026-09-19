// Package worker pulls task-id notifications from NATS JetStream, claims
// each one directly against Postgres, executes it, and reports its own
// terminal status. A worker owns nothing that survives its own process --
// Postgres is the sole source of truth for what "claimed" means, so
// duplicate delivery from JetStream is harmless: a second claim attempt on
// an already-running task simply matches zero rows.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/common"
	"github.com/abhisheksinghvi09/task-scheduler/internal/db"
	"github.com/abhisheksinghvi09/task-scheduler/internal/metrics"
	"github.com/abhisheksinghvi09/task-scheduler/internal/queue"
	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	workerPoolSize     = 5
	defaultTaskTimeout = 60 * time.Second
	drainTimeout       = 30 * time.Second
	minRenewInterval   = 1 * time.Second
	// fetchWait is deliberately short, not a generous poll interval: with
	// tiers checked strictly high->default->low, every empty higher tier
	// costs one full fetchWait before falling through to the tier that
	// actually has work. A long wait here directly taxes throughput
	// whenever most traffic isn't on the highest tier -- which is the
	// common case, since "default" is the default.
	fetchWait = 100 * time.Millisecond
)

// tiers is checked in order on every pull iteration -- high before default
// before low -- so a worker with idle capacity always prefers
// higher-priority work when both are available.
var tiers = []queue.Tier{queue.TierHigh, queue.TierDefault, queue.TierLow}

// WorkerServer pulls tasks from NATS and executes them against Postgres.
type WorkerServer struct {
	id                 uint32
	dbConnectionString string
	natsURL            string
	httpAddr           string
	leaseDuration      time.Duration
	dbPool             *pgxpool.Pool
	natsClient         *queue.Client
	consumers          map[queue.Tier]jetstream.Consumer
	httpServer         *http.Server
	draining           bool
	drainingMu         sync.RWMutex
	activeTasks        map[string]context.CancelFunc
	activeTasksMutex   sync.Mutex
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup

	// OnConnected, if set, runs once the database pool exists and has been
	// migrated, before any pull loop starts -- the hook a caller uses to
	// register handlers (e.g. internal/ai's llm_task) that need the pool
	// at registration time, without internal/task's registry (a plain
	// package-level map populated by each handler's own init()) needing
	// to know about per-handler dependencies.
	OnConnected func(*pgxpool.Pool)
}

// NewServer creates and returns a new WorkerServer.
func NewServer(dbConnectionString, natsURL, httpAddr string, leaseDuration time.Duration) *WorkerServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkerServer{
		id:                 uuid.New().ID(),
		dbConnectionString: dbConnectionString,
		natsURL:            natsURL,
		httpAddr:           httpAddr,
		leaseDuration:      leaseDuration,
		consumers:          make(map[queue.Tier]jetstream.Consumer),
		activeTasks:        make(map[string]context.CancelFunc),
		ctx:                ctx,
		cancel:             cancel,
	}
}

func (w *WorkerServer) workerID() string { return fmt.Sprintf("worker-%d", w.id) }

func (w *WorkerServer) isDraining() bool {
	w.drainingMu.RLock()
	defer w.drainingMu.RUnlock()
	return w.draining
}

// Start connects to Postgres and NATS, then runs workerPoolSize pull loops
// until a shutdown signal arrives.
func (w *WorkerServer) Start() error {
	var err error
	w.dbPool, err = common.ConnectToDatabase(w.ctx, w.dbConnectionString)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	if err := db.Migrate(w.ctx, w.dbPool); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	if w.OnConnected != nil {
		w.OnConnected(w.dbPool)
	}

	w.natsClient, err = queue.Connect(w.ctx, w.natsURL)
	if err != nil {
		return fmt.Errorf("connect to nats: %w", err)
	}
	if err := w.natsClient.EnsureStream(w.ctx); err != nil {
		return fmt.Errorf("ensure stream: %w", err)
	}

	for _, tier := range tiers {
		cons, err := w.natsClient.EnsureConsumer(w.ctx, tier)
		if err != nil {
			return fmt.Errorf("ensure consumer: %w", err)
		}
		w.consumers[tier] = cons
	}

	w.startHTTPServer()
	slog.Info("worker ready", "worker_id", w.workerID(), "tiers", tiers)
	w.startWorkerPool(workerPoolSize)

	return w.awaitShutdown()
}

func (w *WorkerServer) startHTTPServer() {
	if w.httpAddr == "" {
		return
	}
	mux := metrics.Mux(metrics.DBReady(w.dbPool))
	w.httpServer = &http.Server{Addr: w.httpAddr, Handler: mux}
	go func() {
		if err := w.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("worker: metrics server failed", "error", err)
		}
	}()
	slog.Info("worker: metrics server listening", "addr", w.httpAddr)
}

func (w *WorkerServer) awaitShutdown() error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	return w.Stop()
}

// Stop gracefully shuts down the worker: stop pulling new work, give
// in-flight tasks drainTimeout to finish, then force-cancel and release
// anything still running. There is no claimed-but-unstarted buffer to
// drain here -- pull-based consumption only ever claims a task once a pool
// goroutine is actually free to run it immediately.
func (w *WorkerServer) Stop() error {
	w.drainingMu.Lock()
	w.draining = true
	w.drainingMu.Unlock()

	w.cancel()

	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(drainTimeout):
		slog.Warn("worker: drain timeout exceeded, force-cancelling active tasks")
		w.forceCancelActiveTasks()
		<-done
	}

	if w.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.httpServer.Shutdown(ctx)
	}
	if w.natsClient != nil {
		w.natsClient.Close()
	}
	if w.dbPool != nil {
		w.dbPool.Close()
	}
	slog.Info("worker stopped")
	return nil
}

func (w *WorkerServer) forceCancelActiveTasks() {
	w.activeTasksMutex.Lock()
	defer w.activeTasksMutex.Unlock()
	for _, cancel := range w.activeTasks {
		cancel()
	}
}

func (w *WorkerServer) startWorkerPool(numWorkers int) {
	for i := 0; i < numWorkers; i++ {
		w.wg.Add(1)
		go w.pullLoop()
	}
}

// pullLoop repeatedly tries each tier, high to low, fetching at most one
// message at a time -- one goroutine claims at most one task, so it never
// claims more than it can execute right now. This is what gives pull-based
// dispatch real backpressure without any buffer-sizing tuning.
func (w *WorkerServer) pullLoop() {
	defer w.wg.Done()

	for {
		if w.ctx.Err() != nil || w.isDraining() {
			return
		}

		msg, ok := w.fetchOne()
		if !ok {
			continue
		}
		w.handleMessage(msg)
	}
}

func (w *WorkerServer) fetchOne() (jetstream.Msg, bool) {
	for _, tier := range tiers {
		cons, ok := w.consumers[tier]
		if !ok {
			continue
		}

		batch, err := cons.Fetch(1, jetstream.FetchMaxWait(fetchWait))
		if err != nil {
			continue // timeout or transient error: try the next tier
		}
		for msg := range batch.Messages() {
			return msg, true
		}
	}
	return nil, false
}

// handleMessage claims the task the message announces. The claim, not the
// message, is the ownership token: JetStream's role ends the moment Ack is
// called, immediately after a successful (or already-claimed) resolution
// -- never after execution.
func (w *WorkerServer) handleMessage(msg jetstream.Msg) {
	var env queue.Envelope
	if err := json.Unmarshal(msg.Data(), &env); err != nil {
		slog.Error("worker: malformed envelope, dropping", "error", err)
		msg.Term()
		return
	}

	claimed, err := task.ClaimByID(w.ctx, w.dbPool, env.TaskID, w.workerID(), w.leaseDuration)
	if errors.Is(err, task.ErrAlreadyClaimed) {
		msg.Ack() // duplicate delivery or a reaper race -- not an error.
		return
	}
	if err != nil {
		slog.Warn("worker: claim failed", "task_id", env.TaskID, "error", err)
		msg.NakWithDelay(2 * time.Second)
		return
	}

	msg.Ack()
	metrics.TaskDispatchLatency.
		WithLabelValues(strconv.Itoa(claimed.Priority)).
		Observe(time.Since(claimed.ScheduledAt).Seconds())
	w.runTask(claimed)
}

// runTask executes one claimed task and reports its terminal status. The
// task's own context is independent of the worker's root context so a
// shutdown signal doesn't kill in-flight work outright -- Stop() gives it
// drainTimeout before force-cancelling.
func (w *WorkerServer) runTask(claimed task.Claimed) {
	ctx, cancel := context.WithTimeout(context.Background(), taskTimeout(claimed.Payload))
	ctx = task.WithTaskID(ctx, claimed.ID)
	w.registerActive(claimed.ID, cancel)
	defer func() {
		cancel()
		w.unregisterActive(claimed.ID)
	}()

	// wasCancelled distinguishes a user-requested cancellation from a
	// lost lease or a genuine handler error -- all three end in the same
	// ctx.Done(), but only cancellation should skip the retry path.
	var wasCancelled atomic.Bool
	renewCtx, stopRenew := context.WithCancel(context.Background())
	defer stopRenew()
	go w.renewLease(renewCtx, claimed.ID, cancel, &wasCancelled)

	start := time.Now()
	runErr := task.Run(ctx, claimed.TaskType, claimed.Payload)
	duration := time.Since(start).Seconds()

	if wasCancelled.Load() {
		metrics.TaskExecutionDuration.WithLabelValues(claimed.TaskType, "cancelled").Observe(duration)
		w.reportCancelled(claimed)
		return
	}

	if runErr != nil {
		outcome := "failure"
		if errors.Is(runErr, context.DeadlineExceeded) {
			outcome = "timeout"
		}
		metrics.TaskExecutionDuration.WithLabelValues(claimed.TaskType, outcome).Observe(duration)
		w.reportFailure(claimed, runErr, outcome == "timeout")
		return
	}

	metrics.TaskExecutionDuration.WithLabelValues(claimed.TaskType, "success").Observe(duration)
	w.reportSuccess(claimed)
}

func (w *WorkerServer) reportCancelled(claimed task.Claimed) {
	ok, err := task.MarkCancelled(context.Background(), w.dbPool, claimed.ID, claimed.Attempt)
	if err != nil {
		slog.Error("worker: failed to record cancellation", "task_id", claimed.ID, "error", err)
		return
	}
	if !ok {
		slog.Warn("worker: cancellation fenced out -- lease was lost mid-execution", "task_id", claimed.ID)
		return
	}
	slog.Info("task cancelled", "task_id", claimed.ID)
}

func (w *WorkerServer) reportFailure(claimed task.Claimed, runErr error, isTimeout bool) {
	errorClass := metrics.ClassifyError(isTimeout, false)
	metrics.TasksFailedTotal.WithLabelValues(claimed.TaskType, string(errorClass)).Inc()

	var permErr *task.ErrPermanent
	if errors.As(runErr, &permErr) {
		// No amount of retrying fixes this (a malformed request, an
		// unknown resource) -- dead-letter now rather than burn
		// max_attempts on a guaranteed repeat failure.
		if _, err := task.FailPermanent(context.Background(), w.dbPool, claimed.ID, claimed.Attempt, runErr.Error()); err != nil {
			slog.Error("worker: failed to record permanent failure", "task_id", claimed.ID, "error", err)
			return
		}
		metrics.TasksDeadLetteredTotal.WithLabelValues(claimed.TaskType).Inc()
		slog.Warn("task dead-lettered (permanent error)", "task_id", claimed.ID, "error", runErr)
		return
	}

	var retryAfter time.Duration
	var rae *task.ErrRetryAfter
	if errors.As(runErr, &rae) {
		retryAfter = rae.After
	}

	dead, err := task.Fail(context.Background(), w.dbPool, claimed.ID, claimed.Attempt, runErr.Error(), retryAfter)
	if err != nil {
		slog.Error("worker: failed to record failure", "task_id", claimed.ID, "error", err)
		return
	}
	if dead {
		metrics.TasksDeadLetteredTotal.WithLabelValues(claimed.TaskType).Inc()
		slog.Warn("task dead-lettered", "task_id", claimed.ID, "attempts", claimed.Attempt, "error", runErr)
	} else {
		metrics.TasksRetriedTotal.WithLabelValues(claimed.TaskType, strconv.Itoa(int(claimed.Attempt))).Inc()
		slog.Info("task failed, will retry", "task_id", claimed.ID, "attempt", claimed.Attempt, "error", runErr)
	}
}

func (w *WorkerServer) reportSuccess(claimed task.Claimed) {
	ok, err := task.Complete(context.Background(), w.dbPool, claimed.ID, claimed.Attempt)
	if err != nil {
		slog.Error("worker: failed to record completion", "task_id", claimed.ID, "error", err)
		return
	}
	if !ok {
		slog.Warn("task completion fenced out -- lease was lost mid-execution", "task_id", claimed.ID)
		return
	}
	metrics.TasksCompletedTotal.WithLabelValues(claimed.TaskType).Inc()
}

// renewInterval is a fraction of the configured lease, not a fixed
// constant -- renewing at 1/3 of the lease leaves two missed ticks of
// margin before expiry regardless of how short or long the lease is
// configured.
func renewInterval(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < minRenewInterval {
		return minRenewInterval
	}
	return interval
}

// renewLease keeps a running task's lease alive so a healthy worker
// executing a long task is never mistaken for a dead one. If the lease was
// already reclaimed (ok == false), the task is running elsewhere or was
// dead-lettered -- cancel this execution rather than let two copies run.
func (w *WorkerServer) renewLease(ctx context.Context, taskID string, cancelTask context.CancelFunc, wasCancelled *atomic.Bool) {
	ticker := time.NewTicker(renewInterval(w.leaseDuration))
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ok, err := task.RenewLease(context.Background(), w.dbPool, taskID, w.workerID(), w.leaseDuration)
			if err != nil {
				slog.Error("worker: renew lease", "task_id", taskID, "error", err)
				continue
			}
			if !ok {
				slog.Warn("worker: lost lease, cancelling execution", "task_id", taskID)
				cancelTask()
				return
			}

			// Cancellation of a running task is checked here, on the same
			// cadence as lease renewal -- there is no cheaper way to
			// interrupt a task already executing in another goroutine.
			// This is the documented ceiling: cancel latency == renewal
			// interval, not instant.
			requested, err := task.IsCancelRequested(context.Background(), w.dbPool, taskID)
			if err != nil {
				slog.Error("worker: check cancel_requested", "task_id", taskID, "error", err)
				continue
			}
			if requested {
				slog.Info("worker: cancellation requested, stopping execution", "task_id", taskID)
				wasCancelled.Store(true)
				cancelTask()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (w *WorkerServer) registerActive(taskID string, cancel context.CancelFunc) {
	w.activeTasksMutex.Lock()
	defer w.activeTasksMutex.Unlock()
	w.activeTasks[taskID] = cancel
}

func (w *WorkerServer) unregisterActive(taskID string) {
	w.activeTasksMutex.Lock()
	defer w.activeTasksMutex.Unlock()
	delete(w.activeTasks, taskID)
}

type timeoutPayload struct {
	TimeoutMS int `json:"timeout_ms"`
}

func taskTimeout(payload json.RawMessage) time.Duration {
	var p timeoutPayload
	if err := json.Unmarshal(payload, &p); err == nil && p.TimeoutMS > 0 {
		return time.Duration(p.TimeoutMS) * time.Millisecond
	}
	return defaultTaskTimeout
}
