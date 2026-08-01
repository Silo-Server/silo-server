package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *PostgresUserStore) SetAudioPreference(ctx context.Context, pref userstore.AudioPreference) error {
	return setAudioPreference(ctx, s.pool, s.userID, pref)
}

func setAudioPreference(
	ctx context.Context,
	exec preferenceSettingsExecutor,
	userID int,
	pref userstore.AudioPreference,
) error {
	if pref.UpdatedAt == "" {
		pref.UpdatedAt = nowUTC()
	}
	signatureJSON, err := userstore.MarshalAudioTrackSignature(pref.TrackSignature)
	if err != nil {
		return fmt.Errorf("marshaling audio track signature for profile %q series %q: %w",
			pref.ProfileID, pref.SeriesID, err)
	}
	_, err = exec.Exec(ctx, `
		INSERT INTO user_audio_preferences (
			user_id, profile_id, series_id, audio_track_index,
			audio_language, audio_track_signature, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(user_id, profile_id, series_id) DO UPDATE SET
			audio_track_index = excluded.audio_track_index,
			audio_language = excluded.audio_language,
			audio_track_signature = excluded.audio_track_signature,
			updated_at = excluded.updated_at`,
		userID,
		pref.ProfileID,
		pref.SeriesID,
		pref.AudioTrackIndex,
		pref.AudioLanguage,
		signatureJSON,
		pref.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("setting audio preference for profile %q series %q: %w",
			pref.ProfileID, pref.SeriesID, err)
	}
	return nil
}

func (s *PostgresUserStore) GetAudioPreference(ctx context.Context, profileID, seriesID string) (*userstore.AudioPreference, error) {
	var pref userstore.AudioPreference
	var updatedAt time.Time
	var signatureJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT profile_id, series_id, audio_track_index,
		       audio_language, audio_track_signature, updated_at
		FROM user_audio_preferences
		WHERE user_id = $1 AND profile_id = $2 AND series_id = $3`,
		s.userID, profileID, seriesID,
	).Scan(
		&pref.ProfileID,
		&pref.SeriesID,
		&pref.AudioTrackIndex,
		&pref.AudioLanguage,
		&signatureJSON,
		&updatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting audio preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	pref.TrackSignature, err = userstore.UnmarshalAudioTrackSignature(signatureJSON)
	if err != nil {
		return nil, fmt.Errorf("decoding audio track signature for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	pref.UpdatedAt = timeToString(updatedAt)
	return &pref, nil
}

func (s *PostgresUserStore) DeleteAudioPreference(ctx context.Context, profileID, seriesID string) error {
	return deleteAudioPreference(ctx, s.pool, s.userID, profileID, seriesID)
}

func deleteAudioPreference(
	ctx context.Context,
	exec preferenceSettingsExecutor,
	userID int,
	profileID, seriesID string,
) error {
	_, err := exec.Exec(ctx,
		"DELETE FROM user_audio_preferences WHERE user_id = $1 AND profile_id = $2 AND series_id = $3",
		userID, profileID, seriesID,
	)
	if err != nil {
		return fmt.Errorf("deleting audio preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	return nil
}
