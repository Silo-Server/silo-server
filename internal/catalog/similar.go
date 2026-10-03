package catalog

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

// similarCandidateLimit is how many similar titles the source pages through,
// the catalog's own page ceiling.
const similarCandidateLimit = 100

// similarContentIDsSQL ranks titles of the source's type by how many of the
// source's first three genres, first director, first writer and five
// top-billed actors they share, wherever they list them. A title sharing none
// is left out. Ties go to the newer title, then the one added first.
const similarContentIDsSQL = `
WITH src AS (
	SELECT content_id, type, genres[1:3] AS genres
	FROM media_items
	WHERE content_id = $1
), picked AS (
	(SELECT person_id, kind FROM item_people
		WHERE content_id = $1 AND kind = $2 ORDER BY sort_order, id LIMIT 1)
	UNION ALL
	(SELECT person_id, kind FROM item_people
		WHERE content_id = $1 AND kind = $3 ORDER BY sort_order, id LIMIT 1)
	UNION ALL
	(SELECT person_id, kind FROM item_people
		WHERE content_id = $1 AND kind = $4 ORDER BY sort_order, id LIMIT 5)
), shared_people AS (
	SELECT other.content_id, COUNT(DISTINCT (other.person_id, other.kind)) AS shared
	FROM picked
	JOIN item_people other ON other.person_id = picked.person_id AND other.kind = picked.kind
	WHERE other.content_id <> $1
	GROUP BY other.content_id
)
SELECT mi.content_id
FROM media_items mi
CROSS JOIN src
LEFT JOIN shared_people p ON p.content_id = mi.content_id
WHERE mi.type = src.type
	AND mi.content_id <> src.content_id
	AND (mi.genres && src.genres OR p.content_id IS NOT NULL)
ORDER BY
	cardinality(ARRAY(SELECT unnest(mi.genres) INTERSECT SELECT unnest(src.genres)))
	+ COALESCE(p.shared, 0) DESC,
	COALESCE(mi.release_date,
		CASE WHEN mi.first_air_date ~ '^\d{4}-\d{2}-\d{2}$' THEN mi.first_air_date::date END) DESC NULLS LAST,
	mi.created_at,
	mi.content_id
LIMIT $5`

// SimilarContentIDs answers the titles most like contentID by shared
// metadata, best first. An unknown title answers none.
func (r *ItemRepository) SimilarContentIDs(ctx context.Context, contentID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, similarContentIDsSQL,
		contentID,
		int(models.PersonKindDirector),
		int(models.PersonKindWriter),
		int(models.PersonKindActor),
		similarCandidateLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("listing similar titles: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("reading similar titles: %w", err)
	}
	return ids, nil
}
