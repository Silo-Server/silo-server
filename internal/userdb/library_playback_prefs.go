package userdb

import (
	"database/sql"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// LibraryPlaybackPreference is an alias for the canonical type in userstore.
type LibraryPlaybackPreference = userstore.LibraryPlaybackPreference

// UpsertLibraryPlaybackPreference creates or replaces a library playback preference
// for a given profile and library. Timestamps should be ISO 8601 UTC strings.
func UpsertLibraryPlaybackPreference(db *sql.DB, pref LibraryPlaybackPreference) error {
	return upsertLibraryPlaybackPreference(db, pref)
}

func upsertLibraryPlaybackPreference(exec preferenceSettingsExecutor, pref LibraryPlaybackPreference) error {
	normalizeLibraryPlaybackPreference(&pref)
	_, err := exec.Exec(`
		INSERT INTO library_playback_preferences (
			profile_id, library_id, audio_language, subtitle_language,
			subtitle_mode, show_forced_subtitles, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, library_id) DO UPDATE SET
			audio_language = excluded.audio_language,
			subtitle_language = excluded.subtitle_language,
			subtitle_mode = excluded.subtitle_mode,
			show_forced_subtitles = excluded.show_forced_subtitles,
			updated_at = excluded.updated_at`,
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

// GetLibraryPlaybackPreference retrieves the library playback preference for a profile
// and library. Returns nil (not an error) if no preference exists.
func GetLibraryPlaybackPreference(db *sql.DB, profileID string, libraryID int) (*LibraryPlaybackPreference, error) {
	var pref LibraryPlaybackPreference
	var audioLanguage, subtitleLanguage, subtitleMode sql.NullString
	var showForcedSubtitles sql.NullBool
	err := db.QueryRow(`
		SELECT profile_id, library_id, audio_language, subtitle_language,
		       subtitle_mode, show_forced_subtitles, updated_at
		FROM library_playback_preferences
		WHERE profile_id = ? AND library_id = ?`,
		profileID, libraryID,
	).Scan(
		&pref.ProfileID,
		&pref.LibraryID,
		&audioLanguage,
		&subtitleLanguage,
		&subtitleMode,
		&showForcedSubtitles,
		&pref.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting library playback preference for profile %q library %d: %w",
			profileID, libraryID, err)
	}
	pref.AudioLanguage, pref.HasAudioLanguage = stringValue(audioLanguage)
	pref.SubtitleLanguage, pref.HasSubtitleLanguage = stringValue(subtitleLanguage)
	pref.SubtitleMode, pref.HasSubtitleMode = stringValue(subtitleMode)
	pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles = boolValue(showForcedSubtitles)
	return &pref, nil
}

// ListLibraryPlaybackPreferences retrieves all library playback preferences for a profile.
func ListLibraryPlaybackPreferences(db *sql.DB, profileID string) ([]LibraryPlaybackPreference, error) {
	rows, err := db.Query(`
		SELECT profile_id, library_id, audio_language, subtitle_language,
		       subtitle_mode, show_forced_subtitles, updated_at
		FROM library_playback_preferences
		WHERE profile_id = ?
		ORDER BY library_id`,
		profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing library playback preferences for profile %q: %w", profileID, err)
	}
	defer rows.Close()

	prefs := make([]LibraryPlaybackPreference, 0)
	for rows.Next() {
		var pref LibraryPlaybackPreference
		var audioLanguage, subtitleLanguage, subtitleMode sql.NullString
		var showForcedSubtitles sql.NullBool
		if err := rows.Scan(
			&pref.ProfileID,
			&pref.LibraryID,
			&audioLanguage,
			&subtitleLanguage,
			&subtitleMode,
			&showForcedSubtitles,
			&pref.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("listing library playback preferences for profile %q: %w", profileID, err)
		}
		pref.AudioLanguage, pref.HasAudioLanguage = stringValue(audioLanguage)
		pref.SubtitleLanguage, pref.HasSubtitleLanguage = stringValue(subtitleLanguage)
		pref.SubtitleMode, pref.HasSubtitleMode = stringValue(subtitleMode)
		pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles = boolValue(showForcedSubtitles)
		prefs = append(prefs, pref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing library playback preferences for profile %q: %w", profileID, err)
	}
	return prefs, nil
}

// DeleteLibraryPlaybackPreference removes the library playback preference for a profile and library.
func DeleteLibraryPlaybackPreference(db *sql.DB, profileID string, libraryID int) error {
	return deleteLibraryPlaybackPreference(db, profileID, libraryID)
}

func deleteLibraryPlaybackPreference(exec preferenceSettingsExecutor, profileID string, libraryID int) error {
	_, err := exec.Exec(
		"DELETE FROM library_playback_preferences WHERE profile_id = ? AND library_id = ?",
		profileID, libraryID,
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

func stringValue(value sql.NullString) (string, bool) {
	if !value.Valid {
		return "", false
	}
	return value.String, true
}

func boolValue(value sql.NullBool) (bool, bool) {
	if !value.Valid {
		return false, false
	}
	return value.Bool, true
}

func normalizeLibraryPlaybackPreference(pref *LibraryPlaybackPreference) {
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
