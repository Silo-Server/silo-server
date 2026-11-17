package notifications

import (
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/watchlist"
)

// TestWatchlistArrivalTellsEachWatcherOnce covers the watchlist arrival
// notice: a series arriving tells the profiles whose watchlist waits for it,
// matched by any TMDB ID the title held, once each, and also tells a profile
// whose entry promotion already moved onto the library watchlist. It leaves
// out profiles that added the series after it arrived, the profiles a request
// of the title tells instead (its requester and followers, or a profile it
// already told), profiles with notifications off, and profiles that may not
// open the series. Once the series is older than the staleness horizon, a new
// episode tells nobody.
func TestWatchlistArrivalTellsEachWatcherOnce(t *testing.T) {
	pool := inboxPageDB(t)
	ctx := t.Context()
	c := seedAccessCatalog(t, pool)
	seedFanoutEvent(t, pool, c, nil)
	seedWatchlistArrival(t, pool, c)

	worker := newAccessFanoutWorker(pool, scopeByProfile{
		"watcher": {}, "requester": {}, "follower": {}, "told": {}, "off": {}, "promoted": {}, "later": {}, "later-entry": {},
		"failed-requester": {},
		"blocked":          {AllowedLibraryIDs: []int{c.otherLibrary + 1}},
	})
	worker.watchlist = &watchlist.Arrivals{}
	worker.requests = requests.NewRepository(pool, nil)
	arrivals := func() []string {
		t.Helper()
		return deliveryRecipientsByAccount(t, pool, `type = 'watchlist.available' AND series_id = '`+c.series+`'
			AND reason_flags->>'tmdb_id' = '424242' AND reason_flags->>'media_type' = 'series'`)
	}

	if _, err := worker.processBatch(ctx); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	want := []string{"1/promoted", "1/watcher", "2/watcher"}
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
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'declined' WHERE id = 'req-open'`); err != nil {
		t.Fatal(err)
	}
	nextEvent("event-3", 4)
	want = []string{"1/follower", "1/promoted", "1/requester", "1/watcher", "2/watcher"}
	if got := arrivals(); !slices.Equal(got, want) {
		t.Fatalf("watchlist arrivals after the decline = %v, want %v", got, want)
	}

	// Once the series is older than the staleness horizon, its episodes are
	// no arrival, even for a waiting profile with no notice in its inbox (as
	// after delivery retention).
	for _, stmt := range []string{
		`UPDATE media_items SET created_at = now() - interval '10 days' WHERE content_id = '` + c.series + `'`,
		`INSERT INTO user_watchlist_titles (user_id, profile_id, title_id, added_at) VALUES (3, 'watcher', 1, now() - interval '20 days')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	nextEvent("event-4", 5)
	if got := arrivals(); !slices.Equal(got, want) {
		t.Fatalf("watchlist arrivals for a series past the horizon = %v, want %v", got, want)
	}

	// Retention keeps a notice younger than the staleness horizon whatever
	// its settings say, since the notice is what stops a repeat, and removes
	// it once it is older.
	future := time.Now().Add(time.Hour)
	deliveries := NewDeliveryRepository(pool)
	if _, err := deliveries.DeleteOld(ctx, future, future, time.Now().Add(-72*time.Hour)); err != nil {
		t.Fatalf("DeleteOld inside the horizon: %v", err)
	}
	if got := arrivals(); !slices.Equal(got, want) {
		t.Fatalf("watchlist arrivals after retention inside the horizon = %v, want %v", got, want)
	}
	if _, err := deliveries.DeleteOld(ctx, future, future, future); err != nil {
		t.Fatalf("DeleteOld past the horizon: %v", err)
	}
	if got := arrivals(); len(got) != 0 {
		t.Fatalf("watchlist arrivals after retention past the horizon = %v, want none", got)
	}
}

// seedWatchlistArrival creates the watchlist and request tables in the test
// schema and seeds TestWatchlistArrivalTellsEachWatcherOnce's profiles for
// c's series: watchers under its current and former TMDB IDs, one entry newer
// than the series, two promoted entries, an open request with its requester
// and a follower, a recently failed request's requester, a profile a request
// already told, and one with notifications off.
func seedWatchlistArrival(t *testing.T, pool *pgxpool.Pool, c accessCatalog) {
	t.Helper()
	ctx := t.Context()
	const currentTMDB, formerTMDB = 424242, 4242
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`CREATE TABLE watchlist_titles (LIKE public.watchlist_titles INCLUDING ALL);
		  CREATE TABLE watchlist_title_aliases (LIKE public.watchlist_title_aliases INCLUDING ALL);
		  CREATE TABLE user_watchlist_titles (LIKE public.user_watchlist_titles INCLUDING ALL);
		  CREATE TABLE watchlist_title_promotions (LIKE public.watchlist_title_promotions INCLUDING ALL);
		  CREATE TABLE media_requests (LIKE public.media_requests INCLUDING ALL);
		  CREATE TABLE media_request_targets (LIKE public.media_request_targets INCLUDING ALL);
		  CREATE TABLE media_request_follows (LIKE public.media_request_follows INCLUDING ALL)`, nil},
		{`UPDATE media_items SET tmdb_id = $2 WHERE content_id = $1`, []any{c.series, "424242"}},
		{`INSERT INTO watchlist_titles (id, media_type, tmdb_id, title) VALUES (1, 'series', $1, 'Series')`, []any{currentTMDB}},
		{`INSERT INTO watchlist_title_aliases (title_id, media_type, provider, provider_id)
			VALUES (1, 'series', 'tmdb', $1), (1, 'series', 'tmdb', $2)`, []any{"424242", "4242"}},
		{`INSERT INTO user_watchlist_titles (user_id, profile_id, title_id, added_at)
			SELECT user_id, profile_id, 1, now() - interval '1 day'
			FROM (VALUES (1, 'watcher'), (2, 'watcher'), (1, 'requester'), (1, 'follower'),
			             (1, 'told'), (1, 'off'), (1, 'blocked'), (1, 'failed-requester')) AS p(user_id, profile_id)`, nil},
		// A profile whose entry is newer than the series was not waiting.
		{`INSERT INTO user_watchlist_titles (user_id, profile_id, title_id, added_at)
			VALUES (1, 'later-entry', 1, now() + interval '1 minute')`, nil},
		// Promotion moved one entry onto the library watchlist before fanout
		// ran, keeping the entry's added_at; another profile's promoted entry
		// is newer than the series.
		{`INSERT INTO watchlist_title_promotions (user_id, profile_id, media_item_id, title_added_at) VALUES
			(1, 'promoted', $1, now() - interval '1 day'), (1, 'later', $1, now() + interval '1 minute')`, []any{c.series}},
		// The open request was made under the title's former TMDB ID.
		{`INSERT INTO media_requests (id, media_type, tmdb_id, title, status, requested_by_user_id, requested_by_profile_id)
			VALUES ('req-open', 'series', $1, 'Series', 'approved', 1, 'requester')`, []any{formerTMDB}},
		// A request that failed recently without delivering anything: the
		// library completes it and it then tells its requester.
		{`INSERT INTO media_requests (id, media_type, tmdb_id, title, status, outcome, requested_by_user_id, requested_by_profile_id)
			VALUES ('req-failed', 'series', $1, 'Series', 'approved', 'failed', 1, 'failed-requester')`, []any{currentTMDB}},
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
