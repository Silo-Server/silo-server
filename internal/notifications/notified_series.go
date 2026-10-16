package notifications

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// NotifiedSeries is one series a profile has a reason to hear about: the
// interest rows of every library the series is in, merged, with the profile's
// preferences applied.
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
// profile, ordered by SortKey then SeriesID. The interest index is the same
// one fanout reads, so the list names exactly the series a new episode would
// be evaluated for. It does not judge catalog access; callers filter the
// series the profile may not open, as fanout does per delivery.
func (s *System) ListNotifiedSeries(ctx context.Context, profileID string) ([]NotifiedSeries, error) {
	prefs, err := s.Preferences.Get(ctx, profileID)
	if err != nil {
		return nil, err
	}
	interests, err := s.Interests.listMergedForProfile(ctx, profileID)
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
}

// listMergedForProfile loads the profile's active interest rows, one per
// series: a series in several libraries has a row in each, and any row's
// reason counts. Next Up waits for the earliest expected episode among the
// rows that carry it. Series the catalog no longer has are skipped.
func (r *InterestRepository) listMergedForProfile(ctx context.Context, profileID string) ([]mergedInterest, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT psi.series_id,
		       bool_or(psi.favorite), bool_or(psi.watchlist),
		       bool_or(psi.continue_watching), bool_or(psi.next_up_candidate),
		       min(psi.next_expected_episode_key) FILTER (WHERE psi.next_up_candidate),
		       lower(COALESCE(NULLIF(mi.sort_title, ''), mi.title)), mi.title,
		       COALESCE(mi.year, 0), mi.created_at, mi.latest_episode_added_at
		FROM profile_series_interest psi
		JOIN media_items mi ON mi.content_id = psi.series_id
		WHERE psi.profile_id = $1
		  AND (psi.favorite OR psi.watchlist OR psi.continue_watching OR psi.next_up_candidate)
		GROUP BY psi.series_id, mi.content_id`,
		profileID)
	if err != nil {
		return nil, fmt.Errorf("list profile series interest: %w", err)
	}
	defer rows.Close()
	out := make([]mergedInterest, 0, 32)
	for rows.Next() {
		row := mergedInterest{SeriesInterest: SeriesInterest{ProfileID: profileID}}
		if err := rows.Scan(&row.SeriesID, &row.Favorite, &row.Watchlist,
			&row.ContinueWatching, &row.NextUpCandidate, &row.NextExpectedEpisodeKey,
			&row.SortKey, &row.Title, &row.Year, &row.AddedAt, &row.LatestEpisodeAddedAt); err != nil {
			return nil, fmt.Errorf("scan profile series interest: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
