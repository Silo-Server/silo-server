package policy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/partman"
)

type fakeDecisionLogDB struct {
	mu       sync.Mutex
	argCount []int
	calls    chan int
}

func (f *fakeDecisionLogDB) Exec(_ context.Context, _ string, arguments ...any) (pgconn.CommandTag, error) {
	count := len(arguments) / 14
	f.mu.Lock()
	f.argCount = append(f.argCount, len(arguments))
	f.mu.Unlock()
	f.calls <- count
	return pgconn.CommandTag{}, nil
}

func TestDecisionLoggerDropsWhenBufferFull(t *testing.T) {
	logger := newDecisionLogger(nil, "node-a", WithDecisionLogBufferSize(1))

	logger.Log(testDecisionEntry())
	logger.Log(testDecisionEntry())

	if got := logger.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount() = %d, want 1", got)
	}
}

func TestDecisionLoggerDenialsAndErrorsBypassScopeSampling(t *testing.T) {
	logger := newDecisionLogger(nil, "node-a", WithDecisionLogBufferSize(10))
	logger.SetScopeSampleRate(100)
	denied := false

	logger.Log(Entry{DecisionName: DecisionScope, Allowed: &denied, InputDigest: "digest"})
	logger.Log(Entry{DecisionName: DecisionScope, Error: "boom", InputDigest: "digest"})

	if got := len(logger.ch); got != 2 {
		t.Fatalf("buffered entries = %d, want 2", got)
	}
}

func testDecisionEntry() Entry {
	return Entry{
		DecisionName:     DecisionName("silo.permission.decision"),
		PolicyGeneration: 1,
		EvalTimeNS:       100,
		InputDigest:      "digest",
	}
}

type fakeDecisionLogPartitions struct {
	dropErr        error
	defaultDeleted int64
}

func (f fakeDecisionLogPartitions) EnsureFuturePartitions(context.Context) error { return nil }

func (f fakeDecisionLogPartitions) DropExpiredPartitions(context.Context, time.Time) ([]string, error) {
	return nil, f.dropErr
}

func (f fakeDecisionLogPartitions) DeleteExpiredRowsFromDefault(context.Context, time.Time) (int64, error) {
	return f.defaultDeleted, nil
}

func TestCleanupDecisionLogsOnceAfterAFailedDrop(t *testing.T) {
	broken := errors.New("drop failed")
	for _, tc := range []struct {
		name    string
		dropErr error
		want    error
	}{
		// A busy table is retried next run; the row deletes still prune this one.
		{"busy table", fmt.Errorf("drop partition p: %w", partman.ErrPartitionBusy), nil},
		{"other error", broken, broken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The row-delete fallback must not drop the default partition's count.
			deleted, err := CleanupDecisionLogsOnce(t.Context(), nil, nil, fakeDecisionLogPartitions{dropErr: tc.dropErr, defaultDeleted: 3})
			if !errors.Is(err, tc.want) || deleted != 3 {
				t.Fatalf("got %d rows, %v; want 3 rows, %v", deleted, err, tc.want)
			}
		})
	}
}
