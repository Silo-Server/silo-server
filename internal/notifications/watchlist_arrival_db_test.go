package notifications

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/watchlist"
)

// TestWatchlistArrivalTellsEachWatcherOnce covers the watchlist arrival
// notice: a series arriving tells the profiles whose watchlist waits for it,
// matched by any TMDB ID the title held, once each, and leaves out the
// profiles a request of the title tells instead (its requester and followers,
// or a profile it already told), profiles with notifications off, and
// profiles that may not open the series.
func TestWatchlistArrivalTellsEachWatcherOnce(t *testing.T) {
	pool := inboxPageDB(t)
	ctx := t.Context()
	c := seedAccessCatalog(t, pool)
	seedFanoutEvent(t, pool, c, nil)
	const currentTMDB, formerTMDB = 424242, 4242
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`CREATE TABLE watchlist_titles (LIKE public.watchlist_titles INCLUDING ALL);
		  CREATE TABLE watchlist_title_aliases (LIKE public.watchlist_title_aliases INCLUDING ALL);
		  CREATE TABLE user_watchlist_titles (LIKE public.user_watchlist_titles INCLUDING ALL);
		  CREATE TABLE media_requests (LIKE public.media_requests INCLUDING ALL);
		  CREATE TABLE media_request_follows (LIKE public.media_request_follows INCLUDING ALL)`, nil},
		{`UPDATE media_items SET tmdb_id = $2 WHERE content_id = $1`, []any{c.series, "424242"}},
		{`INSERT INTO watchlist_titles (id, media_type, tmdb_id, title) VALUES (1, 'series', $1, 'Series')`, []any{currentTMDB}},
		{`INSERT INTO watchlist_title_aliases (title_id, media_type, provider, provider_id)
			VALUES (1, 'series', 'tmdb', $1), (1, 'series', 'tmdb', $2)`, []any{"424242", "4242"}},
		{`INSERT INTO user_watchlist_titles (user_id, profile_id, title_id) VALUES
			(1, 'watcher', 1), (2, 'watcher', 1), (1, 'requester', 1), (1, 'follower', 1),
			(1, 'told', 1), (1, 'off', 1), (1, 'blocked', 1)`, nil},
		// The open request was made under the title's former TMDB ID.
		{`INSERT INTO media_requests (id, media_type, tmdb_id, title, status, requested_by_user_id, requested_by_profile_id)
			VALUES ('req-open', 'series', $1, 'Series', 'approved', 1, 'requester')`, []any{formerTMDB}},
		{`INSERT INTO media_request_follows (media_type, tmdb_id, user_id, profile_id, request_id)
			VALUES ('series', $1, 1, 'follower', 'req-open')`, []any{formerTMDB}},
		{`INSERT INTO notification_deliveries (id, user_id, profile_id, series_id, type, reason_flags)
			VALUES ('told-before', 1, 'told', $1, 'request.fulfilled', $2)`,
			[]any{c.series, `{"request_id":"req-done","tmdb_id":424242,"media_type":"series"}`}},
		{`INSERT INTO notification_preferences (user_id, profile_id, enabled, notify_next_up) VALUES (1, 'off', false, true)`, nil},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	worker := newAccessFanoutWorker(pool, scopeByProfile{
		"watcher": {}, "requester": {}, "follower": {}, "told": {}, "off": {},
		"blocked": {AllowedLibraryIDs: []int{c.otherLibrary + 1}},
	})
	worker.watchlist = watchlist.NewArrivals(pool, catalog.NewItemRepository(pool))
	worker.requests = requests.NewRepository(pool, nil)
	arrivals := func() []string {
		t.Helper()
		return deliveryRecipientsByAccount(t, pool, `type = 'watchlist.available' AND series_id = '`+c.series+`'
			AND reason_flags->>'tmdb_id' = '424242' AND reason_flags->>'media_type' = 'series'`)
	}

	if _, err := worker.processBatch(ctx); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	want := []string{"1/watcher", "2/watcher"}
	if got := arrivals(); !slices.Equal(got, want) {
		t.Fatalf("watchlist arrivals = %v, want %v", got, want)
	}

	nextEvent := func(id string, episode int) {
		t.Helper()
		key := EpisodeKey(1, episode)
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title)
			VALUES ($1 || '-' || $2, $1, $1 || '-s1', 1, $3, $2)`, c.series, id, episode); err != nil {
			t.Fatalf("seed episode: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO release_events (id, library_id, series_id, episode_id, season_number, episode_number,
				episode_key, available_at, dedupe_key)
			VALUES ($1, $2, $3, $3 || '-' || $1, 1, $4, $5, now(), $6)`,
			id, c.library, c.series, episode, key, EpisodeDedupeKey(c.library, c.series, key)); err != nil {
			t.Fatalf("seed release event: %v", err)
		}
		if _, err := worker.processBatch(ctx); err != nil {
			t.Fatalf("processBatch %s: %v", id, err)
		}
	}

	// The next episode of the series is no new arrival.
	nextEvent("event-2", 3)
	if got := arrivals(); !slices.Equal(got, want) {
		t.Fatalf("watchlist arrivals after a second episode = %v, want %v", got, want)
	}

	// A declined request no longer tells its requester or followers, so the
	// watchlist does.
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'declined'`); err != nil {
		t.Fatal(err)
	}
	nextEvent("event-3", 4)
	want = []string{"1/follower", "1/requester", "1/watcher", "2/watcher"}
	if got := arrivals(); !slices.Equal(got, want) {
		t.Fatalf("watchlist arrivals after the decline = %v, want %v", got, want)
	}
}

// deliveryRecipientsByAccount lists the account/profile of each delivery the
// condition selects, sorted.
func deliveryRecipientsByAccount(t *testing.T, pool *pgxpool.Pool, where string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT user_id::text || '/' || profile_id FROM notification_deliveries WHERE `+where+` ORDER BY 1`)
	if err != nil {
		t.Fatalf("query deliveries: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read deliveries: %v", err)
	}
	return got
}
