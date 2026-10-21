package notifications

import (
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSameProfileIDOnTwoAccounts covers two accounts that each have a profile
// named "default", as every account from before profiles does, following the
// same series in the same library. Each keeps its own interest row and
// switches, gets its own delivery and sees only its own inbox, and one
// account's recompute or profile deletion leaves the other's state alone.
func TestSameProfileIDOnTwoAccounts(t *testing.T) {
	pool := inboxPageDB(t)
	ctx := t.Context()
	c := seedAccessCatalog(t, pool)
	seedFanoutEvent(t, pool, c, nil)
	interests := NewInterestRepository(pool)
	preferences := NewPreferencesRepository(pool)
	deliveries := NewDeliveryRepository(pool)
	s := &System{Interests: interests, Preferences: preferences, Deliveries: deliveries}

	// Account 2 writes second, with different flags; before rows were keyed by
	// account, its write took over account 1's row.
	for _, row := range []SeriesInterest{
		{UserID: 1, ProfileID: "default", LibraryID: c.library, SeriesID: c.series, Favorite: true},
		{UserID: 2, ProfileID: "default", LibraryID: c.library, SeriesID: c.series, Favorite: true, Watchlist: true},
	} {
		if err := interests.UpsertRows(ctx, []SeriesInterest{row}); err != nil {
			t.Fatalf("UpsertRows account %d: %v", row.UserID, err)
		}
	}
	if got := interestOwners(t, pool); !slices.Equal(got, []string{"1/default", "2/default"}) {
		t.Fatalf("interest rows = %v, want one per account", got)
	}
	for _, userID := range []int{1, 2} {
		series, err := interests.ListSeriesForProfile(ctx, userID, "default")
		if err != nil || !slices.Equal(series, []string{c.series}) {
			t.Fatalf("ListSeriesForProfile(%d) = %v, %v", userID, series, err)
		}
	}

	worker := newAccessFanoutWorker(pool, scopeByProfile{"default": {}})
	for range 2 { // the second pass finds the event processed and adds nothing
		if _, err := worker.processBatch(ctx); err != nil {
			t.Fatalf("processBatch: %v", err)
		}
	}
	if got := accountRecipients(t, pool, "event-1"); !slices.Equal(got, []string{"1/default", "2/default"}) {
		t.Fatalf("event-1 recipients = %v, want both accounts' default profiles", got)
	}
	var cursors int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM profile_series_interest WHERE last_notified_episode_key IS NOT NULL`,
	).Scan(&cursors); err != nil || cursors != 2 {
		t.Fatalf("notified cursors moved on %d rows (%v), want both", cursors, err)
	}

	checkInboxPerAccount(t, deliveries)
	checkSwitchesPerAccount(t, pool, c, worker, preferences)

	// Account 1's recompute drops its rows; account 2's stay.
	if err := interests.DeleteStaleForProfileSeries(ctx, 1, "default", c.series, []int{}); err != nil {
		t.Fatalf("DeleteStaleForProfileSeries: %v", err)
	}
	if got := interestOwners(t, pool); !slices.Equal(got, []string{"2/default"}) {
		t.Fatalf("interest rows after account 1's recompute = %v, want account 2's", got)
	}

	// Deleting account 2's profile leaves account 1's deliveries and
	// switches alone.
	if err := s.PurgeProfile(ctx, 2, "default"); err != nil {
		t.Fatalf("PurgeProfile: %v", err)
	}
	if got := interestOwners(t, pool); len(got) != 0 {
		t.Fatalf("interest rows after purge = %v, want none", got)
	}
	if got := accountRecipients(t, pool, "event-1"); !slices.Equal(got, []string{"1/default"}) {
		t.Fatalf("event-1 deliveries after purge = %v, want account 1's", got)
	}
	if prefs, err := preferences.Get(ctx, 1, "default"); err != nil || prefs.Enabled {
		t.Fatalf("account 1 preferences after account 2's purge = %+v, %v; want notifications off", prefs, err)
	}
}

// TestSameProfileIDFanoutTargets covers channel targets when both accounts'
// "default" profiles follow the series: each account gets its own delivery,
// and each delivery queues attempts for its own account's webhook, web push
// subscription, and Apple push device only.
func TestSameProfileIDFanoutTargets(t *testing.T) {
	pool := inboxPageDB(t)
	ctx := t.Context()
	c := seedAccessCatalog(t, pool)
	seedFanoutEvent(t, pool, c, nil)
	seedTwoAccountTargets(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO profile_series_interest (user_id, profile_id, library_id, series_id, favorite)
		VALUES (1, 'default', $1, $2, true), (2, 'default', $1, $2, true)`, c.library, c.series); err != nil {
		t.Fatalf("seed interest: %v", err)
	}

	worker := newAccessFanoutWorker(pool, scopeByProfile{"default": {}})
	worker.settings = NewSettings(mapSettingReader{SettingFanoutSettleSeconds: "0", SettingWebhooksEnabled: "true"})
	worker.SetWebhookOutbox(NewWebhookRepository(pool), newProfileRateLimiter())
	worker.SetWebPushOutbox(NewWebPushRepository(pool))
	worker.SetPushOutbox(NewPushDeviceRepository(pool))
	if _, err := worker.processBatch(ctx); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if got := accountRecipients(t, pool, "event-1"); !slices.Equal(got, []string{"1/default", "2/default"}) {
		t.Fatalf("event-1 recipients = %v, want both accounts' default profiles", got)
	}
	for _, account := range []string{"1", "2"} {
		want := []string{"browser-" + account, "hook-" + account, "phone-" + account}
		if got := attemptTargets(t, pool, `d.release_event_id = 'event-1' AND d.user_id = `+account); !slices.Equal(got, want) {
			t.Fatalf("account %s attempts = %v, want %v", account, got, want)
		}
	}
}

