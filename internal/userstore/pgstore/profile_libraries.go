package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type profileLibraryExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// attachAllowedLibraries fills AllowedLibraryIDs for every profile with a
// single batched query, avoiding the N+1 round trip a per-profile lookup
// would create.
func (s *PostgresUserStore) attachAllowedLibraries(ctx context.Context, profiles []userstore.Profile) error {
	if len(profiles) == 0 {
		return nil
	}
	ids := make([]string, len(profiles))
	for i := range profiles {
		ids[i] = profiles[i].ID
	}
	rows, err := s.pool.Query(ctx,
		`SELECT profile_id, library_id
		FROM user_profile_allowed_libraries
		WHERE user_id = $1 AND profile_id = ANY($2)
		ORDER BY library_id ASC`,
		s.userID,
		ids,
	)
	if err != nil {
		return fmt.Errorf("listing allowed libraries for user %d: %w", s.userID, err)
	}
	defer rows.Close()

	var allowedLibraries []userstore.ProfileAllowedLibrary
	for rows.Next() {
		var allowedLibrary userstore.ProfileAllowedLibrary
		if err := rows.Scan(&allowedLibrary.ProfileID, &allowedLibrary.LibraryID); err != nil {
			return fmt.Errorf("scanning allowed library for user %d: %w", s.userID, err)
		}
		allowedLibraries = append(allowedLibraries, allowedLibrary)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating allowed libraries for user %d: %w", s.userID, err)
	}
	userstore.AttachAllowedLibraries(profiles, allowedLibraries)
	return nil
}

func replaceProfileAllowedLibraries(ctx context.Context, q profileLibraryExecutor, userID int, profileID string, libraryIDs []int) error {
	if _, err := q.Exec(ctx,
		"DELETE FROM user_profile_allowed_libraries WHERE user_id = $1 AND profile_id = $2",
		userID,
		profileID,
	); err != nil {
		return fmt.Errorf("clearing allowed libraries for profile %s: %w", profileID, err)
	}
	for _, libraryID := range libraryIDs {
		if _, err := q.Exec(ctx,
			"INSERT INTO user_profile_allowed_libraries (user_id, profile_id, library_id) VALUES ($1, $2, $3)",
			userID,
			profileID,
			libraryID,
		); err != nil {
			return fmt.Errorf("inserting allowed library %d for profile %s: %w", libraryID, profileID, err)
		}
	}
	return nil
}
