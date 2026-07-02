package usercollections

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ServerVisibleCollection is the per-user view of a personal collection the
// owner has opted into their own library Collections tab. Personal collections
// are private to their owner; this list never crosses user boundaries.
type ServerVisibleCollection struct {
	ID               string `json:"id"`
	CreatorProfileID string `json:"creator_profile_id"`
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	CollectionType   string `json:"collection_type"`
	ItemCount        int    `json:"item_count"`
	PosterPath       string `json:"-"`
	PosterURL        string `json:"poster_url,omitempty"`
	PosterThumbhash  string `json:"poster_thumbhash,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// serverVisibleListLimit caps how many opt-in collections a single library tab
// will surface so a user with hundreds of published collections can't blow up
// the response.
const serverVisibleListLimit = 500

// ListServerVisibleByLibrary returns the current user's personal collections
// that have opted into their library Collections tab and whose library scope
// matches the requested library. Imported exact collections read library_ids
// from source_config, while legacy rows can fall back to query_definition. A
// collection with no library_ids is treated as library-agnostic and therefore
// visible in every library tab. Other users' collections are never returned.
func ListServerVisibleByLibrary(ctx context.Context, pool *pgxpool.Pool, userID int, profileID string, libraryID int) ([]ServerVisibleCollection, error) {
	rows, err := pool.Query(ctx,
		`WITH visible_collections AS (
			SELECT upc.id, upc.creator_profile_id, upc.name, upc.description, upc.collection_type,
			       upc.item_count, upc.poster_url, upc.poster_thumbhash, upc.created_at, upc.updated_at,
			       CASE
			         WHEN upc.collection_type = 'smart' THEN upc.query_definition
			         WHEN upc.source_config ? 'library_ids' THEN upc.source_config
			         ELSE upc.query_definition
			       END AS scope_config
			FROM user_personal_collections upc
			WHERE upc.user_id = $1
			  AND upc.include_in_server_collections = TRUE
			  AND EXISTS (
			    SELECT 1 FROM user_personal_collection_profiles vp
			    WHERE vp.user_id = upc.user_id
			      AND vp.collection_id = upc.id
			      AND vp.profile_id = $2
			  )
		)
		 SELECT id, creator_profile_id, name, description, collection_type, item_count,
		        poster_url, poster_thumbhash, created_at, updated_at
		 FROM visible_collections
		 WHERE TRUE
		   AND (
		     NOT (scope_config ? 'library_ids')
		     OR jsonb_array_length(COALESCE(scope_config->'library_ids', '[]'::jsonb)) = 0
		     OR scope_config->'library_ids' @> to_jsonb($3::int)
		   )
		 ORDER BY name ASC
		 LIMIT $4`,
		userID, profileID, libraryID, serverVisibleListLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("listing server-visible user collections: %w", err)
	}
	defer rows.Close()

	var out []ServerVisibleCollection
	for rows.Next() {
		var c ServerVisibleCollection
		var createdAt, updatedAt time.Time
		if err := rows.Scan(
			&c.ID, &c.CreatorProfileID, &c.Name, &c.Description, &c.CollectionType,
			&c.ItemCount, &c.PosterPath, &c.PosterThumbhash, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning server-visible user collection: %w", err)
		}
		c.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		c.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		out = append(out, c)
	}
	return out, rows.Err()
}
