package reattribute

import (
	"context"
	"fmt"
	"testing"
)

// TestMoveRatingSyncPairsDB checks that watch-provider agreed ratings follow a
// reattribution unconfirmed and without their provider key, and that the
// destination's own row wins a collision.
func TestMoveRatingSyncPairsDB(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if _, err := env.pool.Exec(ctx, `INSERT INTO user_profiles (user_id, id, name) VALUES ($1, $2, 'Reattr')`, env.userID, env.profileID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	var connectionID string
	if err := env.pool.QueryRow(ctx, `
		INSERT INTO watch_provider_connections (provider, user_id, profile_id)
		VALUES ('reattr-test', $1, $2) RETURNING id::text
	`, env.userID, env.profileID).Scan(&connectionID); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(ctx, `DELETE FROM watch_provider_connections WHERE id = $1::uuid`, connectionID)
	})
	from := fmt.Sprintf("reattr-from-%d", env.suffix)
	to := fmt.Sprintf("reattr-to-%d", env.suffix)
	collidingFrom := fmt.Sprintf("reattr-cfrom-%d", env.suffix)
	collidingTo := fmt.Sprintf("reattr-cto-%d", env.suffix)
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO watch_provider_rating_items (connection_id, media_item_id, kind, provider_item_key, synced_rating, remote_seen)
		VALUES ($1::uuid, $2, 'movie', 'imdb:tt1', 4, true),
		       ($1::uuid, $3, 'movie', 'imdb:tt2', 2, true),
		       ($1::uuid, $4, 'movie', 'imdb:tt3', 5, true)
	`, connectionID, from, collidingFrom, collidingTo); err != nil {
		t.Fatalf("seed agreed ratings: %v", err)
	}

	tx, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := moveRatingSyncPairs(ctx, tx, []string{from, collidingFrom}, []string{to, collidingTo}); err != nil {
		t.Fatal(err)
	}

	var rating int
	var seen bool
	var key string
	if err := tx.QueryRow(ctx, `
		SELECT synced_rating, remote_seen, provider_item_key FROM watch_provider_rating_items
		WHERE connection_id = $1::uuid AND media_item_id = $2
	`, connectionID, to).Scan(&rating, &seen, &key); err != nil {
		t.Fatalf("moved row: %v", err)
	}
	if rating != 4 || seen || key != "" {
		t.Fatalf("moved row = rating %d seen %v key %q, want 4, unconfirmed, no key", rating, seen, key)
	}
	if err := tx.QueryRow(ctx, `
		SELECT synced_rating FROM watch_provider_rating_items
		WHERE connection_id = $1::uuid AND media_item_id = $2
	`, connectionID, collidingTo).Scan(&rating); err != nil || rating != 5 {
		t.Fatalf("destination row = %d (%v), want its own 5 to win", rating, err)
	}
	var left int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM watch_provider_rating_items
		WHERE connection_id = $1::uuid AND media_item_id = ANY($2)
	`, connectionID, []string{from, collidingFrom}).Scan(&left); err != nil || left != 0 {
		t.Fatalf("source rows left = %d (%v), want 0", left, err)
	}
}
