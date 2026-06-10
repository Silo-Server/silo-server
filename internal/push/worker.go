package push

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// presenceChecker reports live-connection presence (internal/presence.Registry).
type presenceChecker interface {
	Connected(ctx context.Context, userID int) bool
}

// queue is the worker's view of delivery persistence. One Claim call returns a
// batch and an outcome recorder; the prod impl wraps a single tx.
type queue interface {
	Claim(ctx context.Context, now time.Time, limit int) ([]claimedDelivery, Outcomes, error)
}

// Outcomes records per-delivery results and commits/rolls back the batch.
type Outcomes interface {
	Sent(id int64)
	Skipped(id int64, reason string)
	Failed(id int64, nextNotBefore time.Time, errMsg string)
	Dead(id int64, userID int, deviceID, errMsg string)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context)
}

// backoffSchedule by attempts already made (0-indexed): 1m, 5m, 30m, then dead.
var backoffSchedule = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// Worker drains due push deliveries in batches.
type Worker struct {
	q           queue
	presence    presenceChecker
	transports  map[string]Transport
	batch       int
	now         func() time.Time
	parkedUntil map[string]time.Time
}

// NewWorker constructs a Worker. now may be nil (defaults to time.Now).
func NewWorker(q queue, presence presenceChecker, transports []Transport, now func() time.Time) *Worker {
	if now == nil {
		now = time.Now
	}
	m := make(map[string]Transport, len(transports))
	for _, t := range transports {
		m[t.Name()] = t
	}
	return &Worker{
		q:           q,
		presence:    presence,
		transports:  m,
		batch:       100,
		now:         now,
		parkedUntil: map[string]time.Time{},
	}
}

// RunOnce processes one batch. Returns the number of deliveries handled.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	now := w.now()
	items, out, err := w.q.Claim(ctx, now, w.batch)
	if err != nil {
		return 0, err
	}
	defer out.Rollback(ctx) // no-op after Commit

	for _, it := range items {
		w.handle(ctx, now, it, out)
	}
	if err := out.Commit(ctx); err != nil {
		return 0, err
	}
	return len(items), nil
}

func (w *Worker) handle(ctx context.Context, now time.Time, it claimedDelivery, out Outcomes) {
	// Presence gate: user actively connected → skip (in-app sufficed).
	if w.presence != nil && w.presence.Connected(ctx, it.UserID) {
		out.Skipped(it.ID, "user present")
		return
	}
	t := w.transports[it.Transport]
	if t == nil || !t.Configured() {
		out.Skipped(it.ID, "transport unconfigured")
		return
	}
	if until, ok := w.parkedUntil[it.Transport]; ok && now.Before(until) {
		out.Failed(it.ID, until, "transport rate-limited")
		return
	}

	res, retryAfter, sendErr := t.Send(ctx, it.Token, it.Payload)
	switch res {
	case ResultSent:
		out.Sent(it.ID)
	case ResultDead:
		out.Dead(it.ID, it.UserID, it.DeviceID, errString(sendErr))
	case ResultSoftFail:
		if retryAfter > 0 {
			w.parkedUntil[it.Transport] = now.Add(retryAfter)
		}
		next, dead := nextBackoff(it.Attempts, now, retryAfter)
		if dead {
			out.Dead(it.ID, it.UserID, it.DeviceID, "max attempts: "+errString(sendErr))
		} else {
			out.Failed(it.ID, next, errString(sendErr))
		}
	}
	if sendErr != nil {
		slog.WarnContext(ctx, "push: send result",
			"delivery_id", it.ID,
			"transport", it.Transport,
			"result", res,
			"error", sendErr,
		)
	}
}

