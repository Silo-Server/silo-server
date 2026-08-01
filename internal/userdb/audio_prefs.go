package userdb

import (
	"database/sql"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// AudioPreference is an alias for the canonical type in userstore.
type AudioPreference = userstore.AudioPreference

// SetAudioPreference creates or replaces an audio preference for a
// given profile and series. Timestamps should be ISO 8601 UTC strings.
func SetAudioPreference(db *sql.DB, pref AudioPreference) error {
	return setAudioPreference(db, pref)
}

func setAudioPreference(exec preferenceSettingsExecutor, pref AudioPreference) error {
	signatureJSON, err := userstore.MarshalAudioTrackSignature(pref.TrackSignature)
	if err != nil {
		return fmt.Errorf("marshaling audio track signature for profile %q series %q: %w",
			pref.ProfileID, pref.SeriesID, err)
	}
	_, err = exec.Exec(`
		INSERT INTO audio_preferences (
			profile_id, series_id, audio_track_index,
			audio_language, audio_track_signature, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, series_id) DO UPDATE SET
			audio_track_index = excluded.audio_track_index,
			audio_language = excluded.audio_language,
			audio_track_signature = excluded.audio_track_signature,
			updated_at = excluded.updated_at`,
		pref.ProfileID,
		pref.SeriesID,
		pref.AudioTrackIndex,
		pref.AudioLanguage,
		string(signatureJSON),
		pref.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("setting audio preference for profile %q series %q: %w",
			pref.ProfileID, pref.SeriesID, err)
	}
	return nil
}

// GetAudioPreference retrieves the audio preference for a profile
// and series. Returns nil (not an error) if no preference exists.
func GetAudioPreference(db *sql.DB, profileID, seriesID string) (*AudioPreference, error) {
	var pref AudioPreference
	var signatureJSON string
	err := db.QueryRow(`
		SELECT profile_id, series_id, audio_track_index,
		       audio_language, audio_track_signature, updated_at
		FROM audio_preferences
		WHERE profile_id = ? AND series_id = ?`,
		profileID, seriesID,
	).Scan(
		&pref.ProfileID,
		&pref.SeriesID,
		&pref.AudioTrackIndex,
		&pref.AudioLanguage,
		&signatureJSON,
		&pref.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting audio preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	pref.TrackSignature, err = userstore.UnmarshalAudioTrackSignature([]byte(signatureJSON))
	if err != nil {
		return nil, fmt.Errorf("decoding audio track signature for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	return &pref, nil
}

// DeleteAudioPreference removes the audio preference for a profile
// and series. It is not an error if the preference does not exist.
func DeleteAudioPreference(db *sql.DB, profileID, seriesID string) error {
	return deleteAudioPreference(db, profileID, seriesID)
}

func deleteAudioPreference(exec preferenceSettingsExecutor, profileID, seriesID string) error {
	_, err := exec.Exec(
		"DELETE FROM audio_preferences WHERE profile_id = ? AND series_id = ?",
		profileID, seriesID,
	)
	if err != nil {
		return fmt.Errorf("deleting audio preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	return nil
}
