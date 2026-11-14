package metadata

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ArtworkPublicationStore reads the publication manifests catalog reads
// select cached variants from.
type ArtworkPublicationStore struct {
	pool *pgxpool.Pool
}

func NewArtworkPublicationStore(pool *pgxpool.Pool) *ArtworkPublicationStore {
	return &ArtworkPublicationStore{pool: pool}
}

// ArtworkAvailability returns the manifest of each revision in paths that has
// one. A collected revision has an empty manifest; a legacy record without
// one is absent.
func (s *ArtworkPublicationStore) ArtworkAvailability(ctx context.Context, paths []string) (map[string]ArtworkAvailability, error) {
	rows, err := s.pool.Query(ctx, `SELECT original_path,
        CASE WHEN deleted_at IS NULL THEN published_keys ELSE '{}'::text[] END
        FROM artwork_revision_gc_candidates
        WHERE original_path = ANY($1) AND (published_keys IS NOT NULL OR deleted_at IS NOT NULL)`, paths)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[string]ArtworkAvailability)
	for rows.Next() {
		var path string
		var state ArtworkAvailability
		if err := rows.Scan(&path, &state.Published); err != nil {
			return nil, err
		}
		states[path] = state
	}
	return states, rows.Err()
}
