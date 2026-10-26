package usercollections

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ServerVisibleCollection is a personal collection opted into the library
// Collections tab, as one profile sees it: its own, or another profile's on
// the same login that is shared. This list never crosses logins.
type ServerVisibleCollection struct {
	ID               string `json:"id"`
	CreatorProfileID string `json:"creator_profile_id"`
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	CollectionType   string `json:"collection_type"`
	ItemCount        int    `json:"item_count"`
	QueryDefinition  string `json:"-"`
	// DisplayQueryDefinition is the collection's display filter, "" when none.
	DisplayQueryDefinition string `json:"-"`
	PosterPath             string `json:"-"`
	PosterURL              string `json:"poster_url,omitempty"`
	PosterThumbhash        string `json:"poster_thumbhash,omitempty"`
	// PosterIsCollage reports that PosterPath is the viewer's collage. The
	// frozen /api/v1 shape does not carry it.
	PosterIsCollage bool   `json:"-"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// serverVisibleListLimit caps how many opt-in collections a single library tab
// will surface so a user with hundreds of published collections can't blow up
// the response.
const serverVisibleListLimit = 500

// ListServerVisibleByLibrary returns the personal collections profileID may
// see (its own, and other profiles' shared collections on the login) that are
// opted into the library Collections tab and whose library scope matches the
// requested library. Imported exact collections read library_ids from
// source_config, while legacy rows can fall back to query_definition. A
// collection with no library_ids is treated as library-agnostic and therefore
// visible in every library tab. Other logins' collections and Audiobookshelf
// rows are never returned.
func ListServerVisibleByLibrary(ctx context.Context, pool *pgxpool.Pool, userID int, profileID string, libraryID int) ([]ServerVisibleCollection, error) {
	rows, err := pool.Query(ctx,
		`WITH visible_collections AS (
			SELECT upc.id, upc.creator_profile_id, upc.name, upc.description, upc.collection_type,
			       upc.item_count, upc.query_definition, COALESCE(upc.display_query_definition::text, '') AS display_query_definition, upc.poster_url, upc.poster_thumbhash, upc.created_at, upc.updated_at,
			       CASE
			         WHEN upc.collection_type = 'smart' THEN upc.query_definition
			         WHEN upc.source_config ? 'library_ids' THEN upc.source_config
			         ELSE upc.query_definition
			       END AS scope_config
			FROM user_personal_collections upc
			WHERE upc.user_id = $1
			  AND upc.native
			  AND upc.include_in_server_collections = TRUE
			  AND (upc.creator_profile_id = $2 OR upc.is_shared)
		)
		 SELECT id, creator_profile_id, name, description, collection_type, item_count,
		        query_definition, display_query_definition, poster_url, poster_thumbhash, created_at, updated_at
		 FROM visible_collections
		 WHERE TRUE
		   AND (
		     NOT (scope_config ? 'library_ids')
		     OR jsonb_array_length(COALESCE(scope_config->'library_ids', '[]'::jsonb)) = 0
		     OR scope_config->'library_ids' @> to_jsonb($3::bigint)
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
			&c.ItemCount, &c.QueryDefinition, &c.DisplayQueryDefinition, &c.PosterPath, &c.PosterThumbhash, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning server-visible user collection: %w", err)
		}
		c.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		c.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		out = append(out, c)
	}
	return out, rows.Err()
}