func nextBackoff(attempts int, now time.Time, retryAfter time.Duration) (time.Time, bool) {
	if attempts >= len(backoffSchedule) {
		return time.Time{}, true // exhausted → dead
	}
	d := backoffSchedule[attempts]
	if retryAfter > d {
		d = retryAfter
	}
	return now.Add(d), false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// --- store-backed queue adapter ---

type storeQueue struct{ s *Store }

// NewStoreQueue wraps *Store to satisfy the queue interface.
func NewStoreQueue(s *Store) queue { return &storeQueue{s: s} }

func (q *storeQueue) Claim(ctx context.Context, now time.Time, limit int) ([]claimedDelivery, Outcomes, error) {
	conn, tx, items, err := q.s.ClaimDue(ctx, now, limit)
	if err != nil {
		return nil, nil, err
	}
	return items, &txOutcomes{s: q.s, tx: tx, conn: conn}, nil
}

type txOutcomes struct {
	s    *Store
	tx   pgx.Tx
	conn *pgxpool.Conn
	done bool
	err  error
}

func (o *txOutcomes) Sent(id int64) {
	o.run(func() error { return o.s.MarkSentTx(context.Background(), o.tx, id) })
}
func (o *txOutcomes) Skipped(id int64, r string) {
	o.run(func() error { return o.s.MarkSkippedTx(context.Background(), o.tx, id, r) })
}
func (o *txOutcomes) Failed(id int64, n time.Time, m string) {
	o.run(func() error { return o.s.MarkFailedTx(context.Background(), o.tx, id, n, m) })
}
func (o *txOutcomes) Dead(id int64, userID int, deviceID, m string) {
	o.run(func() error {
		if err := o.s.MarkDeadTx(context.Background(), o.tx, id, m); err != nil {
			return err
		}
		return o.s.PruneTokenTx(context.Background(), o.tx, userID, deviceID)
	})
}
// run executes one outcome op, short-circuiting once any op has errored so the
// batch commits atomically. NOTE: if a DB op fails mid-batch, the whole tx rolls
// back and every delivery in the batch is re-claimed next tick — including any
// that already hit the transport, which re-sends them. Duplicate banners are
// mitigated by the per-notification collapse/thread key the transports set.
// If duplicates prove unacceptable, switch to per-delivery commits.
func (o *txOutcomes) run(fn func() error) {
	if o.err != nil {
		return
	}
	o.err = fn()
}
func (o *txOutcomes) Commit(ctx context.Context) error {
	if o.done {
		return nil
	}
	o.done = true
	defer o.conn.Release()
	if o.err != nil {
		_ = o.tx.Rollback(ctx)
		return o.err
	}
	return o.tx.Commit(ctx)
}
func (o *txOutcomes) Rollback(ctx context.Context) {
	if o.done {
		return
	}
	o.done = true
	defer o.conn.Release()
	_ = o.tx.Rollback(ctx)
}

// --- scheduled task ---

// PushDeliveryTask drains due push deliveries on a short interval.
type PushDeliveryTask struct{ w *Worker }

// NewPushDeliveryTask wraps Worker as a taskmanager.Task.
func NewPushDeliveryTask(w *Worker) *PushDeliveryTask { return &PushDeliveryTask{w: w} }

func (t *PushDeliveryTask) Key() string         { return "push_delivery" }
func (t *PushDeliveryTask) Name() string        { return "Deliver push notifications" }
func (t *PushDeliveryTask) Description() string { return "Sends queued push notifications to registered devices" }
func (t *PushDeliveryTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *PushDeliveryTask) IsHidden() bool { return false }
func (t *PushDeliveryTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 15 * 1000},
	}
}
func (t *PushDeliveryTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.w == nil {
		progress.Report(100, "push delivery unavailable")
		return nil
	}
	// Drain until empty or a cap, so a burst clears within one tick.
	total := 0
	for i := 0; i < 20; i++ {
		n, err := t.w.RunOnce(ctx)
		if err != nil {
			return fmt.Errorf("push delivery: %w", err)
		}
		total += n
		if n == 0 {
			break
		}
	}
	progress.Report(100, fmt.Sprintf("processed %d deliveries", total))
	return nil
}
