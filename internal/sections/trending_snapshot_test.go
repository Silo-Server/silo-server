package sections

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCanonicalTrendingKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src, win         string
		wantSrc, wantWin string
	}{
		{"tmdb", "day", "tmdb", "day"},
		{"tmdb", "week", "tmdb", "week"},
		{"tmdb", "", "tmdb", "week"},
		{"", "day", "tmdb", "day"},
		{"", "", "tmdb", "week"},
		{"trakt", "day", "trakt", "week"},
		{"trakt", "week", "trakt", "week"},
		{"trakt", "", "trakt", "week"},
		{"bogus", "bogus", "tmdb", "week"},
	}
	for _, c := range cases {
		gotSrc, gotWin := canonicalTrendingKey(c.src, c.win)
		if gotSrc != c.wantSrc || gotWin != c.wantWin {
			t.Errorf("canonicalTrendingKey(%q, %q) = (%q, %q); want (%q, %q)",
				c.src, c.win, gotSrc, gotWin, c.wantSrc, c.wantWin)
		}
	}
}

func TestRefreshLeaseReleasesSingleConnectionDuringWork(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), `
		CREATE TEMP TABLE trending_discover_snapshots (
			source text NOT NULL,
			time_window text NOT NULL,
			content_ids text[] NOT NULL DEFAULT '{}',
			entry_count integer NOT NULL DEFAULT 0,
			refreshed_at timestamptz,
			last_attempt_at timestamptz,
			last_status text NOT NULL DEFAULT '',
			last_error text NOT NULL DEFAULT '',
			PRIMARY KEY (source, time_window)
		)`); err != nil {
		t.Fatal(err)
	}

	repo := NewTrendingSnapshotRepository(pool)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	claimAt, claimed, err := repo.TryClaimRefresh(ctx, "tmdb", "day", time.Minute)
	if err != nil {
		t.Fatalf("TryClaimRefresh: %v", err)
	}
	if !claimed {
		t.Fatal("first refresh did not claim the lease")
	}
	// The only pool connection must be available while external fetching and
	// catalog resolution happen between claim and completion.
	var one int
	if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("pool connection unavailable during leased work: one=%d err=%v", one, err)
	}
	if _, claimedAgain, err := repo.TryClaimRefresh(ctx, "tmdb", "day", time.Minute); err != nil || claimedAgain {
		t.Fatalf("overlapping claim: claimed=%v err=%v, want false/nil", claimedAgain, err)
	}
	if err := repo.SaveSuccess(ctx, "tmdb", "day", []string{"last-good"}, 1, "ok", claimAt, time.Now()); err != nil {
		t.Fatalf("initial SaveSuccess: %v", err)
	}
	staleClaim, claimed, err := repo.TryClaimRefresh(ctx, "tmdb", "day", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("stale-worker claim: claimed=%v err=%v", claimed, err)
	}
	snapshot, found, err := repo.Get(ctx, "tmdb", "day")
	if err != nil || !found || len(snapshot.ContentIDs) != 1 || snapshot.ContentIDs[0] != "last-good" {
		t.Fatalf("claim did not preserve last-good snapshot: found=%v snapshot=%+v err=%v", found, snapshot, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE trending_discover_snapshots SET last_attempt_at = last_attempt_at - INTERVAL '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	replacementClaim, replacementClaimed, err := repo.TryClaimRefresh(ctx, "tmdb", "day", time.Minute)
	if err != nil || !replacementClaimed {
		t.Fatalf("replacement claim: claimed=%v err=%v, want true/nil", replacementClaimed, err)
	}
	if err := repo.SaveSuccess(ctx, "tmdb", "day", []string{"stale"}, 1, "ok", staleClaim, time.Now()); !errors.Is(err, ErrTrendingRefreshLeaseLost) {
		t.Fatalf("stale SaveSuccess error = %v, want ErrTrendingRefreshLeaseLost", err)
	}
	if err := repo.RecordAttempt(ctx, "tmdb", "day", "error", "replacement failed", replacementClaim); err != nil {
		t.Fatalf("replacement RecordAttempt: %v", err)
	}
	snapshot, found, err = repo.Get(ctx, "tmdb", "day")
	if err != nil || !found || len(snapshot.ContentIDs) != 1 || snapshot.ContentIDs[0] != "last-good" || snapshot.LastStatus != "error" {
		t.Fatalf("failed replacement did not preserve last-good snapshot: found=%v snapshot=%+v err=%v", found, snapshot, err)
	}

	staleAttemptClaim, claimed, err := repo.TryClaimRefresh(ctx, "tmdb", "day", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("stale-attempt claim: claimed=%v err=%v", claimed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE trending_discover_snapshots SET last_attempt_at = last_attempt_at - INTERVAL '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	finalClaim, claimed, err := repo.TryClaimRefresh(ctx, "tmdb", "day", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("final claim: claimed=%v err=%v", claimed, err)
	}
	if err := repo.RecordAttempt(ctx, "tmdb", "day", "error", "stale failure", staleAttemptClaim); !errors.Is(err, ErrTrendingRefreshLeaseLost) {
		t.Fatalf("stale RecordAttempt error = %v, want ErrTrendingRefreshLeaseLost", err)
	}
	if err := repo.SaveSuccess(ctx, "tmdb", "day", []string{"content-1"}, 1, "ok", finalClaim, time.Now()); err != nil {
		t.Fatalf("final SaveSuccess: %v", err)
	}
	if _, claimedAfterCompletion, err := repo.TryClaimRefresh(ctx, "tmdb", "day", time.Minute); err != nil || !claimedAfterCompletion {
		t.Fatalf("claim after completion: claimed=%v err=%v, want true/nil", claimedAfterCompletion, err)
	}
}
