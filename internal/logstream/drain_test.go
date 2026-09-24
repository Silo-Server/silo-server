package logstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// insertRecorder records Insert calls; fail, when set, decides each
// attempt's result.
type insertRecorder struct {
	mu       sync.Mutex
	batches  [][]int
	ctxErrs  []error
	values   []any
	deadline []bool
	failures []InsertFailure
	fail     func(attempt int) error
	attempts int
	inserted chan struct{}
}

func newInsertRecorder() *insertRecorder {
	return &insertRecorder{inserted: make(chan struct{}, 64)}
}

func (r *insertRecorder) insert(ctx context.Context, batch []int) error {
	r.mu.Lock()
	r.attempts++
	var err error
	if r.fail != nil {
		err = r.fail(r.attempts)
	}
	if err == nil {
		r.batches = append(r.batches, slices.Clone(batch))
		r.ctxErrs = append(r.ctxErrs, ctx.Err())
		r.values = append(r.values, ctx.Value(ctxKey{}))
		_, ok := ctx.Deadline()
		r.deadline = append(r.deadline, ok)
	}
	r.mu.Unlock()
	r.inserted <- struct{}{}
	return err
}

func (r *insertRecorder) failed(_ context.Context, f InsertFailure) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, f)
}

func (r *insertRecorder) drain(size int, interval time.Duration) Drain[int] {
	return Drain[int]{Stream: StreamAudit, Size: size, Interval: interval, Insert: r.insert, Failed: r.failed}
}

func (r *insertRecorder) snapshot() (batches [][]int, attempts int, failures []InsertFailure) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.batches), r.attempts, slices.Clone(r.failures)
}

func (r *insertRecorder) wait(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-r.inserted:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for an insert attempt")
		}
	}
}

// shortRetries makes retry backoff fast for the test.
func shortRetries(t *testing.T, backoff time.Duration) {
	t.Helper()
	prevMin, prevMax := retryMinBackoff, retryMaxBackoff
	retryMinBackoff, retryMaxBackoff = backoff, backoff
	t.Cleanup(func() { retryMinBackoff, retryMaxBackoff = prevMin, prevMax })
}

func insertFailedDrops(stream Stream) float64 {
	return testutil.ToFloat64(droppedEntries.WithLabelValues(string(stream), DropInsertFailed))
}

// errUnreachable is what an insert returns while Postgres refuses connections.
var errUnreachable = fmt.Errorf("failed to connect: %w", &netError{})

type netError struct{}

func (*netError) Error() string   { return "dial tcp 127.0.0.1:5432: connect: connection refused" }
func (*netError) Timeout() bool   { return false }
func (*netError) Temporary() bool { return false }

func TestDrainFlushesFullBatchesThenOnInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan int, 10)
	rec := newInsertRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.drain(3, 20*time.Millisecond).Run(ctx, ch)
	}()

	for i := range 4 {
		ch <- i
	}
	rec.wait(t, 2)
	cancel()
	<-done

	batches, _, _ := rec.snapshot()
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

	rec := newInsertRecorder()
	rec.drain(2, time.Hour).Run(ctx, ch)

	batches, _, _ := rec.snapshot()
	want := [][]int{{0, 1}, {2, 3}, {4}}
	if !slices.EqualFunc(batches, want, slices.Equal) {
		t.Fatalf("batches = %v, want %v", batches, want)
	}
	for i := range batches {
		if rec.ctxErrs[i] != nil {
			t.Fatalf("insert %d got a canceled context: %v", i, rec.ctxErrs[i])
		}
		if rec.values[i] != "kept" {
			t.Fatalf("insert %d lost context values", i)
		}
		if !rec.deadline[i] {
			t.Fatalf("insert %d ran without a deadline", i)
		}
	}
}

func TestDrainReturnsAfterChannelCloses(t *testing.T) {
	ch := make(chan int, 4)
	ch <- 1
	ch <- 2
	close(ch)
	rec := newInsertRecorder()
	rec.drain(10, time.Hour).Run(context.Background(), ch)

	batches, _, _ := rec.snapshot()
	if want := [][]int{{1, 2}}; !slices.EqualFunc(batches, want, slices.Equal) {
		t.Fatalf("batches = %v, want %v", batches, want)
	}
}

