package notifications

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// NotifiedSeries is one series a profile has a reason to hear about, or
// muted: the interest rows of every library the series is in, merged, with the
// profile's preferences applied.
type NotifiedSeries struct {
	SeriesID string
	SeriesSortFields
	// Reasons are the profile's relationships to the series.
	Reasons ReasonFlags
	// Active are the Reasons the profile's preferences let notify. A series
	// with none notifies nobody until a preference changes.
	Active ReasonFlags
	// NextExpectedEpisodeKey is the episode an active Next Up reason waits
	// for; nil without one.
	NextExpectedEpisodeKey *int
	// Muted is set when the profile muted the series: it notifies nobody
	// whatever its Active reasons, which still say what would notify once
	// the profile unmutes.
	Muted bool
}

// Notifies reports whether a new episode of the series can notify the
// profile: it has an active reason and is not muted.
func (s NotifiedSeries) Notifies() bool {
	return !s.Muted && s.Active.Any()
}

// SeriesSortFields are the catalog fields the notified-series list is
// searched and sorted by.
type SeriesSortFields struct {
	// SortKey is the default order: the case-folded sort title, else the title.
	SortKey string
	Title   string
	// Year is 0 when the catalog has none.
	Year    int
	AddedAt time.Time
	// LatestEpisodeAddedAt is when the series last gained an episode; nil
	// before its first.
	LatestEpisodeAddedAt *time.Time
}

// ListNotifiedSeries answers every series with an interest reason for the
// profile on userID's account, and every series it muted, ordered by SortKey then SeriesID. The
// interest index and mutes are the ones fanout reads, so the list names
// exactly the series a new episode would be evaluated for. It does not judge
// catalog access; callers filter the series the profile may not open, as
// fanout does per delivery.
func (s *System) ListNotifiedSeries(ctx context.Context, userID int, profileID string) ([]NotifiedSeries, error) {
	prefs, err := s.Preferences.Get(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	interests, err := s.Interests.listMergedForProfile(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	out := make([]NotifiedSeries, 0, len(interests))
	for _, row := range interests {
		active := ActiveReasons(row.SeriesInterest, prefs)
		series := NotifiedSeries{
			SeriesID:         row.SeriesID,
			SeriesSortFields: row.SeriesSortFields,
			Reasons: ReasonFlags{
				Favorite:         row.Favorite,
				Watchlist:        row.Watchlist,
				ContinueWatching: row.ContinueWatching,
				NextUp:           row.NextUpCandidate,
			},
			Active: active,
			Muted:  row.Muted,
		}
		if active.NextUp {
			series.NextExpectedEpisodeKey = row.NextExpectedEpisodeKey
		}
		out = append(out, series)
	}
	slices.SortFunc(out, func(a, b NotifiedSeries) int {
		return cmp.Or(strings.Compare(a.SortKey, b.SortKey), strings.Compare(a.SeriesID, b.SeriesID))
	})
	return out, nil
}

// mergedInterest is one series' interest rows folded across libraries.
type mergedInterest struct {
	SeriesInterest
	SeriesSortFields
	Muted bool
}

// listMergedForProfile loads the profile's active interest rows, one per
// series, and its muted series: a series in several libraries has a row in
// each, and any row's reason counts. Next Up waits for the earliest expected
// episode among the rows that carry it. A muted series with no reason has a
// row with none. Series the catalog no longer has are skipped. Rows are read
// for the profile on userID's account only, since profile IDs such as the
// legacy "default" repeat across accounts.
func (r *InterestRepository) listMergedForProfile(ctx context.Context, userID int, profileID string) ([]mergedInterest, error) {
	rows, err := r.pool.Query(ctx, `
		WITH interest AS (
			SELECT series_id,
			       bool_or(favorite) AS favorite, bool_or(watchlist) AS watchlist,
			       bool_or(continue_watching) AS continue_watching,
			       bool_or(next_up_candidate) AS next_up_candidate,
			       min(next_expected_episode_key) FILTER (WHERE next_up_candidate) AS next_expected_episode_key
			FROM profile_series_interest
			WHERE user_id = $2 AND profile_id = $1
			  AND (favorite OR watchlist OR continue_watching OR next_up_candidate)
			GROUP BY series_id
		), muted AS (
			SELECT series_id FROM profile_series_notification_mutes WHERE user_id = $2 AND profile_id = $1
		)
		SELECT mi.content_id,
		       COALESCE(i.favorite, false), COALESCE(i.watchlist, false),
		       COALESCE(i.continue_watching, false), COALESCE(i.next_up_candidate, false),
		       i.next_expected_episode_key, m.series_id IS NOT NULL,
		       lower(COALESCE(NULLIF(mi.sort_title, ''), mi.title)), mi.title,
		       COALESCE(mi.year, 0), mi.created_at, mi.latest_episode_added_at
		FROM interest i
		FULL JOIN muted m ON m.series_id = i.series_id
		JOIN media_items mi ON mi.content_id = COALESCE(i.series_id, m.series_id)`,
		profileID, userID)
	if err != nil {
		return nil, fmt.Errorf("list profile series interest: %w", err)
	}
	defer rows.Close()
	out := make([]mergedInterest, 0, 32)
	for rows.Next() {
		row := mergedInterest{SeriesInterest: SeriesInterest{UserID: userID, ProfileID: profileID}}
		if err := rows.Scan(&row.SeriesID, &row.Favorite, &row.Watchlist,
			&row.ContinueWatching, &row.NextUpCandidate, &row.NextExpectedEpisodeKey, &row.Muted,
			&row.SortKey, &row.Title, &row.Year, &row.AddedAt, &row.LatestEpisodeAddedAt); err != nil {
			return nil, fmt.Errorf("scan profile series interest: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
