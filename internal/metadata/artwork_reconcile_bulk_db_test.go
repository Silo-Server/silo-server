package metadata

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRetryOnDeadlock(t *testing.T) {
	prevBackoff := artworkReconcileDeadlockBaseBackoff
	artworkReconcileDeadlockBaseBackoff = time.Millisecond
	t.Cleanup(func() { artworkReconcileDeadlockBaseBackoff = prevBackoff })

	deadlock := &pgconn.PgError{Code: "40P01"}
	ctx := context.Background()

	t.Run("retries deadlocks until success", func(t *testing.T) {
		calls := 0
		err := retryOnDeadlock(ctx, func() error {
			calls++
			if calls < 3 {
				return fmt.Errorf("exec: %w", deadlock)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("retryOnDeadlock: %v", err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})

	t.Run("gives up after max attempts", func(t *testing.T) {
		calls := 0
		err := retryOnDeadlock(ctx, func() error {
			calls++
			return deadlock
		})
		if !errors.Is(err, deadlock) {
			t.Fatalf("err = %v, want the deadlock error", err)
		}
		if calls != artworkReconcileDeadlockMaxAttempts {
			t.Errorf("calls = %d, want %d", calls, artworkReconcileDeadlockMaxAttempts)
		}
	})

	t.Run("returns other errors immediately", func(t *testing.T) {
		calls := 0
		boom := errors.New("boom")
		if err := retryOnDeadlock(ctx, func() error {
			calls++
			return boom
		}); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("honors cancellation between attempts", func(t *testing.T) {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		err := retryOnDeadlock(canceled, func() error { return deadlock })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}