// TestDrainRetriesABatchWhilePostgresIsUnreachable fails the first two
// attempts the way a refused connection does and checks that the batch is
// persisted once Postgres is back, with nothing counted as dropped.
func TestDrainRetriesABatchWhilePostgresIsUnreachable(t *testing.T) {
	shortRetries(t, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := insertFailedDrops(StreamAudit)

	rec := newInsertRecorder()
	rec.fail = func(attempt int) error {
		if attempt <= 2 {
			return errUnreachable
		}
		return nil
	}
	ch := make(chan int, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.drain(2, time.Hour).Run(ctx, ch)
	}()
	ch <- 1
	ch <- 2
	rec.wait(t, 3)
	cancel()
	<-done

	batches, attempts, failures := rec.snapshot()
	if want := [][]int{{1, 2}}; attempts != 3 || !slices.EqualFunc(batches, want, slices.Equal) {
		t.Fatalf("attempts = %d, batches = %v; want 3 attempts persisting %v", attempts, batches, want)
	}
	if len(failures) != 2 || failures[0].RetryIn == 0 || failures[1].RetryIn == 0 {
		t.Fatalf("failures = %+v, want two retried attempts", failures)
	}
	if got := insertFailedDrops(StreamAudit) - before; got != 0 {
		t.Fatalf("insert_failed drops = %v, want 0", got)
	}
}

// TestDrainDropsABatchTheServerRejects checks that a batch Postgres rejects
// as a whole is dropped after one attempt instead of holding up the stream.
func TestDrainDropsABatchTheServerRejects(t *testing.T) {
	shortRetries(t, time.Millisecond)
	before := insertFailedDrops(StreamAudit)

	rec := newInsertRecorder()
	rec.fail = func(attempt int) error {
		if attempt == 1 {
			return &pgconn.PgError{Code: "23514", Message: "no partition of relation found for row"}
		}
		return nil
	}
	ch := make(chan int, 4)
	ch <- 1
	ch <- 2
	ch <- 3
	close(ch)
	rec.drain(2, time.Hour).Run(context.Background(), ch)

	batches, attempts, failures := rec.snapshot()
	if want := [][]int{{3}}; attempts != 2 || !slices.EqualFunc(batches, want, slices.Equal) {
		t.Fatalf("attempts = %d, batches = %v; want the rejected batch dropped and %v persisted", attempts, batches, want)
	}
	if len(failures) != 1 || failures[0].RetryIn != 0 {
		t.Fatalf("failures = %+v, want one dropped batch", failures)
	}
	if got := insertFailedDrops(StreamAudit) - before; got != 2 {
		t.Fatalf("insert_failed drops = %v, want 2", got)
	}
}

// TestDrainKeepsTheRowsBesideARejectedValue fails every statement that holds
// one bad row, the way Postgres rejects a value it cannot store, and checks
// that the other rows are inserted and only the bad one is counted.
func TestDrainKeepsTheRowsBesideARejectedValue(t *testing.T) {
	shortRetries(t, time.Millisecond)
	before := insertFailedDrops(StreamAudit)

	var mu sync.Mutex
	var persisted [][]int
	var failures []InsertFailure
	d := Drain[int]{Stream: StreamAudit, Size: 4, Interval: time.Hour,
		Insert: func(_ context.Context, batch []int) error {
			if slices.Contains(batch, 2) {
				return fmt.Errorf("batch insert: %w", &pgconn.PgError{Code: "22P02", Message: "invalid input syntax for type inet"})
			}
			mu.Lock()
			defer mu.Unlock()
			persisted = append(persisted, slices.Clone(batch))
			return nil
		},
		Failed: func(_ context.Context, f InsertFailure) {
			mu.Lock()
			defer mu.Unlock()
			failures = append(failures, f)
		},
	}
	ch := make(chan int, 6)
	for i := range 6 {
		ch <- i
	}
	close(ch)
	d.Run(context.Background(), ch)

	if want := [][]int{{0}, {1}, {3}, {4, 5}}; !slices.EqualFunc(persisted, want, slices.Equal) {
		t.Fatalf("persisted %v, want %v", persisted, want)
	}
	if len(failures) != 1 || failures[0].Entries != 1 || failures[0].RetryIn != 0 {
		t.Fatalf("failures = %+v, want the one rejected row dropped", failures)
	}
	if got := insertFailedDrops(StreamAudit) - before; got != 1 {
		t.Fatalf("insert_failed drops = %v, want 1", got)
	}
}

// TestDrainGivesUpRetryingWhenStopped cancels the drain while a batch waits
// between retries: the batch gets one last attempt and Run returns without
// waiting out the backoff.
func TestDrainGivesUpRetryingWhenStopped(t *testing.T) {
	shortRetries(t, time.Hour)
	before := insertFailedDrops(StreamAudit)
	ctx, cancel := context.WithCancel(context.Background())

	rec := newInsertRecorder()
	rec.fail = func(int) error { return errUnreachable }
	ch := make(chan int, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.drain(1, time.Hour).Run(ctx, ch)
	}()
	ch <- 1
	rec.wait(t, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run kept waiting out the retry backoff after it was stopped")
	}

	_, attempts, failures := rec.snapshot()
	if attempts != 2 || len(failures) != 2 || failures[1].RetryIn != 0 {
		t.Fatalf("attempts = %d, failures = %+v; want a final attempt that drops the batch", attempts, failures)
	}
	if got := insertFailedDrops(StreamAudit) - before; got != 1 {
		t.Fatalf("insert_failed drops = %v, want 1", got)
	}
}

// TestDrainBoundsAHungInsert checks that an attempt that never returns on its
// own is cut off by the insert deadline and retried.
func TestDrainBoundsAHungInsert(t *testing.T) {
	shortRetries(t, time.Millisecond)
	prev := insertTimeout
	insertTimeout = 20 * time.Millisecond
	t.Cleanup(func() { insertTimeout = prev })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempts int
	var mu sync.Mutex
	persisted := make(chan []int, 1)
	d := Drain[int]{Stream: StreamAudit, Size: 1, Interval: time.Hour, Insert: func(ctx context.Context, batch []int) error {
		mu.Lock()
		attempts++
		first := attempts == 1
		mu.Unlock()
		if first {
			<-ctx.Done()
			return fmt.Errorf("batch insert: %w", ctx.Err())
		}
		persisted <- slices.Clone(batch)
		return nil
	}}
	ch := make(chan int, 1)
	go d.Run(ctx, ch)
	ch <- 7

	select {
	case got := <-persisted:
		if !slices.Equal(got, []int{7}) {
			t.Fatalf("persisted %v, want [7]", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a hung insert held the batch")
	}
}

func TestRetryable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"connection refused", errUnreachable, true},
		{"attempt deadline", fmt.Errorf("batch insert: %w", context.DeadlineExceeded), true},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, true},
		{"cannot connect now", &pgconn.PgError{Code: "57P03"}, true},
		{"too many connections", &pgconn.PgError{Code: "53300"}, true},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"read-only after switchover", &pgconn.PgError{Code: "25006"}, true},
		{"other invalid transaction state", &pgconn.PgError{Code: "25P02"}, false},
		{"invalid inet", &pgconn.PgError{Code: "22P02"}, false},
		{"missing partition", &pgconn.PgError{Code: "23514"}, false},
		{"undefined column", &pgconn.PgError{Code: "42703"}, false},
		{"closed pool", errors.New("closed pool"), false},
		{"scan failure", fmt.Errorf("scan inserted row: %w", errors.New("cannot scan")), false},
	} {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("retryable(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRetryableClassifiesRealPoolErrors checks the classification against the
// errors pgxpool actually returns, not hand-built ones.
func TestRetryableClassifiesRealPoolErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	pool, err := pgxpool.New(context.Background(), "postgres://silo@"+addr+"/silo?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}

	_, err = pool.Exec(context.Background(), "SELECT 1")
	if err == nil || !retryable(err) {
		t.Fatalf("refused connection: retryable(%v) = false, want true", err)
	}
	pool.Close()
	_, err = pool.Exec(context.Background(), "SELECT 1")
	if err == nil || retryable(err) {
		t.Fatalf("closed pool: retryable(%v) = true, want false", err)
	}
}
