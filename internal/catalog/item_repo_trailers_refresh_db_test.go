package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestTryClaimTrailersRefresh exercises the cooldown gate against a real
// database: the check-and-set is a single UPDATE precisely so two concurrent
// viewers cannot both win it, and that guarantee lives entirely in SQL — a
// fake cannot verify it.
func TestTryClaimTrailersRefresh(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := NewItemRepository(pool)
	contentID := fmt.Sprintf("trailer-claim-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Trailer Claim', 'matched', '{}'::text[])
	`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	const cooldown = 7 * 24 * time.Hour

	claimed, requestedAt, err := repo.TryClaimTrailersRefresh(ctx, contentID, cooldown)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimed {
		t.Fatal("first claim on a NULL timestamp must win")
	}
	if requestedAt != nil {
		t.Fatalf("winning claim must not report a timestamp, got %v", requestedAt)
	}

	claimed, requestedAt, err = repo.TryClaimTrailersRefresh(ctx, contentID, cooldown)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatal("second claim inside the window must lose")
	}
	if requestedAt == nil {
		t.Fatal("losing claim must report the stored timestamp for next-allowed math")
	}
	if time.Since(*requestedAt) > time.Minute {
		t.Fatalf("stored timestamp = %s, want approximately now", requestedAt)
	}

	// Backdating past the window reopens the gate.
	if _, err := pool.Exec(ctx, `
		UPDATE media_items SET trailers_refresh_requested_at = NOW() - INTERVAL '8 days'
		WHERE content_id = $1`, contentID); err != nil {
		t.Fatalf("backdate timestamp: %v", err)
	}
	claimed, _, err = repo.TryClaimTrailersRefresh(ctx, contentID, cooldown)
	if err != nil {
		t.Fatalf("claim after cooldown lapsed: %v", err)
	}
	if !claimed {
		t.Fatal("claim must win once the stored timestamp predates the window")
	}

	// A missing item is distinguishable from a cooldown: the follow-up read
	// finds no row.
	_, _, err = repo.TryClaimTrailersRefresh(ctx, contentID+"-missing", cooldown)
	if !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("missing item err = %v, want ErrItemNotFound", err)
	}
}

// TestTryClaimTrailersRefreshIsAtomic runs concurrent claims against one item;
// exactly one may win.
func TestTryClaimTrailersRefreshIsAtomic(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := NewItemRepository(pool)
	contentID := fmt.Sprintf("trailer-claim-race-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Trailer Claim Race', 'matched', '{}'::text[])
	`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	const workers = 8
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			claimed, _, err := repo.TryClaimTrailersRefresh(ctx, contentID, 7*24*time.Hour)
			if err != nil {
				errs <- err
				return
			}
			results <- claimed
		}()
	}
	close(start)

	wins := 0
	for i := 0; i < workers; i++ {
		select {
		case err := <-errs:
			t.Fatalf("concurrent claim: %v", err)
		case claimed := <-results:
			if claimed {
				wins++
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for concurrent claims")
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent claims won %d times, want exactly 1", wins)
	}
}
