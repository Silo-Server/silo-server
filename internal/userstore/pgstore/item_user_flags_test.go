package pgstore

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestPostgresItemUserFlagsMatchesPerTableReads checks the one-statement read
// against the reads it replaces, including rows a history hide covers and
// plays recorded after it. Skips unless SILO_TEST_DATABASE_URL is set.
func TestPostgresItemUserFlagsMatchesPerTableReads(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := seedTestUser(t, pool, "item-user-flags")
	store := newStore(pool, userID)
	const profile = "p1"
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	hiddenBefore := base.Add(30 * time.Minute)
	afterHide := base.Add(45 * time.Minute)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mustAdd := func(_ bool, err error) { t.Helper(); must(err) }
	mustAdd(store.AddFavorite(ctx, profile, "fav"))
	mustAdd(store.AddFavorite(ctx, "p2", "other-profile"))
	mustAdd(store.AddToWatchlist(ctx, profile, "watch"))
	mustAdd(store.MarkNotInterested(ctx, profile, "skip"))
	for _, p := range []struct {
		id        string
		completed bool
		at        time.Time
	}{
		{"progress-done", true, base},
		{"progress-partial", false, base},
		{"progress-hidden", true, base},
		{"progress-after-hide", true, afterHide},
		{"not-played-id", true, base},
		{"episode-only", true, base},
	} {
		must(store.SetProgressAt(ctx, profile, p.id, 50, 100, p.completed, p.at))
	}
	for _, h := range []struct {
		id        string
		completed bool
		at        time.Time
	}{
		{"history-done", true, base},
		{"history-incomplete", false, base},
		{"history-hidden", true, base},
		{"history-after-hide", true, afterHide},
	} {
		must(store.AddHistory(ctx, userstore.WatchHistoryEntry{
			ProfileID: profile, MediaItemID: h.id, WatchedAt: h.at.Format(time.RFC3339), Completed: h.completed,
		}))
	}
	// Rows written before a hide stay stored, as after an import or a write
	// racing the hide; reads must skip them.
	_, err := pool.Exec(ctx, `
		INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before)
		SELECT $1, $2, unnest($3::text[]), $4`,
		userID, profile, []string{"progress-hidden", "progress-after-hide", "history-hidden", "history-after-hide"}, hiddenBefore)
	must(err)

	ids := []string{
		"fav", "watch", "skip", "other-profile", "progress-done", "progress-partial", "progress-hidden",
		"progress-after-hide", "not-played-id", "history-done", "history-incomplete", "history-hidden",
		"history-after-hide", "missing",
	}
	playedIDs := []string{
		"fav", "progress-done", "progress-partial", "progress-hidden", "progress-after-hide", "history-done",
		"history-incomplete", "history-hidden", "history-after-hide", "missing", "episode-only",
	}
	got, ok, err := store.ItemUserFlags(ctx, profile, ids, playedIDs)
	if err != nil || !ok {
		t.Fatalf("ItemUserFlags: ok=%v err=%v", ok, err)
	}

	favorites, err := store.ListFavoritesByMediaItems(ctx, profile, ids)
	must(err)
	watchlist, err := store.ListWatchlistByMediaItems(ctx, profile, ids)
	must(err)
	notInterested, err := store.ListNotInterestedByMediaItems(ctx, profile, ids)
	must(err)
	progress, err := userstore.ListProgressWithCompletedHistory(ctx, store, profile, playedIDs)
	must(err)
	want := map[string]userstore.ItemUserFlags{
		"fav":                 {Favorite: true},
		"watch":               {InWatchlist: true},
		"skip":                {NotInterested: true},
		"progress-done":       {Played: true},
		"progress-after-hide": {Played: true},
		"history-done":        {Played: true},
		"history-after-hide":  {Played: true},
		"episode-only":        {Played: true},
	}
	for _, id := range append(append([]string{}, ids...), "episode-only") {
		perTable := userstore.ItemUserFlags{
			Favorite:      favorites[id],
			InWatchlist:   watchlist[id],
			NotInterested: notInterested[id],
			Played:        progress[id].Completed,
		}
		if got[id] != perTable || got[id] != want[id] {
			t.Errorf("%s: got %+v, per-table reads %+v, want %+v", id, got[id], perTable, want[id])
		}
	}
}
