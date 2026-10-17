package notifications

import (
	"testing"
	"time"
)

// TestListNotifiedSeriesMergesLibrariesAndAppliesPreferences covers the
// notified-series list over the fanout index: a series in two libraries is one
// entry with both libraries' reasons, an inert row and a series the catalog no
// longer has are left out, entries sort by sort title then title, and a reason
// the profile turned off stays listed but inactive.
func TestListNotifiedSeriesMergesLibrariesAndAppliesPreferences(t *testing.T) {
	pool := schemaDB(t, "notified_series_")
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE profile_series_interest (LIKE public.profile_series_interest INCLUDING ALL);
		CREATE TABLE media_items (LIKE public.media_items INCLUDING ALL);
		CREATE TABLE notification_preferences (LIKE public.notification_preferences INCLUDING ALL);
		CREATE TABLE profile_series_notification_mutes (LIKE public.profile_series_notification_mutes INCLUDING ALL)`); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	nextUp, laterNextUp := EpisodeKey(2, 3), EpisodeKey(2, 5)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id, type, title, sort_title, genres, year, latest_episode_added_at) VALUES
			('series:bear', 'series', 'The Bear', 'Bear', '{}', 2022, '2026-03-04T05:06:07Z'),
			('series:andor', 'series', 'Andor', '', '{}', 0, NULL),
			('series:inert', 'series', 'Inert', '', '{}', 0, NULL)`, nil},
		{`INSERT INTO profile_series_interest (user_id, profile_id, library_id, series_id, favorite, watchlist, next_up_candidate, next_expected_episode_key) VALUES
			(1, 'p1', 1, 'series:bear', true, false, false, NULL),
			(1, 'p1', 2, 'series:bear', false, false, true, $1),
			(1, 'p1', 3, 'series:bear', false, false, true, $2),
			(1, 'p1', 1, 'series:andor', false, true, false, NULL),
			(1, 'p1', 1, 'series:inert', false, false, false, $1),
			(1, 'p1', 1, 'series:gone', true, false, false, NULL),
			(1, 'p2', 1, 'series:andor', true, false, false, NULL)`, []any{nextUp, laterNextUp}},
		{`INSERT INTO notification_preferences (user_id, profile_id, enabled, notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up)
			VALUES (1, 'p1', true, true, false, true, true)`, nil},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	s := &System{Interests: NewInterestRepository(pool), Preferences: NewPreferencesRepository(pool)}

	got, err := s.ListNotifiedSeries(ctx, 1, "p1")
	if err != nil {
		t.Fatalf("ListNotifiedSeries: %v", err)
	}
	if len(got) != 2 || got[0].SeriesID != "series:andor" || got[1].SeriesID != "series:bear" {
		t.Fatalf("series = %+v, want andor then bear", got)
	}
	andor, bear := got[0], got[1]
	if andor.Reasons != (ReasonFlags{Watchlist: true}) || andor.Active.Any() || andor.NextExpectedEpisodeKey != nil {
		t.Errorf("andor = %+v, want an inactive watchlist entry", andor)
	}
	if bear.Reasons != (ReasonFlags{Favorite: true, NextUp: true}) || bear.Active != bear.Reasons {
		t.Errorf("bear = %+v, want active favorite and next up", bear)
	}
	if bear.Title != "The Bear" || bear.Year != 2022 || bear.AddedAt.IsZero() ||
		bear.LatestEpisodeAddedAt == nil || !bear.LatestEpisodeAddedAt.Equal(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)) {
		t.Errorf("bear catalog fields = %+v", bear.SeriesSortFields)
	}
	if andor.Year != 0 || andor.LatestEpisodeAddedAt != nil {
		t.Errorf("andor catalog fields = %+v, want no year or latest episode", andor.SeriesSortFields)
	}
	if bear.NextExpectedEpisodeKey == nil || *bear.NextExpectedEpisodeKey != nextUp {
		t.Errorf("bear waits for %v, want the earliest expected episode %d", bear.NextExpectedEpisodeKey, nextUp)
	}

	// Turning notifications off silences every reason but lists the same series.
	if _, err := pool.Exec(ctx, `UPDATE notification_preferences SET enabled = false WHERE profile_id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListNotifiedSeries(ctx, 1, "p1")
	if err != nil {
		t.Fatalf("ListNotifiedSeries: %v", err)
	}
	if len(got) != 2 || got[0].Active.Any() || got[1].Active.Any() || got[1].NextExpectedEpisodeKey != nil {
		t.Fatalf("disabled = %+v, want both listed and inactive", got)
	}
}
