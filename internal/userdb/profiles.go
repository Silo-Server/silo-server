package userdb

import (
	"database/sql"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Profile is an alias for the canonical type in userstore.
type Profile = userstore.Profile

// UpdateProfileInput is an alias for the canonical type in userstore.
type UpdateProfileInput = userstore.UpdateProfileInput

// CreateProfile inserts a new profile into the profiles table.
// The Profile.ID field must be set before calling this function; if empty,
// a new UUID is generated. CreatedAt and UpdatedAt are set to the current
// UTC time if not already populated.
func CreateProfile(db *sql.DB, p Profile) error {
	if p.ID == "" {
		p.ID = generateUUID()
	}
	now := nowUTC()
	if p.CreatedAt == "" {
		p.CreatedAt = now
	}
	if p.UpdatedAt == "" {
		p.UpdatedAt = now
	}
	if !p.ShowForcedSubtitles {
		p.ShowForcedSubtitles = true
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction for profile insert %s: %w", p.ID, err)
	}
	defer tx.Rollback() //nolint:errcheck

	// The first profile in this per-user database is the primary.
	var hasExisting bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM profiles)").Scan(&hasExisting); err != nil {
		return fmt.Errorf("checking existing profiles for %s: %w", p.ID, err)
	}
	if !hasExisting {
		p.IsPrimary = true
	} else {
		p.IsPrimary = false
	}

	_, err = tx.Exec(`
		INSERT INTO profiles (
			id, name, avatar, pin_hash, is_child, is_primary, max_content_rating,
			quality_preference, language, subtitle_language, subtitle_mode,
			auto_skip_intro, auto_skip_credits, auto_skip_recap, auto_play_next_preview,
			show_forced_subtitles,
			library_restrictions_enabled, max_playback_quality, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Avatar, p.PINHash, p.IsChild, p.IsPrimary, p.MaxContentRating,
		p.QualityPreference, p.Language, p.SubtitleLanguage, p.SubtitleMode,
		p.AutoSkipIntro, p.AutoSkipCredits, p.AutoSkipRecap, p.AutoPlayNextPreview,
		p.ShowForcedSubtitles, p.LibraryRestrictionsEnabled,
		p.MaxPlaybackQuality, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting profile %s: %w", p.ID, err)
	}
	if err := replaceProfileAllowedLibrariesTx(tx, p.ID, p.AllowedLibraryIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// GetProfile retrieves a single profile by ID. Returns nil and no error if
// the profile does not exist.
func GetProfile(db *sql.DB, id string) (*Profile, error) {
	var p Profile
	err := db.QueryRow(`
		SELECT id, name, avatar, pin_hash, is_child, is_primary, max_content_rating,
		       quality_preference, language, subtitle_language, subtitle_mode,
		       auto_skip_intro, auto_skip_credits, auto_skip_recap, auto_play_next_preview, show_forced_subtitles,
		       library_restrictions_enabled, max_playback_quality, created_at, updated_at
		FROM profiles WHERE id = ?`, id,
	).Scan(
		&p.ID, &p.Name, &p.Avatar, &p.PINHash, &p.IsChild, &p.IsPrimary, &p.MaxContentRating,
		&p.QualityPreference, &p.Language, &p.SubtitleLanguage, &p.SubtitleMode,
		&p.AutoSkipIntro, &p.AutoSkipCredits, &p.AutoSkipRecap, &p.AutoPlayNextPreview, &p.ShowForcedSubtitles,
		&p.LibraryRestrictionsEnabled, &p.MaxPlaybackQuality, &p.CreatedAt, &p.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying profile %s: %w", id, err)
	}
	p.AllowedLibraryIDs, err = listProfileAllowedLibraries(db, id)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListProfiles returns all profiles ordered by creation time.
func ListProfiles(db *sql.DB) ([]Profile, error) {
	rows, err := db.Query(`
		SELECT id, name, avatar, pin_hash, is_child, is_primary, max_content_rating,
		       quality_preference, language, subtitle_language, subtitle_mode,
		       auto_skip_intro, auto_skip_credits, auto_skip_recap, auto_play_next_preview, show_forced_subtitles,
		       library_restrictions_enabled, max_playback_quality, created_at, updated_at
		FROM profiles ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}
	defer rows.Close()

	var profiles []Profile
	for rows.Next() {
		var p Profile
		if err := rows.Scan(
			&p.ID, &p.Name, &p.Avatar, &p.PINHash, &p.IsChild, &p.IsPrimary, &p.MaxContentRating,
			&p.QualityPreference, &p.Language, &p.SubtitleLanguage, &p.SubtitleMode,
			&p.AutoSkipIntro, &p.AutoSkipCredits, &p.AutoSkipRecap, &p.AutoPlayNextPreview, &p.ShowForcedSubtitles,
			&p.LibraryRestrictionsEnabled, &p.MaxPlaybackQuality, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning profile row: %w", err)
		}
		profiles = append(profiles, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating profile rows: %w", err)
	}
	if err := attachAllowedLibraries(db, profiles); err != nil {
		return nil, err
	}
	return profiles, nil
}

