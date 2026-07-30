package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *PostgresUserStore) SetSubtitlePreference(ctx context.Context, pref userstore.SubtitlePreference) error {
	return setSubtitlePreference(ctx, s.pool, s.userID, pref)
}

func setSubtitlePreference(
	ctx context.Context,
	exec preferenceSettingsExecutor,
	userID int,
	pref userstore.SubtitlePreference,
) error {
	if pref.UpdatedAt == "" {
		pref.UpdatedAt = nowUTC()
	}
	if pref.ShowForcedSubtitles {
		pref.HasShowForcedSubtitles = true
	}
	signatureJSON, err := userstore.MarshalSubtitleTrackSignature(pref.TrackSignature)
	if err != nil {
		return fmt.Errorf("marshaling subtitle track signature for profile %q series %q: %w",
			pref.ProfileID, pref.SeriesID, err)
	}
	_, err = exec.Exec(ctx, `
		INSERT INTO user_subtitle_preferences (
			user_id, profile_id, series_id, subtitle_language, subtitle_track_index,
			external_subtitle_path, subtitle_mode, subtitle_track_signature, show_forced_subtitles, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT(user_id, profile_id, series_id) DO UPDATE SET
			subtitle_language = excluded.subtitle_language,
			subtitle_track_index = excluded.subtitle_track_index,
			external_subtitle_path = excluded.external_subtitle_path,
			subtitle_mode = excluded.subtitle_mode,
			subtitle_track_signature = excluded.subtitle_track_signature,
			show_forced_subtitles = excluded.show_forced_subtitles,
			updated_at = excluded.updated_at`,
		userID,
		pref.ProfileID,
		pref.SeriesID,
		pref.SubtitleLanguage,
		pref.SubtitleTrackIndex,
		pref.ExternalSubtitlePath,
		pref.SubtitleMode,
		signatureJSON,
		pgtype.Bool{Bool: pref.ShowForcedSubtitles, Valid: pref.HasShowForcedSubtitles},
		pref.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("setting subtitle preference for profile %q series %q: %w",
			pref.ProfileID, pref.SeriesID, err)
	}
	return nil
}

func (s *PostgresUserStore) GetSubtitlePreference(ctx context.Context, profileID, seriesID string) (*userstore.SubtitlePreference, error) {
	var pref userstore.SubtitlePreference
	var showForcedSubtitles pgtype.Bool
	var updatedAt time.Time
	var signatureJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT profile_id, series_id, subtitle_language, subtitle_track_index,
		       external_subtitle_path, subtitle_mode, subtitle_track_signature, show_forced_subtitles, updated_at
		FROM user_subtitle_preferences
		WHERE user_id = $1 AND profile_id = $2 AND series_id = $3`,
		s.userID, profileID, seriesID,
	).Scan(
		&pref.ProfileID,
		&pref.SeriesID,
		&pref.SubtitleLanguage,
		&pref.SubtitleTrackIndex,
		&pref.ExternalSubtitlePath,
		&pref.SubtitleMode,
		&signatureJSON,
		&showForcedSubtitles,
		&updatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting subtitle preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	pref.TrackSignature, err = userstore.UnmarshalSubtitleTrackSignature(signatureJSON)
	if err != nil {
		return nil, fmt.Errorf("decoding subtitle track signature for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles = boolValue(showForcedSubtitles)
	pref.UpdatedAt = timeToString(updatedAt)
	return &pref, nil
}

func (s *PostgresUserStore) DeleteSubtitlePreference(ctx context.Context, profileID, seriesID string) error {
	return deleteSubtitlePreference(ctx, s.pool, s.userID, profileID, seriesID)
}

func deleteSubtitlePreference(
	ctx context.Context,
	exec preferenceSettingsExecutor,
	userID int,
	profileID, seriesID string,
) error {
	_, err := exec.Exec(ctx,
		"DELETE FROM user_subtitle_preferences WHERE user_id = $1 AND profile_id = $2 AND series_id = $3",
		userID, profileID, seriesID,
	)
	if err != nil {
		return fmt.Errorf("deleting subtitle preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	return nil
}
