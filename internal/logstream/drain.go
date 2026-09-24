package logstream

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Batch insert tuning. Variables so tests can shorten them.
var (
	// insertTimeout bounds one insert attempt, so a hung connection cannot
	// stall the stream.
	insertTimeout = 10 * time.Second
	// retryMinBackoff and retryMaxBackoff space the attempts at a batch while
	// Postgres is unavailable.
	retryMinBackoff = time.Second
	retryMaxBackoff = 10 * time.Second
)

// InsertFailure describes one failed attempt to persist a batch.
type InsertFailure struct {
	Err     error
	Entries int
	Attempt int
	// RetryIn is the wait before the next attempt. Zero means the batch was
	// dropped and counted.
	RetryIn time.Duration
}

// Drain moves one stream's buffered entries into Postgres in batches.
type Drain[T any] struct {
	Stream Stream
	// Size is the largest batch; Interval flushes a partial one.
	Size     int
	Interval time.Duration
	// Insert persists one batch in a single statement, so a failed attempt
	// leaves nothing behind: the batch may be retried or split into single
	// rows. Each attempt runs under insertTimeout, with the Run context's
	// values but not its cancellation.
	Insert func(ctx context.Context, batch []T) error
	// Failed, when set, is told about every failed attempt.
	Failed func(ctx context.Context, failure InsertFailure)
}

// Run hands entries from ch to Insert until ch is closed, or until ctx ends
// and the entries already buffered at that moment are flushed.
//
// While Run is live, a batch that fails because Postgres is unreachable or
// unavailable is retried with backoff, and new entries wait in the buffer,
// which drops and counts them once it is full. When the server rejects a value
// in a batch, the rows are inserted one at a time, so only the rows it rejects
// are dropped. Any other batch the server rejects is dropped at once, so it
// cannot hold up the stream. Once Run is stopping, each batch gets one
// attempt, so shutdown stays bounded.
func (d Drain[T]) Run(ctx context.Context, ch <-chan T) {
	flushCtx := context.WithoutCancel(ctx)
	ticker := time.NewTicker(d.Interval)
	defer ticker.Stop()

	batch := make([]T, 0, d.Size)
	stopping := false
	flushPending := func() {
		if len(batch) > 0 {
			d.persist(ctx, flushCtx, batch, stopping)
			batch = batch[:0]
		}
	}
	add := func(entry T) {
		batch = append(batch, entry)
		if len(batch) >= d.Size {
			flushPending()
		}
	}

	for {
		select {
		case <-ctx.Done():
			stopping = true
			// Bounded by what is buffered now, so writers that keep logging
			// during shutdown cannot hold the drain open.
			for n := len(ch); n > 0; n-- {
				entry, ok := <-ch
				if !ok {
					break
				}
				add(entry)
			}
			flushPending()
			return
		case entry, ok := <-ch:
			if !ok {
				stopping = true
				flushPending()
				return
			}
			add(entry)
		case <-ticker.C:
			flushPending()
		}
	}
}

// persist inserts batch, retrying transient failures until it succeeds, live
// ends, or the error says a retry cannot help.
func (d Drain[T]) persist(live, ctx context.Context, batch []T, stopping bool) {
	backoff := retryMinBackoff
	for attempt := 1; ; attempt++ {
		err := d.attempt(ctx, batch)
		if err == nil {
			return
		}
		if len(batch) > 1 && valueRejected(err) {
			// The statement failed on one row's value. Insert the rows one at
			// a time so the others are kept; each rejected row is reported
			// and counted on its own.
			for i := range batch {
				d.persist(live, ctx, batch[i:i+1], stopping)
			}
			return
		}
		failure := InsertFailure{Err: err, Entries: len(batch), Attempt: attempt}
		if stopping || live.Err() != nil || !retryable(err) {
			countDropped(d.Stream, DropInsertFailed, len(batch))
			d.report(ctx, failure)
			return
		}
		failure.RetryIn = backoff
		d.report(ctx, failure)

		timer := time.NewTimer(backoff)
		select {
		case <-live.Done():
			// Shutdown started: one last attempt, then give up.
			timer.Stop()
			stopping = true
		case <-timer.C:
		}
		backoff = min(2*backoff, retryMaxBackoff)
	}
}

func (d Drain[T]) attempt(ctx context.Context, batch []T) error {
	ctx, cancel := context.WithTimeout(ctx, insertTimeout)
	defer cancel()
	return d.Insert(ctx, batch)
}

func (d Drain[T]) report(ctx context.Context, failure InsertFailure) {
	if d.Failed != nil {
		d.Failed(ctx, failure)
	}
}

// retryable reports whether a failed insert can succeed unchanged later:
// Postgres was unreachable, the attempt timed out or lost its connection, or
// the server refused work for a passing reason (failover, restart, overload,
// conflict). Anything else, such as a row the server rejects or a closed pool,
// is not retried.
//
// A write refused as read-only (25006) is retried: during a switchover, pooled
// connections still point at the demoted primary until they are replaced.
//
// A connection lost after the insert committed but before its reply arrived
// makes the retry insert the batch twice. Duplicate log rows are preferred to
// lost ones.
func retryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "25006" { // read_only_sql_transaction
			return true
		}
		if len(pgErr.Code) < 2 {
			return false
		}
		switch pgErr.Code[:2] {
		case "08", // connection exception
			"40", // transaction rollback: serialization failure, deadlock
			"53", // insufficient resources: too many connections, out of memory
			"57", // operator intervention: shutdown, cannot connect now, canceled
			"58": // system error
			return true
		}
		return false
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) ||
		pgconn.Timeout(err) ||
		pgconn.SafeToRetry(err) ||
		errors.As(err, &connectErr) ||
		errors.As(err, &netErr) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// valueRejected reports whether the server rejected a value in the statement
// (class 22, data exception), such as a client address that is not an inet.
// That fails the whole multi-row INSERT, but says nothing about the other
// rows.
func valueRejected(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, "22")
}
