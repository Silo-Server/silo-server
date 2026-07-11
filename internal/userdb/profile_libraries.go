package userdb

import (
	"database/sql"
	"fmt"
	"strings"
)

func listProfileAllowedLibraries(db *sql.DB, profileID string) ([]int, error) {
	rows, err := db.Query(
		"SELECT library_id FROM profile_allowed_libraries WHERE profile_id = ? ORDER BY library_id ASC",
		profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing allowed libraries for profile %s: %w", profileID, err)
	}
	defer rows.Close()

	var libraryIDs []int
	for rows.Next() {
		var libraryID int
		if err := rows.Scan(&libraryID); err != nil {
			return nil, fmt.Errorf("scanning allowed library for profile %s: %w", profileID, err)
		}
		libraryIDs = append(libraryIDs, libraryID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating allowed libraries for profile %s: %w", profileID, err)
	}
	return libraryIDs, nil
}

// attachAllowedLibraries fills AllowedLibraryIDs for every profile with a
// single batched query, avoiding the N+1 round trip a per-profile lookup
// would create.
func attachAllowedLibraries(db *sql.DB, profiles []Profile) error {
	if len(profiles) == 0 {
		return nil
	}
	placeholders := make([]string, len(profiles))
	args := make([]any, len(profiles))
	for i := range profiles {
		placeholders[i] = "?"
		args[i] = profiles[i].ID
	}
	rows, err := db.Query(
		`SELECT profile_id, library_id FROM profile_allowed_libraries
		 WHERE profile_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY library_id ASC`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("listing allowed libraries: %w", err)
	}
	defer rows.Close()

	byProfile := make(map[string][]int, len(profiles))
	for rows.Next() {
		var profileID string
		var libraryID int
		if err := rows.Scan(&profileID, &libraryID); err != nil {
			return fmt.Errorf("scanning allowed library: %w", err)
		}
		byProfile[profileID] = append(byProfile[profileID], libraryID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating allowed libraries: %w", err)
	}
	for i := range profiles {
		profiles[i].AllowedLibraryIDs = byProfile[profiles[i].ID]
	}
	return nil
}

func replaceProfileAllowedLibrariesTx(tx *sql.Tx, profileID string, libraryIDs []int) error {
	if _, err := tx.Exec("DELETE FROM profile_allowed_libraries WHERE profile_id = ?", profileID); err != nil {
		return fmt.Errorf("clearing allowed libraries for profile %s: %w", profileID, err)
	}
	for _, libraryID := range libraryIDs {
		if _, err := tx.Exec(
			"INSERT INTO profile_allowed_libraries (profile_id, library_id) VALUES (?, ?)",
			profileID,
			libraryID,
		); err != nil {
			return fmt.Errorf("inserting allowed library %d for profile %s: %w", libraryID, profileID, err)
		}
	}
	return nil
}
