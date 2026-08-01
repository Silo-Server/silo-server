package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *PostgresUserStore) UpsertLibraryPlaybackPreference(ctx context.Context, pref userstore.LibraryPlaybackPreference) error {
	return upsertLibraryPlaybackPreference(ctx, s.pool, s.userID, pref)
}

func upsertLibraryPlaybackPreference(
	ctx context.Context,
	exec preferenceSettingsExecutor,
	userID int,
	pref userstore.LibraryPlaybackPreference,
) error {
	if pref.UpdatedAt == "" {
		pref.UpdatedAt = nowUTC()
	}
	normalizeLibraryPlaybackPreference(&pref)
	_, err := exec.Exec(ctx, `
		INSERT INTO user_library_playback_preferences (
			user_id, profile_id, library_id, audio_language, subtitle_language,
			subtitle_mode, show_forced_subtitles, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT(user_id, profile_id, library_id) DO UPDATE SET
			audio_language = excluded.audio_language,
			subtitle_language = excluded.subtitle_language,
			subtitle_mode = excluded.subtitle_mode,
			show_forced_subtitles = excluded.show_forced_subtitles,
			updated_at = excluded.updated_at`,
		userID,
		pref.ProfileID,
		pref.LibraryID,
		libraryPlaybackDBValue(pref.AudioLanguage, pref.HasAudioLanguage),
		libraryPlaybackDBValue(pref.SubtitleLanguage, pref.HasSubtitleLanguage),
		libraryPlaybackDBValue(pref.SubtitleMode, pref.HasSubtitleMode),
		libraryPlaybackBoolDBValue(pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles),
		pref.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upserting library playback preference for profile %q library %d: %w",
			pref.ProfileID, pref.LibraryID, err)
	}
	return nil
}

func (s *PostgresUserStore) GetLibraryPlaybackPreference(ctx context.Context, profileID string, libraryID int) (*userstore.LibraryPlaybackPreference, error) {
	var pref userstore.LibraryPlaybackPreference
	var audioLanguage, subtitleLanguage, subtitleMode pgtype.Text
	var showForcedSubtitles pgtype.Bool
	var updatedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT profile_id, library_id, audio_language, subtitle_language,
		       subtitle_mode, show_forced_subtitles, updated_at
		FROM user_library_playback_preferences
		WHERE user_id = $1 AND profile_id = $2 AND library_id = $3`,
		s.userID, profileID, libraryID,
	).Scan(
		&pref.ProfileID,
		&pref.LibraryID,
		&audioLanguage,
		&subtitleLanguage,
		&subtitleMode,
		&showForcedSubtitles,
		&updatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting library playback preference for profile %q library %d: %w",
			profileID, libraryID, err)
	}
	pref.AudioLanguage, pref.HasAudioLanguage = textValue(audioLanguage)
	pref.SubtitleLanguage, pref.HasSubtitleLanguage = textValue(subtitleLanguage)
	pref.SubtitleMode, pref.HasSubtitleMode = textValue(subtitleMode)
	pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles = boolValue(showForcedSubtitles)
	pref.UpdatedAt = timeToString(updatedAt)
	return &pref, nil
}

func (s *PostgresUserStore) ListLibraryPlaybackPreferences(ctx context.Context, profileID string) ([]userstore.LibraryPlaybackPreference, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT profile_id, library_id, audio_language, subtitle_language,
		       subtitle_mode, show_forced_subtitles, updated_at
		FROM user_library_playback_preferences
		WHERE user_id = $1 AND profile_id = $2
		ORDER BY library_id`,
		s.userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing library playback preferences for profile %q: %w", profileID, err)
	}
	defer rows.Close()

	prefs := make([]userstore.LibraryPlaybackPreference, 0)
	for rows.Next() {
		var pref userstore.LibraryPlaybackPreference
		var audioLanguage, subtitleLanguage, subtitleMode pgtype.Text
		var showForcedSubtitles pgtype.Bool
		var updatedAt time.Time
		if err := rows.Scan(
			&pref.ProfileID,
			&pref.LibraryID,
			&audioLanguage,
			&subtitleLanguage,
			&subtitleMode,
			&showForcedSubtitles,
			&updatedAt,
		); err != nil {
			return nil, fmt.Errorf("listing library playback preferences for profile %q: %w", profileID, err)
		}
		pref.AudioLanguage, pref.HasAudioLanguage = textValue(audioLanguage)
		pref.SubtitleLanguage, pref.HasSubtitleLanguage = textValue(subtitleLanguage)
		pref.SubtitleMode, pref.HasSubtitleMode = textValue(subtitleMode)
		pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles = boolValue(showForcedSubtitles)
		pref.UpdatedAt = timeToString(updatedAt)
		prefs = append(prefs, pref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing library playback preferences for profile %q: %w", profileID, err)
	}
	return prefs, nil
}

func (s *PostgresUserStore) DeleteLibraryPlaybackPreference(ctx context.Context, profileID string, libraryID int) error {
	return deleteLibraryPlaybackPreference(ctx, s.pool, s.userID, profileID, libraryID)
}

func deleteLibraryPlaybackPreference(
	ctx context.Context,
	exec preferenceSettingsExecutor,
	userID int,
	profileID string,
	libraryID int,
) error {
	_, err := exec.Exec(ctx,
		"DELETE FROM user_library_playback_preferences WHERE user_id = $1 AND profile_id = $2 AND library_id = $3",
		userID, profileID, libraryID,
	)
	if err != nil {
		return fmt.Errorf("deleting library playback preference for profile %q library %d: %w",
			profileID, libraryID, err)
	}
	return nil
}

func libraryPlaybackDBValue(value string, hasValue bool) any {
	if !hasValue {
		return nil
	}
	return value
}

func libraryPlaybackBoolDBValue(value bool, hasValue bool) any {
	if !hasValue {
		return nil
	}
	return value
}

func textValue(value pgtype.Text) (string, bool) {
	if !value.Valid {
		return "", false
	}
	return value.String, true
}

func boolValue(value pgtype.Bool) (bool, bool) {
	if !value.Valid {
		return false, false
	}
	return value.Bool, true
}

func normalizeLibraryPlaybackPreference(pref *userstore.LibraryPlaybackPreference) {
	if pref.AudioLanguage != "" {
		pref.HasAudioLanguage = true
	}
	if pref.SubtitleLanguage != "" {
		pref.HasSubtitleLanguage = true
	}
	if pref.SubtitleMode != "" {
		pref.HasSubtitleMode = true
	}
	if pref.ShowForcedSubtitles {
		pref.HasShowForcedSubtitles = true
	}
}
