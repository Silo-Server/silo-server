package catalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// withFastDeadlockRetry shrinks retry timing/attempts for tests and restores
// the originals on cleanup. Tests using it must not call t.Parallel().
func withFastDeadlockRetry(t *testing.T, maxAttempts int) {
	t.Helper()
	oldMax, oldBackoff := deadlockMaxAttempts, deadlockBaseBackoff
	deadlockMaxAttempts = maxAttempts
	deadlockBaseBackoff = time.Millisecond
	t.Cleanup(func() {
		deadlockMaxAttempts = oldMax
		deadlockBaseBackoff = oldBackoff
	})
}

func TestRetryOnDeadlockRetriesThenSucceeds(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	calls := 0
	err := retryOnDeadlock(context.Background(), func() error {
		calls++
		if calls < 3 {
			return &pgconn.PgError{Code: "40P01"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetryOnDeadlockReturnsNonRetryableImmediately(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	sentinel := errors.New("boom")
	calls := 0
	err := retryOnDeadlock(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestRetryOnDeadlockGivesUpAfterMaxAttempts(t *testing.T) {
	withFastDeadlockRetry(t, 4)
	calls := 0
	err := retryOnDeadlock(context.Background(), func() error {
		calls++
		return &pgconn.PgError{Code: "40P01"}
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
		t.Fatalf("expected 40P01 pg error, got %v", err)
	}
	if calls != 4 {
		t.Fatalf("expected 4 calls, got %d", calls)
	}
}

func TestRetryOnDeadlockStopsOnCanceledContext(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryOnDeadlock(ctx, func() error {
		calls++
		return &pgconn.PgError{Code: "40P01"}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call before cancel, got %d", calls)
	}
}
