package audiobooks

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// sweepBatchGap is the pause between two batches of one sweep. The provider
// plugin's own limiter sets the request rate; the gap only keeps a long
// backlog drain from monopolizing the database and the plugin, so other work
// such as a manual identify gets a turn between batches.
const sweepBatchGap = 2 * time.Second

const sweepCanceled = "canceled"

// sweepBatch is what one claimed batch recorded.
type sweepBatch struct {
	claimed int
	summary sweepSummary
}

// Run sweeps due books batch after batch and returns how many it matched.
// Only one node sweeps at a time: Run returns 0 at once while another holds
// the sweep lock, so adding nodes does not add provider load. Claims still
// fence each item, so a lease that outlives a lost lock cannot be processed
// twice. See sweep for when a run stops.
func (e *Enricher) Run(ctx context.Context) (int, error) {
	if e == nil || e.pool == nil || e.chainRepo == nil {
		return 0, nil
	}
	lock, acquired, err := e.takeSweepLock(ctx)
	if err != nil {
		return 0, err
	}
	if !acquired {
		slog.InfoContext(ctx, "audiobook enrichment: another node is sweeping; skipping this run", "component", "audiobooks")
		return 0, nil
	}
	defer lock.release()
	return e.sweep(ctx, lock.check, e.runOneBatch, sleepCtx)
}

// sweep runs batches until one of these holds after a batch:
//   - the run is canceled (shutdown, or a runtime limit set on the task);
//   - the batch was short or empty, so no more books were due when it was
//     claimed;
//   - no book in the batch reached an outcome: every one was deferred, as
//     when the provider gate has paused the whole chain, or failed. The next
//     batch would most likely go the same way, so the next scheduled run
//     picks up once the deferred books come due. A batch that still matched
//     books while one provider was paused goes on: the gate keeps calls away
//     from the paused provider.
//
// Before each further batch it waits sweepBatchGap and checks that the sweep
// lock's session is still open: once it is gone another node may take the
// lock, and two sweepers would double the provider load.
func (e *Enricher) sweep(
	ctx context.Context,
	lockHeld func(context.Context) error,
	runBatch func(context.Context) (sweepBatch, error),
	sleep func(context.Context, time.Duration) error,
) (int, error) {
	enriched := 0
	for batches := 1; ; batches++ {
		batch, err := runBatch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return enriched, nil //nolint:nilerr // a canceled run is a stop, not a failure
			}
			return enriched, err
		}
		enriched += batch.summary.enriched
		reason := e.sweepStopReason(ctx, batch)
		if reason == "" {
			reason = waitForNextBatch(ctx, lockHeld, sleep)
		}
		if reason == "" {
			continue
		}
		if batch.claimed > 0 || batches > 1 {
			slog.InfoContext(ctx, "audiobook enrichment: sweep stopped", "component", "audiobooks",
				"reason", reason, "batches", batches, "enriched", enriched)
		}
		return enriched, nil
	}
}

// waitForNextBatch waits out the gap and checks the lock. It returns why the
// sweep should stop instead, or "" to claim the next batch. A lost lock is a
// stop, not a failure: the claims fence every item, so what the sweep already
// recorded stands, and another node may sweep from here.
func waitForNextBatch(
	ctx context.Context,
	lockHeld func(context.Context) error,
	sleep func(context.Context, time.Duration) error,
) string {
	if sleep(ctx, sweepBatchGap) != nil || ctx.Err() != nil {
		return sweepCanceled
	}
	if err := lockHeld(ctx); err != nil {
		if ctx.Err() != nil {
			return sweepCanceled
		}
		slog.WarnContext(ctx, "audiobook enrichment: lost the sweep lock; stopping", "component", "audiobooks", "error", err)
		return "lost the sweep lock"
	}
	return ""
}

// sweepStopReason says why the sweep should stop after batch, or "" to go on.
func (e *Enricher) sweepStopReason(ctx context.Context, batch sweepBatch) string {
	s := batch.summary
	switch {
	case ctx.Err() != nil:
		return sweepCanceled
	case batch.claimed == 0 || batch.claimed < e.batchSize:
		return "no more books due"
	case s.enriched+s.noMatch+s.skipped == 0:
		return "no book in the batch reached an outcome"
	default:
		return ""
	}
}

