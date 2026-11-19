package notifications

import (
	"context"
	"errors"
	"fmt"
)

// ErrSeriesNotFound reports a mute of an ID the catalog has no series for.
var ErrSeriesNotFound = errors.New("series not found")

// A muted series sends its profile no new-episode notification, whatever
// interest reasons it has, until the profile unmutes it. The mute lives in
// its own table rather than on the interest rows: those are recomputed per
// library and pruned when the reasons go, and a mute must outlast both.
// Fanout leaves muted profiles out where it chooses recipients
// (ListActiveBySeries), so no delivery exists for any channel to send.

// MuteSeries mutes the series for the profile. Muting a series already muted
// changes nothing. It returns ErrSeriesNotFound when the catalog has no
// series with that ID; catalog access is the caller's check.
func (s *System) MuteSeries(ctx context.Context, userID int, profileID, seriesID string) error {
	return s.Interests.muteSeries(ctx, userID, profileID, seriesID)
}

// UnmuteSeries lifts the profile's mute of the series. Unmuting a series that
// is not muted changes nothing. Fanout reads the mute when it evaluates a
// release, so an episode it evaluated while the series was muted stays
// unannounced, and one still waiting for fanout at unmute is announced.
func (s *System) UnmuteSeries(ctx context.Context, userID int, profileID, seriesID string) error {
	return s.Interests.unmuteSeries(ctx, userID, profileID, seriesID)
}

// SeriesMuteOutcome is what SetSeriesMuted did to one series.
type SeriesMuteOutcome string

const (
	// SeriesMuteApplied: the series was muted, or unmuted, by this call.
	SeriesMuteApplied SeriesMuteOutcome = "applied"
	// SeriesMuteUnchanged: the series was already in the requested state.
	SeriesMuteUnchanged SeriesMuteOutcome = "unchanged"
	// SeriesMuteNotFound: a mute named an ID the catalog has no series for.
	SeriesMuteNotFound SeriesMuteOutcome = "not_found"
)

// SetSeriesMuted mutes or unmutes many series for the profile in one
// statement, as MuteSeries and UnmuteSeries do one, and answers an outcome
// for each distinct ID. Unmuting never answers SeriesMuteNotFound, so a
// profile can lift a mute on a series the catalog has since dropped.
func (s *System) SetSeriesMuted(ctx context.Context, userID int, profileID string, seriesIDs []string, muted bool) (map[string]SeriesMuteOutcome, error) {
	if muted {
		return s.Interests.muteManySeries(ctx, userID, profileID, seriesIDs)
	}
	return s.Interests.unmuteManySeries(ctx, userID, profileID, seriesIDs)
}

func (r *InterestRepository) muteManySeries(ctx context.Context, userID int, profileID string, seriesIDs []string) (map[string]SeriesMuteOutcome, error) {
	out := make(map[string]SeriesMuteOutcome, len(seriesIDs))
	for _, id := range seriesIDs {
		out[id] = SeriesMuteNotFound
	}
	rows, err := r.pool.Query(ctx, `
		WITH series AS (
			SELECT content_id FROM media_items WHERE content_id = ANY($3::text[]) AND type = 'series'
		), muted AS (
			INSERT INTO profile_series_notification_mutes (user_id, profile_id, series_id)
			SELECT $1, $2, content_id FROM series
			ON CONFLICT (user_id, profile_id, series_id) DO NOTHING
			RETURNING series_id
		)
		SELECT s.content_id, m.series_id IS NOT NULL
		FROM series s LEFT JOIN muted m ON m.series_id = s.content_id`,
		userID, profileID, seriesIDs)
	if err != nil {
		return nil, fmt.Errorf("mute series: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var inserted bool
		if err := rows.Scan(&id, &inserted); err != nil {
			return nil, fmt.Errorf("mute series: %w", err)
		}
		out[id] = SeriesMuteUnchanged
		if inserted {
			out[id] = SeriesMuteApplied
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mute series: %w", err)
	}
	return out, nil
}

func (r *InterestRepository) unmuteManySeries(ctx context.Context, userID int, profileID string, seriesIDs []string) (map[string]SeriesMuteOutcome, error) {
	out := make(map[string]SeriesMuteOutcome, len(seriesIDs))
	for _, id := range seriesIDs {
		out[id] = SeriesMuteUnchanged
	}
	rows, err := r.pool.Query(ctx, `
		DELETE FROM profile_series_notification_mutes
		WHERE user_id = $1 AND profile_id = $2 AND series_id = ANY($3::text[])
		RETURNING series_id`,
		userID, profileID, seriesIDs)
	if err != nil {
		return nil, fmt.Errorf("unmute series: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("unmute series: %w", err)
		}
		out[id] = SeriesMuteApplied
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unmute series: %w", err)
	}
	return out, nil
}

func (r *InterestRepository) muteSeries(ctx context.Context, userID int, profileID, seriesID string) error {
	var found bool
	err := r.pool.QueryRow(ctx, `
		WITH series AS (
			SELECT content_id FROM media_items WHERE content_id = $3 AND type = 'series'
		), muted AS (
			INSERT INTO profile_series_notification_mutes (user_id, profile_id, series_id)
			SELECT $1, $2, content_id FROM series
			ON CONFLICT (user_id, profile_id, series_id) DO NOTHING
		)
		SELECT EXISTS (SELECT 1 FROM series)`,
		userID, profileID, seriesID).Scan(&found)
	if err != nil {
		return fmt.Errorf("mute series: %w", err)
	}
	if !found {
		return ErrSeriesNotFound
	}
	return nil
}

func (r *InterestRepository) unmuteSeries(ctx context.Context, userID int, profileID, seriesID string) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM profile_series_notification_mutes WHERE user_id = $1 AND profile_id = $2 AND series_id = $3`,
		userID, profileID, seriesID); err != nil {
		return fmt.Errorf("unmute series: %w", err)
	}
	return nil
}

// deleteMutesForProfile removes a deleted profile's mutes.
func (r *InterestRepository) deleteMutesForProfile(ctx context.Context, userID int, profileID string) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM profile_series_notification_mutes WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID); err != nil {
		return fmt.Errorf("delete series mutes: %w", err)
	}
	return nil
}
