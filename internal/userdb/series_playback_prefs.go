package userdb

import (
	"database/sql"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SeriesPlaybackPreference is an alias for the canonical type in userstore.
type SeriesPlaybackPreference = userstore.SeriesPlaybackPreference

// SetSeriesPlaybackPreference creates or replaces a series playback preference
// for a given profile and series. Timestamps should be ISO 8601 UTC strings.
func SetSeriesPlaybackPreference(db *sql.DB, pref SeriesPlaybackPreference) error {
	if pref.UpdatedAt == "" {
		pref.UpdatedAt = nowUTC()
	}
	_, err := db.Exec(`
		INSERT INTO series_playback_preferences (
			profile_id, series_id, resolution, hdr, codec_video, file_id, edition_key, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, series_id) DO UPDATE SET
			resolution = excluded.resolution,
			hdr = excluded.hdr,
			codec_video = excluded.codec_video,
			file_id = excluded.file_id,
			edition_key = excluded.edition_key,
			updated_at = excluded.updated_at`,
		pref.ProfileID,
		pref.SeriesID,
		pref.Resolution,
		pref.HDR,
		pref.CodecVideo,
		sql.NullInt64{Int64: int64(pref.FileID), Valid: pref.FileID != 0},
		sql.NullString{String: pref.EditionKey, Valid: pref.EditionKey != ""},
		pref.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("setting series playback preference for profile %q series %q: %w",
			pref.ProfileID, pref.SeriesID, err)
	}
	return nil
}

// GetSeriesPlaybackPreference retrieves the version preference for a profile
// and series. Returns nil (not an error) if no preference exists.
func GetSeriesPlaybackPreference(db *sql.DB, profileID, seriesID string) (*SeriesPlaybackPreference, error) {
	var pref SeriesPlaybackPreference
	var fileID sql.NullInt64
	var editionKey sql.NullString
	err := db.QueryRow(`
		SELECT profile_id, series_id, resolution, hdr, codec_video, file_id, edition_key, updated_at
		FROM series_playback_preferences
		WHERE profile_id = ? AND series_id = ?`,
		profileID, seriesID,
	).Scan(
		&pref.ProfileID,
		&pref.SeriesID,
		&pref.Resolution,
		&pref.HDR,
		&pref.CodecVideo,
		&fileID,
		&editionKey,
		&pref.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting series playback preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	pref.FileID = int(fileID.Int64)
	pref.EditionKey = editionKey.String
	return &pref, nil
}

// DeleteSeriesPlaybackPreference removes the version preference for a profile
// and series. It is not an error if the preference does not exist.
func DeleteSeriesPlaybackPreference(db *sql.DB, profileID, seriesID string) error {
	_, err := db.Exec(
		"DELETE FROM series_playback_preferences WHERE profile_id = ? AND series_id = ?",
		profileID, seriesID,
	)
	if err != nil {
		return fmt.Errorf("deleting series playback preference for profile %q series %q: %w",
			profileID, seriesID, err)
	}
	return nil
}