// runOneBatch claims one batch, enriches it and summarizes what it recorded.
func (e *Enricher) runOneBatch(ctx context.Context) (sweepBatch, error) {
	// The sweep summary compares against database time, not this node's clock.
	var started time.Time
	if err := e.pool.QueryRow(ctx, `SELECT now()`).Scan(&started); err != nil {
		return sweepBatch{}, fmt.Errorf("audiobook enrichment: read database time: %w", err)
	}
	items, err := e.claimBatch(ctx)
	if err != nil {
		return sweepBatch{}, fmt.Errorf("audiobook enrichment: claim batch: %w", err)
	}
	if len(items) == 0 {
		return sweepBatch{}, nil
	}

	slog.InfoContext(ctx, "audiobook enrichment: batch started", "component", "audiobooks",
		"count", len(items),
		"workers", e.workers,
	)

	e.runBatch(ctx, items, e.enrichItem)

	// The summary decides whether the sweep goes on, so a failed read ends
	// the run with its error instead of reading as a batch without outcomes.
	// What the batch recorded stands either way.
	summary, err := e.sweepSummary(context.WithoutCancel(ctx), items, started)
	if err != nil {
		return sweepBatch{claimed: len(items)}, fmt.Errorf("audiobook enrichment: summarize batch: %w", err)
	}
	slog.InfoContext(ctx, "audiobook enrichment: batch complete", "component", "audiobooks",
		"claimed", len(items),
		"enriched", summary.enriched,
		"no_match", summary.noMatch,
		"skipped", summary.skipped,
		"failed_retrying", summary.retrying,
		"failed_terminal", summary.failed,
		"deferred", len(items)-summary.recorded(),
	)
	return sweepBatch{claimed: len(items), summary: summary}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// sweepLock is the session that holds the cluster-wide sweep lock.
type sweepLock struct {
	conn *pgx.Conn
}

// check reports an error once the lock's session is gone, and with it the
// lock.
func (l *sweepLock) check(ctx context.Context) error {
	return l.conn.Ping(ctx)
}

// release closes the session, which releases the lock.
func (l *sweepLock) release() {
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = l.conn.Close(closeCtx)
}

// takeSweepLock takes the cluster-wide sweep lock.
// The lock lives on a session of its own, opened with the pool's
// configuration but outside it, as the backup and migration locks do: the
// sweep's own queries take pool connections, and with the lock's session in
// the pool a pool of one connection would wait on itself. Closing the session
// releases the lock, including when this process dies mid-sweep.
func (e *Enricher) takeSweepLock(ctx context.Context) (lock *sweepLock, acquired bool, err error) {
	conn, err := pgx.ConnectConfig(ctx, e.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, false, fmt.Errorf("audiobook enrichment: open the sweep lock session: %w", err)
	}
	lock = &sweepLock{conn: conn}
	// A failed query may still have been granted the lock; closing the
	// session releases it either way.
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, enrichSweepLock).Scan(&acquired); err != nil {
		lock.release()
		return nil, false, fmt.Errorf("audiobook enrichment: take the sweep lock: %w", err)
	}
	if !acquired {
		lock.release()
		return nil, false, nil
	}
	return lock, true, nil
}

// sweepLockHeld reports whether a node holds the sweep lock, without opening
// a session. A bigint advisory key shows in pg_locks as its high and low 32
// bits in classid and objid, with objsubid 1. Advisory locks belong to one
// database but pg_locks lists the whole server, so the probe is limited to
// this database: another Silo database on the same server sweeps on its own.
func (e *Enricher) sweepLockHeld(ctx context.Context) (bool, error) {
	var held bool
	err := e.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND granted AND objsubid = 1
			  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
			  AND classid::bigint = $1 AND objid::bigint = $2
		)
	`, enrichSweepLock>>32, enrichSweepLock&0xFFFFFFFF).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("checking the audiobook enrichment sweep lock: %w", err)
	}
	return held, nil
}

// sweepSummary counts what a batch recorded for its claimed items. It reads
// the state table rather than worker return values, which cannot tell a match
// from a clean no-match or a skip.
type sweepSummary struct {
	enriched, noMatch, skipped, retrying, failed int
}

func (s sweepSummary) recorded() int {
	return s.enriched + s.noMatch + s.skipped + s.retrying + s.failed
}

func (e *Enricher) sweepSummary(ctx context.Context, items []enrichmentItemRow, since time.Time) (sweepSummary, error) {
	var summary sweepSummary
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ContentID
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := e.pool.Query(ctx, `
		SELECT COALESCE(outcome, ''), count(*)
		FROM audiobook_enrichment_state
		WHERE content_id = ANY($1)
		  AND last_attempt_at >= $2
		GROUP BY 1
	`, ids, since)
	if err != nil {
		return summary, err
	}
	defer rows.Close()
	for rows.Next() {
		var outcome string
		var n int
		if err := rows.Scan(&outcome, &n); err != nil {
			return summary, err
		}
		switch EnrichmentOutcome(outcome) {
		case EnrichmentOutcomeSuccess:
			summary.enriched = n
		case EnrichmentOutcomeNoMatch:
			summary.noMatch = n
		case EnrichmentOutcomeSkipped:
			summary.skipped = n
		case EnrichmentOutcomeFailed:
			summary.failed = n
		default:
			summary.retrying = n
		}
	}
	return summary, rows.Err()
}
