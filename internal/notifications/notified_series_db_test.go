package notifications

import (
	"testing"
	"time"
)

// TestListNotifiedSeriesMergesLibrariesAndAppliesPreferences covers the
// notified-series list over the fanout index: a series in two libraries is one
// entry waiting on the earliest cursor, a row without a cursor and a series
// the catalog no longer has are left out, a series that is no Next Up
// candidate stays listed but inactive, a series notifies only while the first
// listed episode at or after its cursor has no present file, and turning Next
// Up off keeps every series listed but silent.
func TestListNotifiedSeriesMergesLibrariesAndAppliesPreferences(t *testing.T) {
	pool := schemaDB(t, "notified_series_")
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE profile_series_interest (LIKE public.profile_series_interest INCLUDING ALL);
		CREATE TABLE media_items (LIKE public.media_items INCLUDING ALL);
		CREATE TABLE episodes (LIKE public.episodes INCLUDING ALL);
		CREATE TABLE media_files (LIKE public.media_files INCLUDING ALL);
		CREATE TABLE notification_preferences (LIKE public.notification_preferences INCLUDING ALL);
		CREATE TABLE profile_series_notification_mutes (LIKE public.profile_series_notification_mutes INCLUDING ALL)`); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	bearCursor, laterCursor, andorCursor := EpisodeKey(2, 3), EpisodeKey(2, 5), EpisodeKey(1, 2)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id, type, title, sort_title, genres, year, latest_episode_added_at) VALUES
			('series:bear', 'series', 'The Bear', 'Bear', '{}', 2022, '2026-03-04T05:06:07Z'),
			('series:andor', 'series', 'Andor', '', '{}', 0, NULL),
			('series:dropped', 'series', 'Dropped', '', '{}', 0, NULL),
			('series:inert', 'series', 'Inert', '', '{}', 0, NULL)`, nil},
		// The Bear's S02E03 lost its file, so the episode the profile waits
		// for has not arrived, although S02E04 after it has; Andor's S01E02
		// is there to watch.
		{`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES
			('bear-s2e2', 'series:bear', 2, 2, 'E2'), ('bear-s2e3', 'series:bear', 2, 3, 'E3'),
			('bear-s2e4', 'series:bear', 2, 4, 'E4'),
			('andor-s1e2', 'series:andor', 1, 2, 'E2')`, nil},
		{`INSERT INTO media_files (media_folder_id, file_path, episode_id, missing_since) VALUES
			(1, '/bear/e2.mkv', 'bear-s2e2', NULL), (1, '/bear/e3.mkv', 'bear-s2e3', now()),
			(1, '/bear/e4.mkv', 'bear-s2e4', NULL),
			(1, '/andor/e2.mkv', 'andor-s1e2', NULL)`, nil},
		{`INSERT INTO profile_series_interest (user_id, profile_id, library_id, series_id, next_up_candidate, next_expected_episode_key) VALUES
			(1, 'p1', 1, 'series:bear', false, NULL),
			(1, 'p1', 2, 'series:bear', true, $1),
			(1, 'p1', 3, 'series:bear', true, $2),
			(1, 'p1', 1, 'series:andor', true, $3),
			(1, 'p1', 1, 'series:dropped', false, $1),
			(1, 'p1', 1, 'series:inert', false, NULL),
			(1, 'p1', 1, 'series:gone', true, $1),
			(1, 'p2', 1, 'series:inert', true, $1)`, []any{bearCursor, laterCursor, andorCursor}},
		{`INSERT INTO notification_preferences (user_id, profile_id, enabled, notify_next_up) VALUES (1, 'p1', true, true)`, nil},
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
	if len(got) != 3 || got[0].SeriesID != "series:andor" || got[1].SeriesID != "series:bear" || got[2].SeriesID != "series:dropped" {
		t.Fatalf("series = %+v, want andor, bear, dropped", got)
	}
	andor, bear, dropped := got[0], got[1], got[2]
	on := ReasonFlags{NextUp: true}
	if bear.Reasons != on || bear.Active != on || !bear.CaughtUp || !bear.Notifies() {
		t.Errorf("bear = %+v, want an active Next Up that notifies", bear)
	}
	if bear.NextExpectedEpisodeKey == nil || *bear.NextExpectedEpisodeKey != bearCursor {
		t.Errorf("bear waits for %v, want the earliest cursor %d", bear.NextExpectedEpisodeKey, bearCursor)
	}
	if andor.Active != on || andor.CaughtUp || andor.Notifies() {
		t.Errorf("andor = %+v, want an active Next Up that does not notify while S01E02 is available", andor)
	}
	if dropped.Reasons != (ReasonFlags{}) || dropped.Active != (ReasonFlags{}) || dropped.Notifies() || dropped.NextExpectedEpisodeKey != nil {
		t.Errorf("dropped = %+v, want listed without a reason", dropped)
	}
	if bear.Title != "The Bear" || bear.Year != 2022 || bear.AddedAt.IsZero() ||
		bear.LatestEpisodeAddedAt == nil || !bear.LatestEpisodeAddedAt.Equal(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)) {
		t.Errorf("bear catalog fields = %+v", bear.SeriesSortFields)
	}
	if andor.Year != 0 || andor.LatestEpisodeAddedAt != nil {
		t.Errorf("andor catalog fields = %+v, want no year or latest episode", andor.SeriesSortFields)
	}

	// Turning Next Up off silences every series but lists the same ones.
	if _, err := pool.Exec(ctx, `UPDATE notification_preferences SET notify_next_up = false WHERE user_id = 1 AND profile_id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListNotifiedSeries(ctx, 1, "p1")
	if err != nil {
		t.Fatalf("ListNotifiedSeries: %v", err)
	}
	if len(got) != 3 || got[1].Active.NextUp || got[1].Notifies() || got[1].NextExpectedEpisodeKey != nil {
		t.Fatalf("next up off = %+v, want all three listed and bear silent", got)
	}
}
