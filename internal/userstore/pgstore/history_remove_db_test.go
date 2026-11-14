package pgstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestPostgresRemoveHistoryItemsDeletesTheirPlaysDB checks the rows a removal
// deletes, not only what the history listing shows: the listing hides plays
// behind the removal cutoff whether or not they were deleted. Removing a show's
// episodes deletes every stored play of them for that profile (#2997), and
// leaves other titles and other profiles alone.
func TestPostgresRemoveHistoryItemsDeletesTheirPlaysDB(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	userID := seedTestUser(t, pool, "remove-history")
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, table := range []string{"user_watch_history", "user_history_hidden_items", "user_watch_progress", "user_profiles"} {
			_, _ = pool.Exec(cleanup, `DELETE FROM `+table+` WHERE user_id = $1`, userID)
		}
	})
	store := newStore(pool, userID)
	for _, id := range []string{"p1", "p2"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}

	const episodes = 20
	watched := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	removed := make([]string, 0, episodes)
	add := func(id, profile, item string, at time.Time) {
		t.Helper()
		if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{ID: id, ProfileID: profile, MediaItemID: item, WatchedAt: at.Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
	}
	for n := range episodes {
		item := fmt.Sprintf("show-ep%04d", n)
		removed = append(removed, item)
		if n%2 == 0 {
			add("p1-"+item, "p1", item, watched.Add(time.Duration(n)*time.Minute))
		}
	}
	add("p1-rewatch", "p1", "show-ep0000", watched.Add(48*time.Hour))
	add("p1-other", "p1", "other-title", watched)
	add("p2-ep0000", "p2", "show-ep0000", watched)

	if err := store.RemoveHistoryItems(ctx, "p1", removed, watched.Add(24*time.Hour)); err != nil {
		t.Fatalf("RemoveHistoryItems: %v", err)
	}
	count := func(profile, item string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_watch_history WHERE user_id = $1 AND profile_id = $2 AND media_item_id LIKE $3`,
			userID, profile, item).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("p1", "show-ep%"); n != 0 {
		t.Errorf("%d plays of the removed episodes are still stored, want 0", n)
	}
	if n := count("p1", "other-title"); n != 1 {
		t.Errorf("plays of another title = %d, want 1", n)
	}
	if n := count("p2", "show-ep%"); n != 1 {
		t.Errorf("another profile's plays = %d, want 1", n)
	}
}
