package downloads

import "context"

// GetByIDs reads the same complete artifact rows as GetByID. Missing IDs are
// absent; a failed query or row decode remains an error for the whole batch.
func (r *ArtifactRepository) GetByIDs(ctx context.Context, ids []string) ([]*Artifact, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+artifactColumns+` FROM download_artifacts WHERE id=ANY($1::text[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArtifacts(rows)
}
