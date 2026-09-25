package logstream

import (
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
	Help: "Operational (app) and activity (audit) log entries that never reached Postgres, by reason. App records also went to stderr and OTLP; audit entries have no other copy.",
}, []string{"stream", "reason"})

// countDropped records n entries of stream lost for reason.
func countDropped(stream Stream, reason string, n int) {
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
