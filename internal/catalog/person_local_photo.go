package catalog

import (
	"context"
	"fmt"
)

// PersonPhotoState is a person's stored photo: the served path and the
// source it was (or is being) cached from.
type PersonPhotoState struct {
	Path   string
	Source string
}

// PhotoStates returns the stored photo of each listed person that exists.
func (r *PersonRepository) PhotoStates(ctx context.Context, ids []int64) (map[int64]PersonPhotoState, error) {
	states := make(map[int64]PersonPhotoState, len(ids))
	if len(ids) == 0 {
		return states, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(photo_path, ''), COALESCE(photo_source_path, '')
		FROM people
		WHERE id = ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("reading person photos: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var state PersonPhotoState
		if err := rows.Scan(&id, &state.Path, &state.Source); err != nil {
			return nil, fmt.Errorf("scanning person photo: %w", err)
		}
		states[id] = state
	}
	return states, rows.Err()
}

// PersonPhotoSourceClaim points a person's photo source at Source, provided
// the stored photo still equals Expected, the state the claim was decided on.
type PersonPhotoSourceClaim struct {
	ID       int64
	Expected PersonPhotoState
	Source   string
}

// ClaimPhotoSources sets each claimed person's photo_source_path to Source
// when the stored photo path and source still hold Expected, so a concurrent
// writer that changed either first wins: provider enrichment can set
// photo_path to a provider photo and leave the source empty. photo_path and
// its thumbhash keep serving the current photo until the image cache stores
// the new source's copy (UpdatePhotoIfSourceMatches). It returns the ids it
// updated.
func (r *PersonRepository) ClaimPhotoSources(ctx context.Context, claims []PersonPhotoSourceClaim) ([]int64, error) {
	if len(claims) == 0 {
		return nil, nil
	}
	ids := make([]int64, len(claims))
	expectedPaths := make([]string, len(claims))
	expectedSources := make([]string, len(claims))
	sources := make([]string, len(claims))
	for i, claim := range claims {
		ids[i], expectedPaths[i], expectedSources[i], sources[i] = claim.ID, claim.Expected.Path, claim.Expected.Source, claim.Source
	}
	rows, err := r.pool.Query(ctx, `
		UPDATE people
		SET photo_source_path = t.source,
			updated_at = NOW()
		FROM UNNEST($1::bigint[], $2::text[], $3::text[], $4::text[]) AS t(id, expected_path, expected_source, source)
		WHERE people.id = t.id
		  AND COALESCE(people.photo_path, '') = t.expected_path
		  AND COALESCE(people.photo_source_path, '') = t.expected_source
		  AND people.photo_source_path IS DISTINCT FROM t.source
		RETURNING people.id`, ids, expectedPaths, expectedSources, sources)
	if err != nil {
		return nil, fmt.Errorf("claiming person photo sources: %w", err)
	}
	defer rows.Close()
	var claimed []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning claimed person photo: %w", err)
		}
		claimed = append(claimed, id)
	}
	return claimed, rows.Err()
}
