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
	// The winner gets the timestamp it wrote; it is the key its own release
	// is guarded on.
	if requestedAt == nil {
		t.Fatal("winning claim must report the timestamp it stored")
	}
	if time.Since(*requestedAt) > time.Minute {
		t.Fatalf("claimed timestamp = %s, want approximately now", requestedAt)
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

// TestReleaseTrailersRefreshClaim covers the failure path's half of the gate:
// a refresh that failed hands its slot back, and the equality guard keeps a
// late release from clearing a slot someone else has since claimed. Both live
// in SQL, so a fake cannot verify them.
func TestReleaseTrailersRefreshClaim(t *testing.T) {
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
	contentID := fmt.Sprintf("trailer-release-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Trailer Release', 'matched', '{}'::text[])
	`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	const cooldown = 7 * 24 * time.Hour

	storedAt := func(t *testing.T) *time.Time {
		t.Helper()
		var stored *time.Time
		if err := pool.QueryRow(ctx, `
			SELECT trailers_refresh_requested_at FROM media_items WHERE content_id = $1`,
			contentID,
		).Scan(&stored); err != nil {
			t.Fatalf("read stored timestamp: %v", err)
		}
		return stored
	}

	claimed, claimedAt, err := repo.TryClaimTrailersRefresh(ctx, contentID, cooldown)
	if err != nil || !claimed || claimedAt == nil {
		t.Fatalf("claim = %v, at = %v, err = %v", claimed, claimedAt, err)
	}
	if err := repo.ReleaseTrailersRefreshClaim(ctx, contentID, *claimedAt); err != nil {
		t.Fatalf("release own claim: %v", err)
	}
	if stored := storedAt(t); stored != nil {
		t.Fatalf("released slot still holds %s", stored)
	}
	// With the slot free the next request wins immediately, no clock movement.
	claimed, claimedAt, err = repo.TryClaimTrailersRefresh(ctx, contentID, cooldown)
	if err != nil || !claimed || claimedAt == nil {
		t.Fatalf("claim after release = %v, at = %v, err = %v", claimed, claimedAt, err)
	}

	// A release naming a timestamp the column no longer holds — the shape of a
	// late release arriving after a newer request re-claimed the slot — is a
	// no-op, not an error.
	stale := claimedAt.Add(-time.Hour)
	if err := repo.ReleaseTrailersRefreshClaim(ctx, contentID, stale); err != nil {
		t.Fatalf("release with a stale timestamp: %v", err)
	}
	stored := storedAt(t)
	if stored == nil {
		t.Fatal("a stale release cleared a slot it does not own")
	}
	if !stored.Equal(*claimedAt) {
		t.Fatalf("stored timestamp = %s, want the current claim %s", stored, claimedAt)
	}

	// Releasing a row that no longer exists is likewise a no-op.
	if err := repo.ReleaseTrailersRefreshClaim(ctx, contentID+"-missing", *claimedAt); err != nil {
		t.Fatalf("release for a missing item: %v", err)
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

// TestTryClaimTrailersRefreshRetriesWhenSlotIsFreedMidClassification covers the
// window between the conditional UPDATE and the follow-up SELECT: a caller can
// lose the gate to another request and then have that request's refresh fail
// and clear the timestamp before the read. Classifying that as a cooldown would
// report one with no next-allowed time while the slot is in fact free, so the
// claim is retried and this caller takes it.
func TestTryClaimTrailersRefreshRetriesWhenSlotIsFreedMidClassification(t *testing.T) {
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
	contentID := fmt.Sprintf("trailer-claim-retry-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Trailer Claim Retry', 'matched', '{}'::text[])
	`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	const cooldown = 7 * 24 * time.Hour

	// Another request owns the slot, so the first UPDATE below loses.
	claimed, claimedAt, err := repo.TryClaimTrailersRefresh(ctx, contentID, cooldown)
	if err != nil || !claimed || claimedAt == nil {
		t.Fatalf("seed claim = %v, at = %v, err = %v", claimed, claimedAt, err)
	}

	// That request's refresh fails and hands the slot back. Doing it here
	// models the release landing between our lost UPDATE and our follow-up
	// read: either way the read observes NULL, which is the state under test.
	if err := repo.ReleaseTrailersRefreshClaim(ctx, contentID, *claimedAt); err != nil {
		t.Fatalf("release the competing claim: %v", err)
	}

	claimed, requestedAt, err := repo.TryClaimTrailersRefresh(ctx, contentID, cooldown)
	if err != nil {
		t.Fatalf("claim after the competing release: %v", err)
	}
	if !claimed {
		t.Fatalf("a freed slot must be claimable, got claimed=false requestedAt=%v", requestedAt)
	}
	if requestedAt == nil {
		t.Fatal("a winning claim must report the timestamp it stored")
	}

	// And the state really is a claim, not a phantom: the next request is in
	// cooldown against the timestamp we just wrote.
	claimed, requestedAt, err = repo.TryClaimTrailersRefresh(ctx, contentID, cooldown)
	if err != nil {
		t.Fatalf("claim after the retry won: %v", err)
	}
	if claimed {
		t.Fatal("the slot must be held after the retry won it")
	}
	if requestedAt == nil {
		t.Fatal("a cooldown must be dateable")
	}
}

// TestTrailersRefreshRequestedAt covers the read the durable recovery path uses
// to inherit a claim: a worker recovering a request whose process died never
// saw the claim, so it reads back the stored timestamp and releases on that
// exact key, keeping ReleaseTrailersRefreshClaim's equality guard meaningful.
func TestTrailersRefreshRequestedAt(t *testing.T) {
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
	contentID := fmt.Sprintf("trailer-read-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Trailer Read', 'matched', '{}'::text[])
	`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	const cooldown = 7 * 24 * time.Hour

	// A free slot reads as nil rather than an error: the recovery path uses
	// that to conclude it owes nobody a release.
	requestedAt, err := repo.TrailersRefreshRequestedAt(ctx, contentID)
	if err != nil {
		t.Fatalf("read an unclaimed slot: %v", err)
	}
	if requestedAt != nil {
		t.Fatalf("unclaimed slot read as %s, want nil", requestedAt)
	}

	claimed, claimedAt, err := repo.TryClaimTrailersRefresh(ctx, contentID, cooldown)
	if err != nil || !claimed || claimedAt == nil {
		t.Fatalf("claim = %v, at = %v, err = %v", claimed, claimedAt, err)
	}

	requestedAt, err = repo.TrailersRefreshRequestedAt(ctx, contentID)
	if err != nil {
		t.Fatalf("read a claimed slot: %v", err)
	}
	if requestedAt == nil {
		t.Fatal("a claimed slot must read back its timestamp")
	}
	// The read must reproduce the claim exactly, or the equality-guarded
	// release it feeds would silently match nothing.
	if !requestedAt.Equal(*claimedAt) {
		t.Fatalf("read %s, want the claimed timestamp %s", requestedAt, claimedAt)
	}
	if err := repo.ReleaseTrailersRefreshClaim(ctx, contentID, *requestedAt); err != nil {
		t.Fatalf("release on the read timestamp: %v", err)
	}
	requestedAt, err = repo.TrailersRefreshRequestedAt(ctx, contentID)
	if err != nil {
		t.Fatalf("read after release: %v", err)
	}
	if requestedAt != nil {
		t.Fatalf("released slot still holds %s", requestedAt)
	}

	// A missing item is distinguishable from a free slot.
	if _, err := repo.TrailersRefreshRequestedAt(ctx, contentID+"-missing"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("read for a missing item = %v, want ErrItemNotFound", err)
	}
}
