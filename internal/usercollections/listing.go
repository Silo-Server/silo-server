package usercollections

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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

// serverVisibleWhere is the ownership + opt-in + profile-ACL predicate that
// gates every server-visible read. Personal collections are private, so this
// privacy boundary is defined once and shared by all readers: $1 is the owning
// user and $2 the viewing profile.
const serverVisibleWhere = `upc.user_id = $1
	AND upc.include_in_server_collections = TRUE
	AND EXISTS (
		SELECT 1 FROM user_personal_collection_profiles vp
		WHERE vp.user_id = upc.user_id
		  AND vp.collection_id = upc.id
		  AND vp.profile_id = $2
	)`

// scopeConfigExpr picks the JSON object that carries a collection's library
// scope: a smart collection scopes through its query, an imported one through
// its source_config, and older rows fall back to the query definition.
const scopeConfigExpr = `CASE
		WHEN upc.collection_type = 'smart' THEN upc.query_definition
		WHEN upc.source_config ? 'library_ids' THEN upc.source_config
		ELSE upc.query_definition
	END`

// scopeMatchesLibraries is the library-overlap predicate over a scope_config
// column. A row with no library_ids, or an empty list, is library-agnostic and
// matches every set.
func scopeMatchesLibraries(scopeColumn, libraryIDsParam string) string {
	return `(
		NOT (` + scopeColumn + ` ? 'library_ids')
		OR jsonb_array_length(COALESCE(` + scopeColumn + `->'library_ids', '[]'::jsonb)) = 0
		OR EXISTS (
			SELECT 1
			FROM unnest(` + libraryIDsParam + `::int[]) library_id
			WHERE ` + scopeColumn + `->'library_ids' @> to_jsonb(library_id)
		)
	)`
}

// ListServerVisibleByLibrary returns the current user's personal collections
// that have opted into their library Collections tab and whose library scope
// matches the requested library. Imported exact collections read library_ids
// from source_config, while legacy rows can fall back to query_definition. A
// collection with no library_ids is treated as library-agnostic and therefore
// visible in every library tab. Other users' collections are never returned.
func ListServerVisibleByLibrary(ctx context.Context, pool *pgxpool.Pool, userID int, profileID string, libraryID int) ([]ServerVisibleCollection, error) {
	return listServerVisible(ctx, pool, userID, profileID, []int{libraryID}, false)
}

