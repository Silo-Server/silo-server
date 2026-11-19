package notifications

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestInterestQueuesAreBounded: a profile queued for a rebuild many times
// rebuilds once, and neither the rebuild queue nor the item mutations grow
// past their bounds.
func TestInterestQueuesAreBounded(t *testing.T) {
	u := NewInterestUpdater(nil, nil, nil, nil)
	var rebuilt []string
	u.rebuildProfile = func(_ context.Context, _ int, profileID string) error {
		rebuilt = append(rebuilt, profileID)
		return nil
	}
	for range 3 {
		u.QueueProfileRebuild(1, "a")
	}
	for i := range interestMaxQueuedRebuilds + 10 {
		u.QueueProfileRebuild(2+i, "b")
	}
	if len(u.rebuilds) != interestMaxQueuedRebuilds {
		t.Fatalf("%d rebuilds queued, want %d", len(u.rebuilds), interestMaxQueuedRebuilds)
	}
	for range interestMaxQueuedRebuilds + 5 {
		u.runRebuild(t.Context())
	}
	a := 0
	for _, p := range rebuilt {
		if p == "a" {
			a++
		}
	}
	if a != 1 || len(rebuilt) != interestMaxQueuedRebuilds {
		t.Fatalf("rebuilt %d profiles, a %d times; want each queued profile once", len(rebuilt), a)
	}
	for i := range interestMaxPending + 10 {
		u.QueueItemMutation(1, "a", string(rune('a'+i%26))+string(rune(i)))
	}
	if len(u.pending) > interestMaxPending {
		t.Fatalf("%d mutations pending, want at most %d", len(u.pending), interestMaxPending)
	}
}

// TestInterestRebuildRunsOnOneServerAtATimeDB: while one rebuild holds the
// cluster lock, another returns ErrInterestRebuildRunning. A rebuild whose
// lock session ends stops before it completes the checkpoint, the next
// rebuild takes the freed lock and completes, and a rebuild completes on a
// pool of one connection.
func TestInterestRebuildRunsOnOneServerAtATimeDB(t *testing.T) {
	pool := schemaDB(t, "interest_rebuild_lock_")
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE notification_backfill_state (LIKE public.notification_backfill_state INCLUDING ALL);
		INSERT INTO notification_backfill_state (task, last_processed_key) VALUES ('interest_rebuild', '0000000001|a')`); err != nil {
		t.Fatal(err)
	}
	completed := func() bool {
		t.Helper()
		var done bool
		if err := pool.QueryRow(ctx, `
			SELECT completed_at IS NOT NULL FROM notification_backfill_state WHERE task = 'interest_rebuild'`).Scan(&done); err != nil {
			t.Fatal(err)
		}
		return done
	}

	users := &gatedUserLister{entered: make(chan struct{}), release: make(chan struct{})}
	s := &System{pool: pool, users: users, logger: slog.New(slog.DiscardHandler)}
	first := make(chan error, 1)
	go func() { first <- s.RebuildInterest(ctx, nil) }()
	select {
	case <-users.entered:
	case err := <-first:
		t.Fatalf("first rebuild returned before listing users: %v", err)
	}

	if err := s.RebuildInterest(ctx, nil); !errors.Is(err, ErrInterestRebuildRunning) {
		t.Fatalf("rebuild while another holds the lock = %v, want ErrInterestRebuildRunning", err)
	}

	// The first rebuild's lock session ends, as when its connection drops.
	var terminated bool
	if err := pool.QueryRow(ctx, `
		SELECT pg_terminate_backend(pid, 5000) FROM pg_locks
		WHERE locktype = 'advisory' AND objsubid = 1 AND granted
			AND (classid::bigint << 32 | objid::bigint) = $1
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, interestRebuildLock).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("end the lock session: terminated %v, %v", terminated, err)
	}
	close(users.release)
	if err := <-first; err == nil || errors.Is(err, ErrInterestRebuildRunning) {
		t.Fatalf("rebuild that lost its lock = %v, want a lost-lock error", err)
	}
	if completed() {
		t.Fatal("the rebuild that lost its lock completed the checkpoint")
	}

	if err := s.RebuildInterest(ctx, nil); err != nil {
		t.Fatalf("rebuild after the lock was freed: %v", err)
	}
	if !completed() {
		t.Fatal("the rebuild did not complete the checkpoint")
	}

	// The lock holds no pool connection, so a pool of one still rebuilds.
	oneCfg := pool.Config()
	oneCfg.MaxConns = 1
	onePool, err := pgxpool.NewWithConfig(ctx, oneCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(onePool.Close)
	released := make(chan struct{})
	close(released)
	single := &System{pool: onePool, users: &gatedUserLister{entered: make(chan struct{}), release: released}, logger: slog.New(slog.DiscardHandler)}
	singleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := single.RebuildInterest(singleCtx, nil); err != nil {
		t.Fatalf("rebuild on a pool of one connection: %v", err)
	}
}

// gatedUserLister lists no users. Its first List waits for release, holding
// that rebuild inside its run.
type gatedUserLister struct {
	entered, release chan struct{}
	calls            atomic.Int32
}

func (u *gatedUserLister) List(ctx context.Context) ([]*models.User, error) {
	if u.calls.Add(1) == 1 {
		close(u.entered)
		select {
		case <-u.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, nil
}
