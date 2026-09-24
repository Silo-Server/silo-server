package logstream

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Reasons a log entry never reached Postgres.
const (
	DropBufferFull   = "buffer_full"
	DropInsertFailed = "insert_failed"
)

var droppedEntries = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "silo_log_writer_dropped_total",
	Help: "Operational (app) and activity (audit) log entries dropped before reaching Postgres, by reason. The same records still reach stderr and OTLP.",
}, []string{"stream", "reason"})

// CountDropped records n entries of stream lost for reason.
func CountDropped(stream Stream, reason string, n int) {
	droppedEntries.WithLabelValues(string(stream), reason).Add(float64(n))
}

// Buffer hands log entries from the goroutine that produced them to the
// consumer that persists them. Every node drains its own buffer into the
// shared Postgres table, so nothing on the caller's path depends on Redis.
//
// Write never blocks and never logs: a full buffer drops the entry and counts
// it. Logging from here would re-enter the pipeline that is already failing.
type Buffer[T any] struct {
	ch      chan T
	dropped prometheus.Counter
}

// NewBuffer returns a Buffer that holds up to size entries of stream.
func NewBuffer[T any](stream Stream, size int) *Buffer[T] {
	return &Buffer[T]{
		ch:      make(chan T, size),
		dropped: droppedEntries.WithLabelValues(string(stream), DropBufferFull),
	}
}

func (b *Buffer[T]) Write(entry T) {
	select {
	case b.ch <- entry:
	default:
		b.dropped.Inc()
	}
}

// Close ends the stream for Drain. Write must not be called afterwards.
func (b *Buffer[T]) Close() error {
	close(b.ch)
	return nil
}

// Chan is the consumer side of the buffer.
func (b *Buffer[T]) Chan() <-chan T {
	return b.ch
}

// Drain hands entries from ch to flush in batches of up to size, and flushes a
// partial batch every interval. flush owns error handling; the batch slice is
// reused once it returns. Drain returns after ch is closed, or after ctx ends
// and the entries already buffered at that moment are flushed.
//
// flush always gets ctx's values without its cancellation, so a batch taken
// from the buffer is inserted even when shutdown starts mid-flush.
func Drain[T any](ctx context.Context, ch <-chan T, size int, interval time.Duration, flush func(context.Context, []T)) {
	flushCtx := context.WithoutCancel(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	batch := make([]T, 0, size)
	flushPending := func() {
		if len(batch) > 0 {
			flush(flushCtx, batch)
			batch = batch[:0]
		}
	}
	add := func(entry T) {
		batch = append(batch, entry)
		if len(batch) >= size {
			flushPending()
		}
	}

	for {
		select {
		case <-ctx.Done():
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
				flushPending()
				return
			}
			add(entry)
		case <-ticker.C:
			flushPending()
		}
	}
}
