package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *PostgresUserStore) SetSeriesPlaybackPreference(ctx context.Context, pref userstore.SeriesPlaybackPreference) error {
	if pref.UpdatedAt == "" {
		pref.UpdatedAt = nowUTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_series_playback_preferences (
			user_id, profile_id, series_id, resolution, hdr, codec_video, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(user_id, profile_id, series_id) DO UPDATE SET
			resolution = excluded.resolution,
			hdr = excluded.hdr,
			codec_video = excluded.codec_video,
			updated_at = excluded.updated_at`,
		s.userID,
		pref.ProfileID,
		pref.SeriesID,
		pref.Resolution,
		pref.HDR,
		pref.CodecVideo,
		pref.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("setting series playback preference for profile %q series %q: %w",
			pref.ProfileID, pref.SeriesID, err)
	}
	return nil
}

func (s *PostgresUserStore) GetSeriesPlaybackPreference(ctx context.Context, profileID, seriesID string) (*userstore.SeriesPlaybackPreference, error) {
	var pref userstore.SeriesPlaybackPreference
	var updatedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT profile_id, series_id, resolution, hdr, codec_video, updated_at
		FROM user_series_playback_preferences
		WHERE user_id = $1 AND profile_id = $2 AND series_id = $3`,
		s.userID, profileID, seriesID,
	).Scan(
		&pref.ProfileID,
		&pref.SeriesID,
		&pref.Resolution,
		&pref.HDR,
		&pref.CodecVideo,
		&updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting series playback preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	pref.UpdatedAt = timeToString(updatedAt)
	return &pref, nil
}

func (s *PostgresUserStore) DeleteSeriesPlaybackPreference(ctx context.Context, profileID, seriesID string) error {
	_, err := s.pool.Exec(ctx,
		"DELETE FROM user_series_playback_preferences WHERE user_id = $1 AND profile_id = $2 AND series_id = $3",
		s.userID, profileID, seriesID,
	)
	if err != nil {
		return fmt.Errorf("deleting series playback preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	return nil
}
