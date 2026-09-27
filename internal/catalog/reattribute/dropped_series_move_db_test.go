package reattribute

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestMoveDroppedSeriesPairsDB checks that a profile's drop follows its series,
// and that the later drop wins when the profile dropped both series.
func TestMoveDroppedSeriesPairsDB(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	from := fmt.Sprintf("reattr-drop-from-%d", env.suffix)
	to := fmt.Sprintf("reattr-drop-to-%d", env.suffix)
	onlyFrom := fmt.Sprintf("reattr-drop-only-%d", env.suffix)
	onlyTo := fmt.Sprintf("reattr-drop-only-to-%d", env.suffix)
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(24 * time.Hour)
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO user_dropped_series (user_id, profile_id, series_id, dropped_at)
		VALUES ($1, $2, $3, $5), ($1, $2, $4, $6), ($1, $2, $7, $6)
	`, env.userID, env.profileID, from, to, newer, older, onlyFrom); err != nil {
		t.Fatalf("seed drops: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(ctx, `DELETE FROM user_dropped_series WHERE user_id = $1`, env.userID)
	})

	tx, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	moved, err := moveDroppedSeriesPairs(ctx, tx, []string{from, onlyFrom}, []string{to, onlyTo})
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("moved = %d, want the one drop without a collision", moved)
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT dropped_at FROM user_dropped_series WHERE user_id = $1 AND series_id = $2`, env.userID, to).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(newer) {
		t.Fatalf("merged dropped_at = %s, want the later drop %s", at, newer)
	}
	var left int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_dropped_series WHERE user_id = $1 AND series_id = ANY($2)`, env.userID, []string{from, onlyFrom}).Scan(&left); err != nil || left != 0 {
		t.Fatalf("source rows left = %d (%v), want 0", left, err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_dropped_series WHERE user_id = $1 AND series_id = $2`, env.userID, onlyTo).Scan(&left); err != nil || left != 1 {
		t.Fatalf("moved rows = %d (%v), want 1", left, err)
	}
}
