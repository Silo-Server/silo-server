package userdb

import (
	"database/sql"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ListSectionOverrides returns all section overrides for a profile+scope+library.
func ListSectionOverrides(db *sql.DB, profileID, scope, libraryID string) ([]userstore.SectionOverride, error) {
	query := `SELECT id, profile_id, scope, COALESCE(library_id,''), COALESCE(section_id,''),
		position, hidden, removed, COALESCE(section_type,''), COALESCE(title,''),
		featured, item_limit, COALESCE(config,''),
		COALESCE(is_user_added, 0), COALESCE(user_section_type,''),
		COALESCE(user_config,''), COALESCE(user_title,''),
		created_at, updated_at
		FROM profile_section_overrides
		WHERE profile_id = ? AND scope = ? AND COALESCE(library_id,'') = ?
		ORDER BY COALESCE(position, 999999)`

	rows, err := db.Query(query, profileID, scope, libraryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var overrides []userstore.SectionOverride
	for rows.Next() {
		var o userstore.SectionOverride
		if err := rows.Scan(
			&o.ID, &o.ProfileID, &o.Scope, &o.LibraryID, &o.SectionID,
			&o.Position, &o.Hidden, &o.Removed, &o.SectionType, &o.Title,
			&o.Featured, &o.ItemLimit, &o.Config,
			&o.IsUserAdded, &o.UserSectionType, &o.UserConfig, &o.UserTitle,
			&o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, err
		}
		overrides = append(overrides, o)
	}
	return overrides, rows.Err()
}

// ListAllSectionOverrides returns all overrides in one account database.
func ListAllSectionOverrides(db *sql.DB) ([]userstore.SectionOverride, error) {
	query := `SELECT id, profile_id, scope, COALESCE(library_id,''), COALESCE(section_id,''),
		position, hidden, removed, COALESCE(section_type,''), COALESCE(title,''),
		featured, item_limit, COALESCE(config,''),
		COALESCE(is_user_added, 0), COALESCE(user_section_type,''),
		COALESCE(user_config,''), COALESCE(user_title,''),
		created_at, updated_at
		FROM profile_section_overrides
		ORDER BY profile_id, scope, COALESCE(library_id,''), COALESCE(position, 999999)`

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var overrides []userstore.SectionOverride
	for rows.Next() {
		var o userstore.SectionOverride
		if err := rows.Scan(
			&o.ID, &o.ProfileID, &o.Scope, &o.LibraryID, &o.SectionID,
			&o.Position, &o.Hidden, &o.Removed, &o.SectionType, &o.Title,
			&o.Featured, &o.ItemLimit, &o.Config,
			&o.IsUserAdded, &o.UserSectionType, &o.UserConfig, &o.UserTitle,
			&o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, err
		}
		overrides = append(overrides, o)
	}
	return overrides, rows.Err()
}

// SaveSectionOverrides replaces all section overrides for a profile+scope+library.
func SaveSectionOverrides(db *sql.DB, profileID, scope, libraryID string, overrides []userstore.SectionOverride) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		`DELETE FROM profile_section_overrides WHERE profile_id = ? AND scope = ? AND COALESCE(library_id,'') = ?`,
		profileID, scope, libraryID,
	)
	if err != nil {
		return err
	}

	for _, o := range overrides {
		id := o.ID
		if id == "" {
			id = generateUUID()
		}
		now := nowUTC()
		_, err := tx.Exec(
			`INSERT INTO profile_section_overrides
				(id, profile_id, scope, library_id, section_id, position, hidden, removed,
				 section_type, title, featured, item_limit, config,
				 is_user_added, user_section_type, user_config, user_title,
				 created_at, updated_at)
			VALUES (?, ?, ?, NULLIF(?,''), NULLIF(?,''), ?, ?, ?,
				NULLIF(?,''), NULLIF(?,''), ?, ?, NULLIF(?,''),
				?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''),
				?, ?)`,
			id, profileID, scope, libraryID, o.SectionID,
			o.Position, o.Hidden, o.Removed,
			o.SectionType, o.Title, o.Featured, o.ItemLimit, o.Config,
			o.IsUserAdded, o.UserSectionType, o.UserConfig, o.UserTitle,
			now, now,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ResetSectionOverrides deletes all overrides for a profile+scope+library.
func ResetSectionOverrides(db *sql.DB, profileID, scope, libraryID string) error {
	_, err := db.Exec(
		`DELETE FROM profile_section_overrides WHERE profile_id = ? AND scope = ? AND COALESCE(library_id,'') = ?`,
		profileID, scope, libraryID,
	)
	return err
}
