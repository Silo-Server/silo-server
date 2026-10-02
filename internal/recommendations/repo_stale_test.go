package recommendations

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestMarkProfileStaleWritesOnlyWhenNotAlreadyStale counts the row versions
// repeated stale marks write. A profile already waiting for the sweep keeps
// its pending mark instead of rewriting the row, and a mark after a refresh
// consumed the previous one (or after ClearStaleAt) makes it pending again.
func TestMarkProfileStaleWritesOnlyWhenNotAlreadyStale(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := context.Background()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		"taste-stale-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	var dims int
	if err := pool.QueryRow(ctx, `
		SELECT atttypmod FROM pg_attribute
		WHERE attrelid = 'public.user_taste_profiles'::regclass AND attname = 'embedding'`).Scan(&dims); err != nil {
		t.Fatalf("read embedding dimensions: %v", err)
	}
	repo := NewRepo(pool)
	if err := repo.UpsertTasteProfile(ctx, userID, "p1", make([]float32, dims), map[string]int{}, ""); err != nil {
		t.Fatalf("seed taste profile: %v", err)
	}
	// Start from a profile refreshed an hour ago and never marked since.
	if _, err := pool.Exec(ctx, `
		UPDATE user_taste_profiles SET updated_at = NOW() - interval '1 hour', stale_at = NULL
		WHERE user_id = $1 AND profile_id = 'p1'`, userID); err != nil {
		t.Fatalf("backdate taste profile: %v", err)
	}

	rowVersion := func() string {
		t.Helper()
		var xmin string
		if err := pool.QueryRow(ctx, `SELECT xmin::text FROM user_taste_profiles WHERE user_id = $1 AND profile_id = 'p1'`, userID).Scan(&xmin); err != nil {
			t.Fatalf("read row version: %v", err)
		}
		return xmin
	}
	pending := func() bool {
		t.Helper()
		stale, err := repo.GetStaleProfiles(ctx, 1000)
		if err != nil {
			t.Fatalf("get stale profiles: %v", err)
		}
		for _, p := range stale {
			if p.UserID == userID && p.ProfileID == "p1" {
				return true
			}
		}
		return false
	}
	markCountingWrites := func(marks int) int {
		t.Helper()
		writes := 0
		for range marks {
			before := rowVersion()
			if err := repo.MarkProfileStale(ctx, userID, "p1"); err != nil {
				t.Fatalf("mark profile stale: %v", err)
			}
			if rowVersion() != before {
				writes++
			}
		}
		return writes
	}

	const marks = 100
	if writes := markCountingWrites(marks); writes != 1 || !pending() {
		t.Fatalf("%d marks on a fresh profile: row writes = %d, pending = %v; want 1 write and pending", marks, writes, pending())
	}

	// A refresh that finished after the last mark consumed it.
	if _, err := pool.Exec(ctx, `
		UPDATE user_taste_profiles SET stale_at = updated_at - interval '1 minute'
		WHERE user_id = $1 AND profile_id = 'p1'`, userID); err != nil {
		t.Fatalf("consume stale mark: %v", err)
	}
	if pending() {
		t.Fatal("precondition: consumed mark still pending")
	}
	if writes := markCountingWrites(marks); writes != 1 || !pending() {
		t.Fatalf("%d marks after a refresh: row writes = %d, pending = %v; want 1 write and pending", marks, writes, pending())
	}

	if err := repo.ClearStaleAt(ctx, userID, "p1"); err != nil {
		t.Fatalf("clear stale mark: %v", err)
	}
	if writes := markCountingWrites(marks); writes != 1 || !pending() {
		t.Fatalf("%d marks after ClearStaleAt: row writes = %d, pending = %v; want 1 write and pending", marks, writes, pending())
	}
}