// UpdateProfile applies partial updates to a profile. Only non-nil fields
// in the UpdateProfileInput are changed. If PIN is provided, it is bcrypt-hashed
// before storage.
func UpdateProfile(db *sql.DB, id string, u UpdateProfileInput) error {
	var setClauses []string
	var args []any

	if u.Name != nil {
		setClauses = append(setClauses, "name = ?")
		args = append(args, *u.Name)
	}
	if u.Avatar != nil {
		setClauses = append(setClauses, "avatar = ?")
		args = append(args, *u.Avatar)
	}
	if u.PIN != nil {
		if *u.PIN == "" {
			// Empty string clears the PIN rather than hashing an empty value.
			setClauses = append(setClauses, "pin_hash = ?")
			args = append(args, "")
		} else {
			hash, err := bcrypt.GenerateFromPassword([]byte(*u.PIN), bcrypt.DefaultCost)
			if err != nil {
				return fmt.Errorf("hashing PIN: %w", err)
			}
			setClauses = append(setClauses, "pin_hash = ?")
			args = append(args, string(hash))
		}
	}
	if u.IsChild != nil {
		setClauses = append(setClauses, "is_child = ?")
		args = append(args, *u.IsChild)
	}
	if u.MaxContentRating != nil {
		setClauses = append(setClauses, "max_content_rating = ?")
		args = append(args, *u.MaxContentRating)
	}
	if u.QualityPreference != nil {
		setClauses = append(setClauses, "quality_preference = ?")
		args = append(args, *u.QualityPreference)
	}
	if u.Language != nil {
		setClauses = append(setClauses, "language = ?")
		args = append(args, *u.Language)
	}
	if u.SubtitleLanguage != nil {
		setClauses = append(setClauses, "subtitle_language = ?")
		args = append(args, *u.SubtitleLanguage)
	}
	if u.SubtitleMode != nil {
		setClauses = append(setClauses, "subtitle_mode = ?")
		args = append(args, *u.SubtitleMode)
	}
	if u.AutoSkipIntro != nil {
		setClauses = append(setClauses, "auto_skip_intro = ?")
		args = append(args, *u.AutoSkipIntro)
	}
	if u.AutoSkipRecap != nil {
		setClauses = append(setClauses, "auto_skip_recap = ?")
		args = append(args, *u.AutoSkipRecap)
	}
	if u.AutoPlayNextPreview != nil {
		setClauses = append(setClauses, "auto_play_next_preview = ?")
		args = append(args, *u.AutoPlayNextPreview)
	}
	if u.AutoSkipCredits != nil {
		setClauses = append(setClauses, "auto_skip_credits = ?")
		args = append(args, *u.AutoSkipCredits)
	}
	if u.ShowForcedSubtitles != nil {
		setClauses = append(setClauses, "show_forced_subtitles = ?")
		args = append(args, *u.ShowForcedSubtitles)
	}
	if u.LibraryRestrictionsEnabled != nil {
		setClauses = append(setClauses, "library_restrictions_enabled = ?")
		args = append(args, *u.LibraryRestrictionsEnabled)
	}
	if u.MaxPlaybackQuality != nil {
		setClauses = append(setClauses, "max_playback_quality = ?")
		args = append(args, *u.MaxPlaybackQuality)
	}

	if len(setClauses) == 0 && u.AllowedLibraryIDs == nil {
		return nil // nothing to update
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction for profile update %s: %w", id, err)
	}
	defer tx.Rollback() //nolint:errcheck

	var exists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM profiles WHERE id = ?)", id).Scan(&exists); err != nil {
		return fmt.Errorf("checking profile %s existence: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("profile %s not found", id)
	}

	if len(setClauses) > 0 {
		// Always update the timestamp when profile attributes change.
		setClauses = append(setClauses, "updated_at = ?")
		args = append(args, nowUTC())

		// WHERE clause.
		args = append(args, id)

		query := fmt.Sprintf("UPDATE profiles SET %s WHERE id = ?", strings.Join(setClauses, ", "))
		result, err := tx.Exec(query, args...)
		if err != nil {
			return fmt.Errorf("updating profile %s: %w", id, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("checking rows affected for profile %s: %w", id, err)
		}
		if rows == 0 {
			return fmt.Errorf("profile %s not found", id)
		}
	}

	if u.AllowedLibraryIDs != nil {
		if err := replaceProfileAllowedLibrariesTx(tx, id, *u.AllowedLibraryIDs); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// DeleteProfile removes a profile and cascades deletion to favorites,
// watchlist, watch_progress, and personal_collections (plus their items).
func DeleteProfile(db *sql.DB, id string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction for profile delete: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Delete personal_collection_items for collections owned by this profile.
	_, err = tx.Exec(`
		DELETE FROM personal_collection_items
		WHERE collection_id IN (
			SELECT id FROM personal_collections WHERE creator_profile_id = ?
		)`, id)
	if err != nil {
		return fmt.Errorf("deleting collection items for profile %s: %w", id, err)
	}
	if _, err := tx.Exec(`DELETE FROM personal_collection_profiles WHERE profile_id = ?`, id); err != nil {
		return fmt.Errorf("deleting collection visibility for profile %s: %w", id, err)
	}

	// Cascade-delete related tables.
	cascadeTables := []string{
		"favorites",
		"watchlist",
		"watch_progress",
		"personal_collections",
		"profile_allowed_libraries",
		"series_playback_preferences",
		"library_playback_preferences",
	}
	for _, table := range cascadeTables {
		column := "profile_id"
		if table == "personal_collections" {
			column = "creator_profile_id"
		}
		if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s = ?", table, column), id); err != nil {
			return fmt.Errorf("deleting from %s for profile %s: %w", table, id, err)
		}
	}

	// Delete the profile itself.
	result, err := tx.Exec("DELETE FROM profiles WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting profile %s: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for profile %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("profile %s not found", id)
	}

	return tx.Commit()
}

// VerifyPIN checks a plaintext PIN against the bcrypt hash stored for a
// profile. Returns true if the PIN matches, false otherwise. Returns an
// error if the profile is not found or has no PIN set.
func VerifyPIN(db *sql.DB, profileID string, pin string) (bool, error) {
	var pinHash string
	err := db.QueryRow("SELECT pin_hash FROM profiles WHERE id = ?", profileID).Scan(&pinHash)
	if err == sql.ErrNoRows {
		return false, fmt.Errorf("profile %s not found", profileID)
	}
	if err != nil {
		return false, fmt.Errorf("querying PIN hash for profile %s: %w", profileID, err)
	}
	if pinHash == "" {
		return false, fmt.Errorf("profile %s has no PIN set", profileID)
	}

	err = bcrypt.CompareHashAndPassword([]byte(pinHash), []byte(pin))
	if err == bcrypt.ErrMismatchedHashAndPassword {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("comparing PIN for profile %s: %w", profileID, err)
	}
	return true, nil
}
