package userdb

import (
	"database/sql"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
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
	rows, err := db.Query(
		`SELECT profile_id, library_id
		 FROM profile_allowed_libraries
		 ORDER BY library_id ASC`,
	)
	if err != nil {
		return fmt.Errorf("listing allowed libraries: %w", err)
	}
	defer rows.Close()

	var allowedLibraries []userstore.ProfileAllowedLibrary
	for rows.Next() {
		var allowedLibrary userstore.ProfileAllowedLibrary
		if err := rows.Scan(&allowedLibrary.ProfileID, &allowedLibrary.LibraryID); err != nil {
			return fmt.Errorf("scanning allowed library: %w", err)
		}
		allowedLibraries = append(allowedLibraries, allowedLibrary)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating allowed libraries: %w", err)
	}
	userstore.AttachAllowedLibraries(profiles, allowedLibraries)
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
