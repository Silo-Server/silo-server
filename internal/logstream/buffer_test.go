package logstream

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestBufferWriteDropsAndCountsWhenFull(t *testing.T) {
	dropped := droppedEntries.WithLabelValues(string(StreamAudit), DropBufferFull)
	before := testutil.ToFloat64(dropped)

	buf := NewBuffer[int](StreamAudit, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 5 {
			buf.Write(i)
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Write blocked on a full buffer")
	}

	if got := testutil.ToFloat64(dropped) - before; got != 3 {
		t.Fatalf("dropped = %v, want 3", got)
	}
	if got := []int{<-buf.Chan(), <-buf.Chan()}; !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("buffered = %v, want [0 1]", got)
	}
}

type flushRecorder struct {
	mu      sync.Mutex
	batches [][]int
	ctxErrs []error
	flushed chan struct{}
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{flushed: make(chan struct{}, 16)}
}

func (r *flushRecorder) flush(ctx context.Context, batch []int) {
	r.mu.Lock()
	r.batches = append(r.batches, slices.Clone(batch))
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
	r.mu.Unlock()
	r.flushed <- struct{}{}
}

func (r *flushRecorder) snapshot() ([][]int, []error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.batches), slices.Clone(r.ctxErrs)
}

func TestDrainFlushesFullBatchesThenOnInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan int, 10)
	rec := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Drain(ctx, ch, 3, 20*time.Millisecond, rec.flush)
	}()

	for i := range 4 {
		ch <- i
	}
	for range 2 {
		select {
		case <-rec.flushed:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for flush")
		}
	}
	cancel()
	<-done

	batches, _ := rec.snapshot()
	want := [][]int{{0, 1, 2}, {3}}
	if !slices.EqualFunc(batches, want, slices.Equal) {
		t.Fatalf("batches = %v, want %v", batches, want)
	}
}

type ctxKey struct{}

func TestDrainFlushesBufferedEntriesWhenStopped(t *testing.T) {
	ch := make(chan int, 10)
	for i := range 5 {
		ch <- i
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "kept"))
	cancel()

	var values []any
	rec := newFlushRecorder()
	Drain(ctx, ch, 2, time.Hour, func(ctx context.Context, batch []int) {
		values = append(values, ctx.Value(ctxKey{}))
		rec.flush(ctx, batch)
	})

	batches, errs := rec.snapshot()
	want := [][]int{{0, 1}, {2, 3}, {4}}
	if !slices.EqualFunc(batches, want, slices.Equal) {
		t.Fatalf("batches = %v, want %v", batches, want)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("flush %d got a canceled context: %v", i, err)
		}
		if values[i] != "kept" {
			t.Fatalf("flush %d lost context values", i)
		}
	}
}

func TestDrainReturnsAfterChannelCloses(t *testing.T) {
	ch := make(chan int, 4)
	ch <- 1
	ch <- 2
	close(ch)
	rec := newFlushRecorder()
	Drain(context.Background(), ch, 10, time.Hour, rec.flush)

	batches, _ := rec.snapshot()
	if want := [][]int{{1, 2}}; !slices.EqualFunc(batches, want, slices.Equal) {
		t.Fatalf("batches = %v, want %v", batches, want)
	}
}