// checkInboxPerAccount: each account's "default" profile lists, counts, and
// reads only its own delivery of event-1.
func checkInboxPerAccount(t *testing.T, deliveries *DeliveryRepository) {
	t.Helper()
	ctx := t.Context()
	for _, userID := range []int{1, 2} {
		rows, err := deliveries.ListInbox(ctx, userID, "default", false, 10, nil)
		if err != nil || len(rows) != 1 || rows[0].UserID != userID {
			t.Fatalf("ListInbox(%d) = %+v, %v; want only its own delivery", userID, rows, err)
		}
	}
	if marked, err := deliveries.MarkAllRead(ctx, 1, "default"); err != nil || marked != 1 {
		t.Fatalf("MarkAllRead(1) = %d, %v; want 1", marked, err)
	}
	for userID, want := range map[int]int{1: 0, 2: 1} {
		if count, err := deliveries.UnreadCount(ctx, userID, "default"); err != nil || count != want {
			t.Fatalf("UnreadCount(%d) = %d, %v; want %d", userID, count, err, want)
		}
	}
}

// checkSwitchesPerAccount: account 1 turns notifications off; account 2's
// switches are untouched and only account 2 hears about the next episode.
func checkSwitchesPerAccount(t *testing.T, pool *pgxpool.Pool, c accessCatalog, worker *FanoutWorker, preferences *PreferencesRepository) {
	t.Helper()
	ctx := t.Context()
	if _, err := preferences.Patch(ctx, 1, "default", PreferencePatch{Enabled: new(false)}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	for userID, want := range map[int]bool{1: false, 2: true} {
		if prefs, err := preferences.Get(ctx, userID, "default"); err != nil || prefs.Enabled != want {
			t.Fatalf("Get(%d).Enabled = %v, %v; want %v", userID, prefs.Enabled, err, want)
		}
	}
	seedNextEpisode(t, pool, c, "event-2", 3)
	if _, err := worker.processBatch(ctx); err != nil {
		t.Fatalf("processBatch event-2: %v", err)
	}
	if got := accountRecipients(t, pool, "event-2"); !slices.Equal(got, []string{"2/default"}) {
		t.Fatalf("event-2 recipients = %v, want account 2 only", got)
	}
}

// interestOwners lists the interest rows as account/profile.
func interestOwners(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT user_id::text || '/' || profile_id FROM profile_series_interest ORDER BY 1`)
	if err != nil {
		t.Fatalf("query interest: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read interest: %v", err)
	}
	return got
}

// seedNextEpisode adds episode 1x<number> of the catalog's series and its
// release event.
func seedNextEpisode(t *testing.T, pool *pgxpool.Pool, c accessCatalog, eventID string, number int) {
	t.Helper()
	key := EpisodeKey(1, number)
	episodeID := fmt.Sprintf("%s-e%d", c.series, number)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title)
			VALUES ($1, $2, $2 || '-s1', 1, $3, 'E')`, []any{episodeID, c.series, number}},
		{`INSERT INTO release_events (id, library_id, series_id, episode_id, season_number, episode_number,
			episode_key, available_at, dedupe_key)
			VALUES ($1, $2, $3, $4, 1, $5, $6, now(), $7)`,
			[]any{eventID, c.library, c.series, episodeID, number, key, EpisodeDedupeKey(c.library, c.series, key)}},
	} {
		if _, err := pool.Exec(t.Context(), stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed episode %d: %v", number, err)
		}
	}
}