// listServerVisible backs native single-library and Jellyfin visible-library
// reads. Scoped collections must overlap visibleLibraryIDs; unscoped rows stay
// visible. videoOnly excludes unified audiobook/podcast playlists.
func listServerVisible(ctx context.Context, pool *pgxpool.Pool, userID int, profileID string, visibleLibraryIDs []int, videoOnly bool) ([]ServerVisibleCollection, error) {
	rows, err := pool.Query(ctx,
		`WITH visible_collections AS (
			SELECT upc.id, upc.creator_profile_id, upc.name, upc.description, upc.collection_type,
			       upc.item_count, upc.poster_url, upc.poster_thumbhash, upc.created_at, upc.updated_at,
			       `+scopeConfigExpr+` AS scope_config
			FROM user_personal_collections upc
			WHERE `+serverVisibleWhere+`
		)
		 SELECT id, creator_profile_id, name, description, collection_type, item_count,
		        poster_url, poster_thumbhash, created_at, updated_at
		 FROM visible_collections
		 WHERE `+scopeMatchesLibraries("scope_config", "$3")+`
		   AND (NOT $4::boolean OR collection_type <> 'playlist')
		 ORDER BY name ASC
		 LIMIT $5`,
		userID, profileID, visibleLibraryIDs, videoOnly, serverVisibleListLimit,
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

// Store reads a user's server-visible personal collections, mirroring
// catalog.LibraryCollectionRepository for the admin side. Callers pass the
// owning user and the viewing profile on every read; nothing here can reach
// another user's rows.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// List returns video collections visible to profileID and overlapping the
// Jellyfin-visible library set. See ListServerVisibleByLibrary.
func (s *Store) List(ctx context.Context, userID int, profileID string, visibleLibraryIDs []int) ([]ServerVisibleCollection, error) {
	return listServerVisible(ctx, s.pool, userID, profileID, visibleLibraryIDs, true)
}

// Get returns one server-visible video collection by its Jellyfin lookup key,
// intersected with the viewer's Jellyfin-visible libraries. Returns (nil, nil) for missing,
// unauthorized, hidden-scope or non-opted-in rows so callers cannot distinguish
// those cases.
func (s *Store) Get(ctx context.Context, userID int, profileID, key string, visibleLibraryIDs []int) (*ServerVisibleCollection, error) {
	var (
		c                    ServerVisibleCollection
		createdAt, updatedAt time.Time
	)
	err := s.pool.QueryRow(ctx,
		`WITH visible_collection AS (
			SELECT upc.id, upc.creator_profile_id, upc.name, upc.description, upc.collection_type,
			       upc.item_count, upc.poster_url, upc.poster_thumbhash, upc.created_at, upc.updated_at,
			       `+scopeConfigExpr+` AS scope_config
			FROM user_personal_collections upc
			WHERE `+serverVisibleWhere+`
			  AND jellycompat_user_collection_key(upc.id) = $3
			  AND upc.collection_type <> 'playlist'
		)
		 SELECT id, creator_profile_id, name, description, collection_type, item_count,
		        poster_url, poster_thumbhash, created_at, updated_at
		 FROM visible_collection
		 WHERE `+scopeMatchesLibraries("scope_config", "$4"),
		userID, profileID, key, visibleLibraryIDs,
	).Scan(
		&c.ID, &c.CreatorProfileID, &c.Name, &c.Description, &c.CollectionType,
		&c.ItemCount, &c.PosterPath, &c.PosterThumbhash, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("loading server-visible user collection: %w", err)
	}
	c.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	c.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return &c, nil
}

// AnyVisible reports whether the profile has at least one server-visible
// collection. An EXISTS probe rather than a list, because it gates
// a surface (the Collections view) that is rebuilt on every client refresh.
func (s *Store) AnyVisible(ctx context.Context, userID int, profileID string, visibleLibraryIDs []int) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
			WITH visible_collections AS (
				SELECT `+scopeConfigExpr+` AS scope_config
				FROM user_personal_collections upc
				WHERE `+serverVisibleWhere+`
				  AND upc.collection_type <> 'playlist'
			)
			 SELECT 1 FROM visible_collections
			 WHERE `+scopeMatchesLibraries("scope_config", "$3")+`
		)`,
		userID, profileID, visibleLibraryIDs,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("probing server-visible user collections: %w", err)
	}
	return exists, nil
}

// ImageCandidates returns opted-in personal collection metadata by Jellyfin lookup key.
//
// This is the one read that deliberately skips serverVisibleWhere: it has no
// owning user or viewing profile to scope by. Jellyfin clients load artwork
// from plain <img> elements, which carry no token, so a poster can only be
// authorized by the signed tag minted when its owner listed the collection.
// Rows can share an ID across users (the primary key is user_id + id), hence a
// list: the caller picks the row whose artwork reproduces that signature.
//
// Callers MUST verify the signature before serving bytes, and MUST NOT use this
// for a browse read — it can return another user's collection.
// TestServeCollectionImage_PersonalCollectionOwnerOnly pins both halves.
func (s *Store) ImageCandidates(ctx context.Context, key string) ([]ServerVisibleCollection, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, creator_profile_id, name, description, collection_type, item_count,
		        poster_url, poster_thumbhash, created_at, updated_at
		 FROM user_personal_collections
		 WHERE jellycompat_user_collection_key(id) = $1
		   AND include_in_server_collections = TRUE AND collection_type <> 'playlist'`, key)
	if err != nil {
		return nil, fmt.Errorf("loading signed-image collection candidates: %w", err)
	}
	defer rows.Close()
	var out []ServerVisibleCollection
	for rows.Next() {
		var c ServerVisibleCollection
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&c.ID, &c.CreatorProfileID, &c.Name, &c.Description, &c.CollectionType,
			&c.ItemCount, &c.PosterPath, &c.PosterThumbhash, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning signed-image collection candidate: %w", err)
		}
		c.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		c.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		out = append(out, c)
	}
	return out, rows.Err()
}
