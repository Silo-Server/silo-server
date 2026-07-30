package userdb

import (
	"database/sql"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SubtitlePreference is an alias for the canonical type in userstore.
type SubtitlePreference = userstore.SubtitlePreference

// SetSubtitlePreference creates or replaces a subtitle preference for a
// given profile and series. Timestamps should be ISO 8601 UTC strings.
func SetSubtitlePreference(db *sql.DB, pref SubtitlePreference) error {
	return setSubtitlePreference(db, pref)
}

func setSubtitlePreference(exec preferenceSettingsExecutor, pref SubtitlePreference) error {
	if pref.ShowForcedSubtitles {
		pref.HasShowForcedSubtitles = true
	}
	signatureJSON, err := userstore.MarshalSubtitleTrackSignature(pref.TrackSignature)
	if err != nil {
		return fmt.Errorf("marshaling subtitle track signature for profile %q series %q: %w",
			pref.ProfileID, pref.SeriesID, err)
	}
	_, err = exec.Exec(`
		INSERT INTO subtitle_preferences (
			profile_id, series_id, subtitle_language, subtitle_track_index,
			external_subtitle_path, subtitle_mode, subtitle_track_signature, show_forced_subtitles, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, series_id) DO UPDATE SET
			subtitle_language = excluded.subtitle_language,
			subtitle_track_index = excluded.subtitle_track_index,
			external_subtitle_path = excluded.external_subtitle_path,
			subtitle_mode = excluded.subtitle_mode,
			subtitle_track_signature = excluded.subtitle_track_signature,
			show_forced_subtitles = excluded.show_forced_subtitles,
			updated_at = excluded.updated_at`,
		pref.ProfileID,
		pref.SeriesID,
		pref.SubtitleLanguage,
		pref.SubtitleTrackIndex,
		pref.ExternalSubtitlePath,
		pref.SubtitleMode,
		string(signatureJSON),
		libraryPlaybackBoolDBValue(pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles),
		pref.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("setting subtitle preference for profile %q series %q: %w",
			pref.ProfileID, pref.SeriesID, err)
	}
	return nil
}

// GetSubtitlePreference retrieves the subtitle preference for a profile
// and series. Returns nil (not an error) if no preference exists.
func GetSubtitlePreference(db *sql.DB, profileID, seriesID string) (*SubtitlePreference, error) {
	var pref SubtitlePreference
	var showForcedSubtitles sql.NullBool
	var signatureJSON string
	err := db.QueryRow(`
		SELECT profile_id, series_id, subtitle_language, subtitle_track_index,
		       external_subtitle_path, subtitle_mode, subtitle_track_signature, show_forced_subtitles, updated_at
		FROM subtitle_preferences
		WHERE profile_id = ? AND series_id = ?`,
		profileID, seriesID,
	).Scan(
		&pref.ProfileID,
		&pref.SeriesID,
		&pref.SubtitleLanguage,
		&pref.SubtitleTrackIndex,
		&pref.ExternalSubtitlePath,
		&pref.SubtitleMode,
		&signatureJSON,
		&showForcedSubtitles,
		&pref.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting subtitle preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	pref.TrackSignature, err = userstore.UnmarshalSubtitleTrackSignature([]byte(signatureJSON))
	if err != nil {
		return nil, fmt.Errorf("decoding subtitle track signature for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles = boolValue(showForcedSubtitles)
	return &pref, nil
}

// DeleteSubtitlePreference removes the subtitle preference for a profile
// and series. It is not an error if the preference does not exist.
func DeleteSubtitlePreference(db *sql.DB, profileID, seriesID string) error {
	return deleteSubtitlePreference(db, profileID, seriesID)
}

func deleteSubtitlePreference(exec preferenceSettingsExecutor, profileID, seriesID string) error {
	_, err := exec.Exec(
		"DELETE FROM subtitle_preferences WHERE profile_id = ? AND series_id = ?",
		profileID, seriesID,
	)
	if err != nil {
		return fmt.Errorf("deleting subtitle preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	return nil
}
