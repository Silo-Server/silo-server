package pgstore

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCompactMediaItemIDsReturnsEmptySliceForNilInput(t *testing.T) {
	ids := compactMediaItemIDs(nil)
	if ids == nil {
		t.Fatal("compactMediaItemIDs(nil) returned nil, want empty slice for pgx array binding")
	}
	if len(ids) != 0 {
		t.Fatalf("len = %d, want 0", len(ids))
	}
}

func TestListProgressByMediaItemsReusedPlan(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the plan used after repeated browse requests, including when
	// the same prepared statement receives a different number of item IDs.
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
	config.MaxConns = 1
	ctx := t.Context()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	userIDs := make([]int, 0, 2)
	t.Cleanup(func() {
		_, err := pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = ANY($1)`, userIDs)
		if err != nil {
			t.Errorf("clean up users: %v", err)
		}
	})
	for i := range 2 {
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("progress-array-%d-%d", time.Now().UnixNano(), i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, id)
	}
	const quotedID = "episode-'quoted'\\path"
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress
			(user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ($1, 'a', $3, 30, 120, FALSE, $4),
		       ($1, 'a', 'hidden', 40, 120, FALSE, $4),
		       ($1, 'a', 'newer', 120, 120, TRUE, $4),
		       ($1, 'a', 'unrequested', 50, 120, FALSE, $4),
		       ($1, 'b', 'profile-only', 60, 120, FALSE, $4),
		       ($2, 'a', 'account-only', 70, 120, FALSE, $4)
	`, userIDs[0], userIDs[1], quotedID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before)
		VALUES ($1, 'a', 'hidden', $2), ($1, 'a', 'newer', $3)
	`, userIDs[0], at, at.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	store := newStore(pool, userIDs[0])
	ids := []string{quotedID, quotedID, "hidden", "newer", "profile-only", "account-only", "missing"}
	largeIDs := slices.Clone(ids)
	for i := range 5000 {
		largeIDs = append(largeIDs, fmt.Sprintf("missing-%d", i))
	}
	for _, batch := range [][]string{ids, largeIDs, ids, nil, {}} {
		got, err := store.ListProgressByMediaItems(ctx, "a", batch)
		if err != nil {
			t.Fatalf("batch size %d: %v", len(batch), err)
		}
		if len(batch) == 0 {
			if len(got) != 0 {
				t.Fatalf("empty batch returned %d items", len(got))
			}
			continue
		}
		if len(got) != 2 || got[quotedID].PositionSeconds != 30 || !got["newer"].Completed {
			t.Fatalf("batch size %d: unexpected progress: %+v", len(batch), got)
		}
	}
}
