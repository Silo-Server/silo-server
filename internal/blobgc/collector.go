package blobgc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// Collector deletes the queued prefixes whose time has come.
//
// It takes one due row at a time with FOR UPDATE SKIP LOCKED, so collectors
// on every server share the queue without taking the same row, and holds the
// row's lock while it deletes. A prefix is deleted only when its namespace
// reports it dead. Directory groups are listed after deletion; individual
// images are checked by exact key. A row is removed only after storage
// confirms absence: S3 batch deletes can fail per key without failing the
// call. A failure keeps the row and retries it later, backing off.
type Collector struct {
	pool       *pgxpool.Pool
	store      Store
	namespaces []Namespace
}

// NewCollector returns nil when there is no database or storage to collect
// from.
func NewCollector(pool *pgxpool.Pool, store Store, namespaces ...Namespace) *Collector {
	if pool == nil || store == nil {
		return nil
	}
	return &Collector{pool: pool, store: store, namespaces: namespaces}
}

// CollectStats counts what one collection did.
type CollectStats struct {
	// Deleted prefixes were emptied and dequeued; Objects counts the objects
	// removed from them.
	Deleted int `json:"deleted"`
	Objects int `json:"objects"`
	// Kept prefixes turned out to be referenced again and were dequeued
	// without deleting anything.
	Kept int `json:"kept"`
	// Retried prefixes failed and wait for a later run.
	Retried int `json:"retried"`
}

// collectRetryBase and collectRetryMax bound the backoff of a prefix whose
// deletion failed: ten minutes, doubling per attempt, at most a day.
const (
	collectRetryBase = 10 * time.Minute
	collectRetryMax  = 24 * time.Hour
)

func collectRetryDelay(attempts int) time.Duration {
	delay := collectRetryBase
	for range min(attempts, 8) {
		delay *= 2
	}
	return min(delay, collectRetryMax)
}

// Collect deletes due prefixes until none is due, limit have been handled,
// or ctx ends. An error from the database or a namespace's liveness check stops
// it; a storage failure only reschedules its prefix.
func (c *Collector) Collect(ctx context.Context, limit int) (CollectStats, error) {
	var stats CollectStats
	for range limit {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		done, err := c.collectOne(ctx, &stats)
		if err != nil {
			return stats, err
		}
		if done {
			break
		}
	}
	return stats, nil
}

// collectOne handles the next due prefix, and reports true when none is due.
func (c *Collector) collectOne(ctx context.Context, stats *CollectStats) (bool, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var prefix string
	var attempts int
	err = tx.QueryRow(ctx, `
		SELECT prefix, attempts FROM public.blob_gc_queue
		WHERE not_before <= now()
		ORDER BY not_before, prefix
		LIMIT 1
		FOR UPDATE SKIP LOCKED`).Scan(&prefix, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim a queued prefix: %w", err)
	}

	ns, ok := namespaceFor(c.namespaces, prefix)
	if !ok {
		// A build that knows this namespace queued it; leave it to one.
		return false, c.retry(ctx, tx, prefix, attempts, errors.New("no namespace on this server owns the prefix"), stats)
	}
	live, err := ns.Live(ctx, tx, []string{prefix})
	if err != nil {
		return false, fmt.Errorf("check whether %s is referenced: %w", prefix, err)
	}
	if live[prefix] {
		stats.Kept++
		return false, c.dequeue(ctx, tx, prefix)
	}
	removed, err := c.deleteGroup(ctx, prefix)
	if err != nil {
		return false, c.retry(ctx, tx, prefix, attempts, err, stats)
	}
	stats.Deleted++
	stats.Objects += removed
	return false, c.dequeue(ctx, tx, prefix)
}

// deleteGroup verifies exact object deletions separately from directory
// deletions: S3 prefix operations append a slash and cannot address an image.
func (c *Collector) deleteGroup(ctx context.Context, group string) (int, error) {
	if strings.HasSuffix(group, "/") {
		removed, err := c.store.DeletePrefix(ctx, group)
		if err == nil {
			err = c.checkEmpty(ctx, group)
		}
		return removed, err
	}
	removed, err := c.store.Delete(ctx, []string{group})
	if err != nil {
		return removed, err
	}
	if _, err := c.store.Stat(ctx, group); errors.Is(err, blobstore.ErrNotFound) {
		return removed, nil
	} else if err != nil {
		return removed, fmt.Errorf("stat after delete: %w", err)
	}
	return removed, fmt.Errorf("object %s remains after delete", group)
}

// checkEmpty confirms storage holds nothing under prefix any more.
func (c *Collector) checkEmpty(ctx context.Context, prefix string) error {
	left, _, err := c.store.List(ctx, prefix, "", 1)
	if err != nil {
		return fmt.Errorf("list after delete: %w", err)
	}
	if len(left) > 0 {
		return fmt.Errorf("objects remain under %s after delete", prefix)
	}
	return nil
}

func (c *Collector) dequeue(ctx context.Context, tx pgx.Tx, prefix string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM public.blob_gc_queue WHERE prefix = $1`, prefix); err != nil {
		return fmt.Errorf("dequeue %s: %w", prefix, err)
	}
	return tx.Commit(ctx)
}

func (c *Collector) retry(ctx context.Context, tx pgx.Tx, prefix string, attempts int, cause error, stats *CollectStats) error {
	stats.Retried++
	message := strings.ToValidUTF8(strings.ReplaceAll(cause.Error(), "\x00", ""), "")
	if len(message) > 500 {
		message = strings.ToValidUTF8(message[:500], "")
	}
	_, err := tx.Exec(ctx, `
		UPDATE public.blob_gc_queue
		SET attempts = attempts + 1, last_error = $2, not_before = now() + make_interval(secs => $3)
		WHERE prefix = $1`, prefix, message, collectRetryDelay(attempts).Seconds())
	if err != nil {
		return fmt.Errorf("reschedule %s: %w", prefix, err)
	}
	return tx.Commit(ctx)
}
