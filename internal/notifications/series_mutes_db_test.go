package notifications

import (
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFanoutSkipsProfilesThatMutedTheSeries covers the series mute at fanout:
// a profile that muted the series gets no delivery, so no channel sees the
// episode, and its notification cursor stays put. A mute of another series and
// another profile's mute change nothing. The mute leaves the interest row as it
// was, and once the profile unmutes, the next episode notifies it again.
func TestFanoutSkipsProfilesThatMutedTheSeries(t *testing.T) {
	pool := inboxPageDB(t)
	ctx := t.Context()
	c := seedAccessCatalog(t, pool)
	seedFanoutEvent(t, pool, c, []string{"open", "muted", "other-muted"})
	interests := NewInterestRepository(pool)
	s := &System{Interests: interests}
	if err := s.MuteSeries(ctx, 1, "muted", c.series); err != nil {
		t.Fatalf("MuteSeries: %v", err)
	}
	// A mute of an ID that is no series: refused, and fanout is unaffected.
	if err := s.MuteSeries(ctx, 1, "other-muted", c.episode); !errors.Is(err, ErrSeriesNotFound) {
		t.Fatalf("mute an episode = %v, want ErrSeriesNotFound", err)
	}

	worker := newAccessFanoutWorker(pool, scopeByProfile{"open": {}, "muted": {}, "other-muted": {}})
	if _, err := worker.processBatch(ctx); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if got := deliveryRecipients(t, pool, `release_event_id = 'event-1'`); !slices.Equal(got, []string{"open", "other-muted"}) {
		t.Fatalf("event-1 recipients = %v, want open and other-muted", got)
	}
	var favorite, cursorMoved bool
	if err := pool.QueryRow(ctx, `
		SELECT favorite, last_notified_episode_key IS NOT NULL FROM profile_series_interest WHERE profile_id = 'muted'`,
	).Scan(&favorite, &cursorMoved); err != nil {
		t.Fatalf("read muted interest: %v", err)
	}
	if !favorite || cursorMoved {
		t.Fatalf("muted interest favorite = %v, cursor moved = %v; want the row unchanged", favorite, cursorMoved)
	}

	if err := s.UnmuteSeries(ctx, "muted", c.series); err != nil {
		t.Fatalf("UnmuteSeries: %v", err)
	}
	nextKey := EpisodeKey(1, 3)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title)
			VALUES ($1 || '-e3', $1, $1 || '-s1', 1, 3, 'E3')`, []any{c.series}},
		{`INSERT INTO release_events (id, library_id, series_id, episode_id, season_number, episode_number,
			episode_key, available_at, dedupe_key)
			VALUES ('event-2', $1, $2, $2 || '-e3', 1, 3, $3, now(), $4)`,
			[]any{c.library, c.series, nextKey, EpisodeDedupeKey(c.library, c.series, nextKey)}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed next episode: %v", err)
		}
	}
	if _, err := worker.processBatch(ctx); err != nil {
		t.Fatalf("processBatch after unmute: %v", err)
	}
	if got := deliveryRecipients(t, pool, `release_event_id = 'event-2'`); !slices.Equal(got, []string{"muted", "open", "other-muted"}) {
		t.Fatalf("event-2 recipients = %v, want every profile once unmuted", got)
	}
}

// seedSeriesMutes creates the list's tables in a test schema with two series
// and a movie, the Bear as p1's and p2's favorite, and p1's mutes of both
// series, each written twice.
func seedSeriesMutes(t *testing.T) (*pgxpool.Pool, *System) {
	t.Helper()
	pool := schemaDB(t, "series_mutes_")
	ctx := t.Context()
	for _, stmt := range []string{
		`CREATE TABLE profile_series_interest (LIKE public.profile_series_interest INCLUDING ALL)`,
		`CREATE TABLE media_items (LIKE public.media_items INCLUDING ALL)`,
		`CREATE TABLE notification_preferences (LIKE public.notification_preferences INCLUDING ALL)`,
		`CREATE TABLE profile_series_notification_mutes (LIKE public.profile_series_notification_mutes INCLUDING ALL)`,
		`INSERT INTO media_items (content_id, type, title, sort_title, genres, year) VALUES
			('series:bear', 'series', 'The Bear', '', '{}', 2022),
			('series:andor', 'series', 'Andor', '', '{}', 2022),
			('movie:heat', 'movie', 'Heat', '', '{}', 1995)`,
		`INSERT INTO profile_series_interest (user_id, profile_id, library_id, series_id, favorite) VALUES
			(1, 'p1', 1, 'series:bear', true),
			(1, 'p2', 1, 'series:bear', true)`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	s := &System{Interests: NewInterestRepository(pool), Preferences: NewPreferencesRepository(pool)}
	for range 2 {
		for _, id := range []string{"series:bear", "series:andor"} {
			if err := s.MuteSeries(ctx, 1, "p1", id); err != nil {
				t.Fatalf("MuteSeries(%s): %v", id, err)
			}
		}
	}
	return pool, s
}

// TestSeriesMutesList covers the mute store and the notified-series list:
// muting is per profile and idempotent, only series can be muted, a muted
// series is listed muted with its active reasons kept and does not notify,
// and a muted series with no reason is still listed so it can be unmuted.
func TestSeriesMutesList(t *testing.T) {
	_, s := seedSeriesMutes(t)
	ctx := t.Context()
	for _, id := range []string{"movie:heat", "series:gone"} {
		if err := s.MuteSeries(ctx, 1, "p1", id); !errors.Is(err, ErrSeriesNotFound) {
			t.Fatalf("MuteSeries(%s) = %v, want ErrSeriesNotFound", id, err)
		}
	}
	got, err := s.ListNotifiedSeries(ctx, "p1")
	if err != nil {
		t.Fatalf("ListNotifiedSeries: %v", err)
	}
	if len(got) != 2 || got[0].SeriesID != "series:andor" || got[1].SeriesID != "series:bear" {
		t.Fatalf("p1 series = %+v, want andor then bear", got)
	}
	andor, bear := got[0], got[1]
	if !andor.Muted || andor.Reasons.Any() || andor.Notifies() || andor.Title != "Andor" {
		t.Errorf("andor = %+v, want a muted entry with no reason", andor)
	}
	if !bear.Muted || !bear.Active.Favorite || bear.Notifies() {
		t.Errorf("bear = %+v, want muted with its favorite still active", bear)
	}
	other, err := s.ListNotifiedSeries(ctx, "p2")
	if err != nil {
		t.Fatalf("ListNotifiedSeries p2: %v", err)
	}
	if len(other) != 1 || other[0].Muted || !other[0].Notifies() {
		t.Fatalf("p2 series = %+v, want bear notifying: a mute is per profile", other)
	}
}

// TestSeriesMutesUnmuteAndPurge covers lifting a mute, which is idempotent and
// drops a series with no reason from the list, and the removal of a deleted
// profile's mutes.
func TestSeriesMutesUnmuteAndPurge(t *testing.T) {
	pool, s := seedSeriesMutes(t)
	ctx := t.Context()
	for range 2 {
		if err := s.UnmuteSeries(ctx, "p1", "series:andor"); err != nil {
			t.Fatalf("UnmuteSeries: %v", err)
		}
	}
	got, err := s.ListNotifiedSeries(ctx, "p1")
	if err != nil {
		t.Fatalf("ListNotifiedSeries: %v", err)
	}
	if len(got) != 1 || got[0].SeriesID != "series:bear" || !got[0].Muted {
		t.Fatalf("after unmute = %+v, want only bear, still muted", got)
	}
	if err := s.Interests.deleteMutesForProfile(ctx, "p1"); err != nil {
		t.Fatalf("deleteMutesForProfile: %v", err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM profile_series_notification_mutes`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("mutes left = %d (%v), want none", left, err)
	}
}
