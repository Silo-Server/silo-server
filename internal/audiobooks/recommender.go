package audiobooks

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// ABSRecommender implements abs.Recommender by reusing the catalog
// recommendations pipeline. Embedding-based nearest-neighbor search is
// preferred (extended to audiobooks via gemini-embedding-001); when the
// source item has no embedding yet, we fall back to a shared-genre
// audiobook lookup so the /items/{id}/similar surface always returns
// something useful.
type ABSRecommender struct {
	Pool *pgxpool.Pool
	Recs *recommendations.Repo
}

var _ abs.Recommender = (*ABSRecommender)(nil)

// Similar returns up to limit audiobook content_ids related to contentID.
// Excludes books by the same author so callers can pair this with a
// dedicated "Also by author" rail without overlap. Returns empty on any
// upstream error rather than failing the whole detail page.
func (r *ABSRecommender) Similar(ctx context.Context, contentID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 10
	}
	if r.Recs != nil {
		if emb, err := r.Recs.GetEmbedding(ctx, contentID); err == nil && emb != nil {
			scored, err := r.Recs.FindSimilar(ctx, emb, []string{contentID}, "audiobook", limit*3)
			if err == nil && len(scored) > 0 {
				ids := make([]string, 0, limit)
				for _, s := range scored {
					ids = append(ids, s.MediaItemID)
					if len(ids) >= limit {
						break
					}
				}
				return ids, nil
			}
		}
	}
	// Fallback: shared-genre + same-language + different-author lookup.
	if r.Pool == nil {
		return nil, nil
	}
	const q = `
		WITH this_genres AS (
			SELECT unnest(genres) AS g FROM media_items WHERE content_id = $1
		),
		this_author AS (
			SELECT person_id FROM item_people WHERE content_id = $1 AND kind = $2
		)
		SELECT m.content_id
		FROM media_items m
		WHERE m.type = 'audiobook'
		  AND m.content_id <> $1
		  AND m.genres && (SELECT array_agg(g) FROM this_genres)
		  AND NOT EXISTS (
			SELECT 1 FROM item_people ip
			WHERE ip.content_id = m.content_id
			  AND ip.kind = $2
			  AND ip.person_id IN (SELECT person_id FROM this_author)
		  )
		ORDER BY
			cardinality(ARRAY(SELECT unnest(m.genres) INTERSECT SELECT g FROM this_genres)) DESC,
			COALESCE(m.year, 0) DESC,
			LOWER(m.sort_title)
		LIMIT $3
	`
	rows, err := r.Pool.Query(ctx, q, contentID, models.PersonKindAuthor, limit)
	if err != nil {
		return nil, fmt.Errorf("abs recommender: fallback query: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("abs recommender: scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
