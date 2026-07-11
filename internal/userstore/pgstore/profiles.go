package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// scanProfile scans a profile row, converting TIMESTAMPTZ to string.
func scanProfile(scanner interface {
	Scan(dest ...any) error
}) (*userstore.Profile, error) {
	var p userstore.Profile
	var createdAt, updatedAt time.Time
	err := scanner.Scan(
		&p.ID, &p.Name, &p.Avatar, &p.PINHash, &p.IsChild, &p.IsPrimary, &p.MaxContentRating,
		&p.QualityPreference, &p.Language, &p.PreferredMetadataLanguage, &p.SubtitleLanguage, &p.SubtitleMode,
		&p.AutoSkipIntro, &p.AutoSkipCredits, &p.AutoSkipRecap, &p.AutoPlayNextPreview,
		&p.LibraryRestrictionsEnabled,
		&p.ShowForcedSubtitles, &p.MaxPlaybackQuality, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	p.CreatedAt = timeToString(createdAt)
	p.UpdatedAt = timeToString(updatedAt)
	return &p, nil
}

func (s *PostgresUserStore) CreateProfile(ctx context.Context, p userstore.Profile) error {
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

	// The first profile created for a user becomes the primary, giving it
	// rights to manage the household's other profiles without requiring a
	// server-wide admin role.
	var hasExisting bool
	if err := s.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM user_profiles WHERE user_id = $1)", s.userID,
	).Scan(&hasExisting); err != nil {
		return fmt.Errorf("checking existing profiles for user %d: %w", s.userID, err)
	}
	if !hasExisting {
		p.IsPrimary = true
	} else {
		p.IsPrimary = false
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_profiles (
			id, user_id, name, avatar, pin_hash, is_child, is_primary, max_content_rating,
			quality_preference, language, preferred_metadata_language, subtitle_language, subtitle_mode,
			auto_skip_intro, auto_skip_credits, auto_skip_recap, auto_play_next_preview,
			library_restrictions_enabled,
			show_forced_subtitles, max_playback_quality, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)`,
		p.ID, s.userID, p.Name, p.Avatar, p.PINHash, p.IsChild, p.IsPrimary, p.MaxContentRating,
		p.QualityPreference, p.Language, p.PreferredMetadataLanguage, p.SubtitleLanguage, p.SubtitleMode,
		p.AutoSkipIntro, p.AutoSkipCredits, p.AutoSkipRecap, p.AutoPlayNextPreview,
		p.LibraryRestrictionsEnabled,
		p.ShowForcedSubtitles, p.MaxPlaybackQuality, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting profile %s: %w", p.ID, err)
	}
	if err := replaceProfileAllowedLibraries(ctx, s.pool, s.userID, p.ID, p.AllowedLibraryIDs); err != nil {
		return err
	}
	return nil
}

func (s *PostgresUserStore) GetProfile(ctx context.Context, id string) (*userstore.Profile, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, name, avatar, pin_hash, is_child, is_primary, max_content_rating,
		       quality_preference, language, preferred_metadata_language, subtitle_language, subtitle_mode,
		       auto_skip_intro, auto_skip_credits, auto_skip_recap, auto_play_next_preview, library_restrictions_enabled,
		       show_forced_subtitles, max_playback_quality, created_at, updated_at
		FROM user_profiles WHERE user_id = $1 AND id = $2`, s.userID, id)

	p, err := scanProfile(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying profile %s: %w", id, err)
	}
	p.AllowedLibraryIDs, err = listProfileAllowedLibraries(ctx, s.pool, s.userID, p.ID)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *PostgresUserStore) ListProfiles(ctx context.Context) ([]userstore.Profile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, avatar, pin_hash, is_child, is_primary, max_content_rating,
		       quality_preference, language, preferred_metadata_language, subtitle_language, subtitle_mode,
		       auto_skip_intro, auto_skip_credits, auto_skip_recap, auto_play_next_preview, library_restrictions_enabled,
		       show_forced_subtitles, max_playback_quality, created_at, updated_at
		FROM user_profiles WHERE user_id = $1 ORDER BY created_at ASC`, s.userID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}
	defer rows.Close()

	var profiles []userstore.Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning profile row: %w", err)
		}
		profiles = append(profiles, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating profile rows: %w", err)
	}
	if err := s.attachAllowedLibraries(ctx, profiles); err != nil {
		return nil, err
	}
	return profiles, nil
}

func (s *PostgresUserStore) UpdateProfile(ctx context.Context, id string, u userstore.UpdateProfileInput) error {
	var setClauses []string
	var args []any
	argIdx := 1
	accessPolicyChanged := u.PIN != nil ||
		u.IsChild != nil ||
		u.MaxContentRating != nil ||
		u.LibraryRestrictionsEnabled != nil ||
		u.AllowedLibraryIDs != nil ||
		u.MaxPlaybackQuality != nil

	addArg := func(clause string, val any) {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", clause, argIdx))
		args = append(args, val)
		argIdx++
	}

	if u.Name != nil {
		addArg("name", *u.Name)
	}
	if u.Avatar != nil {
		addArg("avatar", *u.Avatar)
	}
	if u.PIN != nil {
		if *u.PIN == "" {
			// Empty string clears the PIN rather than hashing an empty value.
			addArg("pin_hash", "")
		} else {
			hash, err := bcrypt.GenerateFromPassword([]byte(*u.PIN), bcrypt.DefaultCost)
			if err != nil {
				return fmt.Errorf("hashing PIN: %w", err)
			}
			addArg("pin_hash", string(hash))
		}
	}
	if u.IsChild != nil {
		addArg("is_child", *u.IsChild)
	}
	if u.MaxContentRating != nil {
		addArg("max_content_rating", *u.MaxContentRating)
	}
	if u.QualityPreference != nil {
		addArg("quality_preference", *u.QualityPreference)
	}
	if u.Language != nil {
		addArg("language", *u.Language)
	}
	if u.PreferredMetadataLanguage != nil {
		addArg("preferred_metadata_language", *u.PreferredMetadataLanguage)
	}
	if u.SubtitleLanguage != nil {
		addArg("subtitle_language", *u.SubtitleLanguage)
	}
	if u.SubtitleMode != nil {
		addArg("subtitle_mode", *u.SubtitleMode)
	}
	if u.AutoSkipIntro != nil {
		addArg("auto_skip_intro", *u.AutoSkipIntro)
	}
	if u.AutoSkipCredits != nil {
		addArg("auto_skip_credits", *u.AutoSkipCredits)
	}
	if u.AutoSkipRecap != nil {
		addArg("auto_skip_recap", *u.AutoSkipRecap)
	}
	if u.AutoPlayNextPreview != nil {
		addArg("auto_play_next_preview", *u.AutoPlayNextPreview)
	}
	if u.LibraryRestrictionsEnabled != nil {
		addArg("library_restrictions_enabled", *u.LibraryRestrictionsEnabled)
	}
	if u.ShowForcedSubtitles != nil {
		addArg("show_forced_subtitles", *u.ShowForcedSubtitles)
	}
	if u.MaxPlaybackQuality != nil {
		addArg("max_playback_quality", *u.MaxPlaybackQuality)
	}

	if len(setClauses) == 0 && u.AllowedLibraryIDs == nil {
		return nil
	}

	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM user_profiles WHERE user_id = $1 AND id = $2)", s.userID, id).Scan(&exists); err != nil {
		return fmt.Errorf("checking profile %s existence: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("profile %s not found", id)
	}

	if len(setClauses) > 0 {
		addArg("updated_at", nowUTC())

		whereClause := fmt.Sprintf("WHERE user_id = $%d AND id = $%d", argIdx, argIdx+1)
		args = append(args, s.userID, id)

		query := fmt.Sprintf("UPDATE user_profiles SET %s %s", strings.Join(setClauses, ", "), whereClause)
		result, err := s.pool.Exec(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("updating profile %s: %w", id, err)
		}
		if result.RowsAffected() == 0 {
			return fmt.Errorf("profile %s not found", id)
		}
	}

	if u.AllowedLibraryIDs != nil {
		if err := replaceProfileAllowedLibraries(ctx, s.pool, s.userID, id, *u.AllowedLibraryIDs); err != nil {
			return err
		}
	}
	if accessPolicyChanged {
		if _, err := s.pool.Exec(ctx, "UPDATE users SET access_policy_revision = access_policy_revision + 1 WHERE id = $1", s.userID); err != nil {
			return fmt.Errorf("bumping access policy revision for user %d: %w", s.userID, err)
		}
	}
	return nil
}

func (s *PostgresUserStore) DeleteProfile(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction for profile delete: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx, `
		DELETE FROM user_personal_collection_items
		WHERE user_id = $1 AND collection_id IN (
			SELECT id FROM user_personal_collections WHERE user_id = $1 AND profile_id = $2
		)`, s.userID, id)
	if err != nil {
		return fmt.Errorf("deleting collection items for profile %s: %w", id, err)
	}

	cascadeTables := []string{
		"user_favorites",
		"user_watchlist",
		"user_watch_progress",
		"user_personal_collections",
		"user_series_playback_preferences",
		"user_library_playback_preferences",
	}
	for _, table := range cascadeTables {
		if _, err := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE user_id = $1 AND profile_id = $2", table), s.userID, id); err != nil {
			return fmt.Errorf("deleting from %s for profile %s: %w", table, id, err)
		}
	}

	result, err := tx.Exec(ctx, "DELETE FROM user_profiles WHERE user_id = $1 AND id = $2", s.userID, id)
	if err != nil {
		return fmt.Errorf("deleting profile %s: %w", id, err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("profile %s not found", id)
	}

	return tx.Commit(ctx)
}

func (s *PostgresUserStore) VerifyPIN(ctx context.Context, profileID, pin string) (bool, error) {
	var pinHash string
	err := s.pool.QueryRow(ctx,
		"SELECT pin_hash FROM user_profiles WHERE user_id = $1 AND id = $2",
		s.userID, profileID,
	).Scan(&pinHash)
	if err == pgx.ErrNoRows {
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
