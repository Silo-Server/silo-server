package blobgc

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Queue schedules and cancels deletions for a writer that retires objects
// outside its own transaction, such as an image a newer one replaced. A
// namespace must own every prefix it names, or the table's check rejects it.
type Queue struct {
	pool *pgxpool.Pool
}

// NewQueue returns nil without a database.
func NewQueue(pool *pgxpool.Pool) *Queue {
	if pool == nil {
		return nil
	}
	return &Queue{pool: pool}
}

// Schedule queues prefixes for deletion once delay has passed. A prefix
// already queued keeps the later of its two times, so a second retirement
// never shortens the first one's wait.
func (q *Queue) Schedule(ctx context.Context, prefixes []string, delay time.Duration) error {
	if len(prefixes) == 0 {
		return nil
	}
	_, err := q.pool.Exec(ctx, `
		INSERT INTO public.blob_gc_queue (prefix, not_before)
		SELECT unnest($1::text[]), now() + make_interval(secs => $2)
		ON CONFLICT (prefix) DO UPDATE
		SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before)`,
		prefixes, delay.Seconds())
	if err != nil {
		return fmt.Errorf("queue %d prefixes for deletion: %w", len(prefixes), err)
	}
	return nil
}

// Cancel takes prefixes off the queue before a writer stores under them
// again. A collector deleting one holds its row locked, so Cancel waits for
// that deletion to finish rather than letting it remove the new object.
func (q *Queue) Cancel(ctx context.Context, prefixes []string) error {
	if len(prefixes) == 0 {
		return nil
	}
	if _, err := q.pool.Exec(ctx, `DELETE FROM public.blob_gc_queue WHERE prefix = ANY($1::text[])`, prefixes); err != nil {
		return fmt.Errorf("cancel queued deletion of %d prefixes: %w", len(prefixes), err)
	}
	return nil
}
