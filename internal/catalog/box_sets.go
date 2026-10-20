package catalog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/librarykind"
)

// Movie box sets group the movies TMDB lists in one collection (a franchise
// such as Harry Potter) into one browse card. A library opts in with
// box_sets_enabled; a collection forms a box set there once the viewer can
// see at least box_set_min_movies of its movies in that library. Membership
// comes from TMDB and is stored by the box set sync task, so browsing never
// calls TMDB.
const (
	DefaultBoxSetMinMovies = 2
	MinBoxSetMinMovies     = 2
	MaxBoxSetMinMovies     = 10

	// BoxSetContentIDPrefix starts the synthetic content ID of a box set
	// card: box_set:<tmdb collection id>.
	BoxSetContentIDPrefix = "box_set:"
)

// LibraryTypeSupportsBoxSets reports whether a library of this type holds
// movies, which box sets group.
func LibraryTypeSupportsBoxSets(libraryType string) bool {
	return librarykind.IsMovie(libraryType) || librarykind.IsMixed(libraryType)
}

// BoxSetContentID is the synthetic content ID of a box set card.
func BoxSetContentID(collectionID int64) string {
	return BoxSetContentIDPrefix + strconv.FormatInt(collectionID, 10)
}

// TMDBMovieCollection is a TMDB collection as the box set sync stored it.
// Each image's SourcePath is the provider URL and Path is the cached copy's
// original key, or the provider URL while the image is not cached.
type TMDBMovieCollection struct {
	ID                 int64
	Name               string
	Overview           string
	PosterPath         string
	PosterSourcePath   string
	PosterThumbhash    string
	BackdropPath       string
	BackdropSourcePath string
	BackdropThumbhash  string
	// MemberTMDBIDs lists the collection's movies in release order.
	MemberTMDBIDs []int64
	FetchedAt     time.Time
}

// BoxSetGroup is one box set of a library as a viewer sees it: the
// collection's movies the viewer can see there, in collection order.
type BoxSetGroup struct {
	CollectionID     int64
	MemberContentIDs []string
	FirstYear        int
	LastYear         int
}

// BoxSetRepository reads and writes the stored TMDB collections and the
// collection each movie belongs to.
type BoxSetRepository struct {
	pool *pgxpool.Pool
}

// NewBoxSetRepository returns a repository over pool.
func NewBoxSetRepository(pool *pgxpool.Pool) *BoxSetRepository {
	return &BoxSetRepository{pool: pool}
}

// Collections returns the stored collections among ids, keyed by ID.
func (r *BoxSetRepository) Collections(ctx context.Context, ids []int64) (map[int64]TMDBMovieCollection, error) {
	out := make(map[int64]TMDBMovieCollection, len(ids))
	if r == nil || r.pool == nil || len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT tmdb_collection_id, name, overview, poster_path, poster_thumbhash, backdrop_path, backdrop_thumbhash, member_tmdb_ids, fetched_at
		FROM tmdb_movie_collections WHERE tmdb_collection_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("querying tmdb collections: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c TMDBMovieCollection
		if err := rows.Scan(&c.ID, &c.Name, &c.Overview, &c.PosterPath, &c.PosterThumbhash, &c.BackdropPath, &c.BackdropThumbhash, &c.MemberTMDBIDs, &c.FetchedAt); err != nil {
			return nil, fmt.Errorf("scanning tmdb collection: %w", err)
		}
		out[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading tmdb collections: %w", err)
	}
	return out, nil
}

// SearchCollectionIDs returns up to limit stored collections with a movie in
// one of libraryIDs that match query, skipping the first offset: first those
// whose name contains every word of it, then those with at least two movies
// whose titles do, case-insensitively.
func (r *BoxSetRepository) SearchCollectionIDs(ctx context.Context, query string, libraryIDs []int, limit, offset int) ([]int64, error) {
	words := strings.Fields(strings.ToLower(query))
	if r == nil || r.pool == nil || len(words) == 0 || len(libraryIDs) == 0 {
		return nil, nil
	}
	patterns := make([]string, 0, len(words))
	for _, word := range words {
		patterns = append(patterns, "%"+escapePrefixForLike(word)+"%")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT tmdb_collection_id FROM (
			SELECT tmdb_collection_id, 0 AS match_rank, length(name) AS name_length
			FROM tmdb_movie_collections
			WHERE lower(name) LIKE ALL($1::text[])
			UNION ALL
			SELECT m.tmdb_collection_id, 1, 0
			FROM media_item_tmdb_collections m
			JOIN media_items mi ON mi.content_id = m.content_id AND mi.tmdb_id = m.checked_tmdb_id::text
			WHERE m.tmdb_collection_id IS NOT NULL AND lower(mi.title) LIKE ALL($1::text[])
			GROUP BY m.tmdb_collection_id
			HAVING count(*) >= 2
		) matches
		-- Only collections with a movie in the searched libraries, so matches
		-- elsewhere do not use up the candidates a page reads.
		WHERE EXISTS (
			SELECT 1 FROM media_item_tmdb_collections lm
			JOIN media_items li ON li.content_id = lm.content_id AND li.tmdb_id = lm.checked_tmdb_id::text
			JOIN media_item_libraries mil ON mil.content_id = lm.content_id
			WHERE lm.tmdb_collection_id = matches.tmdb_collection_id AND mil.media_folder_id = ANY($4))
		GROUP BY tmdb_collection_id
		ORDER BY min(match_rank), min(name_length), tmdb_collection_id
		LIMIT $2 OFFSET $3`, patterns, limit, offset, libraryIDs)
	if err != nil {
		return nil, fmt.Errorf("searching tmdb collections: %w", err)
	}
	defer rows.Close()
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("reading tmdb collection search: %w", err)
	}
	return ids, nil
}

// CollectionIDsOfContent returns the stored collection of each of the given
// movies that has one.
func (r *BoxSetRepository) CollectionIDsOfContent(ctx context.Context, contentIDs []string) (map[string]int64, error) {
	out := map[string]int64{}
	if r == nil || r.pool == nil || len(contentIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT m.content_id, m.tmdb_collection_id
		FROM media_item_tmdb_collections m
		JOIN media_items mi ON mi.content_id = m.content_id AND mi.tmdb_id = m.checked_tmdb_id::text
		WHERE m.content_id = ANY($1) AND m.tmdb_collection_id IS NOT NULL`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("querying movie collections: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var contentID string
		var collectionID int64
		if err := rows.Scan(&contentID, &collectionID); err != nil {
			return nil, fmt.Errorf("scanning movie collection: %w", err)
		}
		out[contentID] = collectionID
	}
	return out, rows.Err()
}

// MovieCollectionLookup is a movie whose TMDB collection needs looking up.
type MovieCollectionLookup struct {
	ContentID string
	TMDBID    int64
}

// MoviesNeedingCollectionLookup returns up to limit movies of box set
// libraries that were never looked up, were rematched to another TMDB movie
// since, or were last looked up before staleBefore.
func (r *BoxSetRepository) MoviesNeedingCollectionLookup(ctx context.Context, staleBefore time.Time, limit int, skip []string) ([]MovieCollectionLookup, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT mi.content_id, mi.tmdb_id::bigint
		FROM media_items mi
		LEFT JOIN media_item_tmdb_collections m ON m.content_id = mi.content_id
		WHERE mi.type = 'movie'
		  -- Only an ID in plain form: the recorded ID is matched back as
		  -- text, so '0603' would never count as looked up and stay due.
		  AND mi.tmdb_id ~ '^[1-9][0-9]{0,17}$'
		  AND EXISTS (
			SELECT 1 FROM media_item_libraries mil
			JOIN media_folders f ON f.id = mil.media_folder_id
			WHERE mil.content_id = mi.content_id AND f.box_sets_enabled AND f.enabled IS NOT FALSE)
		  AND (m.content_id IS NULL OR m.checked_tmdb_id::text <> mi.tmdb_id OR m.checked_at < $1)
		  AND NOT (mi.content_id = ANY($3::text[]))
		ORDER BY m.checked_at NULLS FIRST, mi.content_id
		LIMIT $2`, staleBefore, limit, nonNilSlice(skip))
	if err != nil {
		return nil, fmt.Errorf("querying movies needing a collection lookup: %w", err)
	}
	defer rows.Close()
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (MovieCollectionLookup, error) {
		var l MovieCollectionLookup
		err := row.Scan(&l.ContentID, &l.TMDBID)
		return l, err
	})
	if err != nil {
		return nil, fmt.Errorf("reading movies needing a collection lookup: %w", err)
	}
	return out, nil
}

// RecordMovieCollection stores the outcome of looking up one movie:
// collectionID nil means the movie is in no collection.
func (r *BoxSetRepository) RecordMovieCollection(ctx context.Context, contentID string, tmdbID int64, collectionID *int64) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO media_item_tmdb_collections (content_id, checked_tmdb_id, tmdb_collection_id, checked_at)
		SELECT $1, $2, $3, now() WHERE EXISTS (SELECT 1 FROM media_items WHERE content_id = $1)
		ON CONFLICT (content_id) DO UPDATE SET
			checked_tmdb_id = EXCLUDED.checked_tmdb_id,
			tmdb_collection_id = EXCLUDED.tmdb_collection_id,
			checked_at = EXCLUDED.checked_at`, contentID, tmdbID, collectionID)
	if err != nil {
		return fmt.Errorf("recording movie collection: %w", err)
	}
	return nil
}

// CollectionsNeedingFetch returns up to limit collections some movie belongs
// to that are not stored yet or were fetched before staleBefore.
func (r *BoxSetRepository) CollectionsNeedingFetch(ctx context.Context, staleBefore time.Time, limit int, skip []int64) ([]int64, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT m.tmdb_collection_id, c.fetched_at
		FROM media_item_tmdb_collections m
		LEFT JOIN tmdb_movie_collections c ON c.tmdb_collection_id = m.tmdb_collection_id
		WHERE m.tmdb_collection_id IS NOT NULL AND (c.tmdb_collection_id IS NULL OR c.fetched_at < $1)
		  AND NOT (m.tmdb_collection_id = ANY($3::bigint[]))
		ORDER BY c.fetched_at NULLS FIRST, m.tmdb_collection_id
		LIMIT $2`, staleBefore, limit, nonNilSlice(skip))
	if err != nil {
		return nil, fmt.Errorf("querying collections needing a fetch: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var fetchedAt *time.Time
		if err := rows.Scan(&id, &fetchedAt); err != nil {
			return nil, fmt.Errorf("scanning collection needing a fetch: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpsertCollection stores a fetched collection with the provider URLs of its
// artwork in PosterSourcePath and BackdropSourcePath. An image whose source is
// unchanged keeps its cached copy; a new source is served from the provider
// until CacheCollectionArtwork stores it.
func (r *BoxSetRepository) UpsertCollection(ctx context.Context, c TMDBMovieCollection) error {
	members := c.MemberTMDBIDs
	if members == nil {
		members = []int64{}
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO tmdb_movie_collections (tmdb_collection_id, name, overview, poster_path, poster_source_path, backdrop_path, backdrop_source_path, member_tmdb_ids, fetched_at)
		VALUES ($1, $2, $3, $4, $4, $5, $5, $6, now())
		ON CONFLICT (tmdb_collection_id) DO UPDATE SET
			name = EXCLUDED.name,
			overview = EXCLUDED.overview,
			poster_path = CASE WHEN tmdb_movie_collections.poster_source_path = EXCLUDED.poster_source_path
				THEN tmdb_movie_collections.poster_path ELSE EXCLUDED.poster_path END,
			poster_thumbhash = CASE WHEN tmdb_movie_collections.poster_source_path = EXCLUDED.poster_source_path
				THEN tmdb_movie_collections.poster_thumbhash ELSE '' END,
			poster_source_path = EXCLUDED.poster_source_path,
			backdrop_path = CASE WHEN tmdb_movie_collections.backdrop_source_path = EXCLUDED.backdrop_source_path
				THEN tmdb_movie_collections.backdrop_path ELSE EXCLUDED.backdrop_path END,
			backdrop_thumbhash = CASE WHEN tmdb_movie_collections.backdrop_source_path = EXCLUDED.backdrop_source_path
				THEN tmdb_movie_collections.backdrop_thumbhash ELSE '' END,
			backdrop_source_path = EXCLUDED.backdrop_source_path,
			member_tmdb_ids = EXCLUDED.member_tmdb_ids,
			fetched_at = EXCLUDED.fetched_at,
			updated_at = now()`,
		c.ID, c.Name, c.Overview, c.PosterSourcePath, c.BackdropSourcePath, members)
	if err != nil {
		return fmt.Errorf("storing tmdb collection: %w", err)
	}
	return nil
}

// CollectionArtworkSlot names one cached image of a collection.
type CollectionArtworkSlot string

const (
	BoxSetPosterSlot   CollectionArtworkSlot = "poster"
	BoxSetBackdropSlot CollectionArtworkSlot = "backdrop"
)

// CollectionArtworkDue is one collection image served from its provider URL.
type CollectionArtworkDue struct {
	CollectionID int64
	Slot         CollectionArtworkSlot
	SourcePath   string
}

// CollectionArtworkNeedingCache returns up to limit collection images that are
// served from their provider URL rather than a cached copy.
func (r *BoxSetRepository) CollectionArtworkNeedingCache(ctx context.Context, limit int, skip []int64) ([]CollectionArtworkDue, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT tmdb_collection_id, slot, source FROM (
			SELECT tmdb_collection_id, 'poster' AS slot, poster_source_path AS source
			FROM tmdb_movie_collections WHERE poster_source_path <> '' AND poster_path = poster_source_path
			UNION ALL
			SELECT tmdb_collection_id, 'backdrop', backdrop_source_path
			FROM tmdb_movie_collections WHERE backdrop_source_path <> '' AND backdrop_path = backdrop_source_path
		) due
		WHERE NOT (tmdb_collection_id = ANY($2::bigint[]))
		ORDER BY tmdb_collection_id, slot
		LIMIT $1`, limit, nonNilSlice(skip))
	if err != nil {
		return nil, fmt.Errorf("querying collection artwork to cache: %w", err)
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (CollectionArtworkDue, error) {
		var d CollectionArtworkDue
		var slot string
		err := row.Scan(&d.CollectionID, &slot, &d.SourcePath)
		d.Slot = CollectionArtworkSlot(slot)
		return d, err
	})
}

// SetCollectionArtwork records the cached copy of a collection image, unless
// the image's source changed while it was being cached.
func (r *BoxSetRepository) SetCollectionArtwork(ctx context.Context, id int64, slot CollectionArtworkSlot, sourcePath, path, thumbhash string) error {
	var sql string
	switch slot {
	case BoxSetPosterSlot:
		sql = `UPDATE tmdb_movie_collections SET poster_path = $3, poster_thumbhash = $4, updated_at = now()
			WHERE tmdb_collection_id = $1 AND poster_source_path = $2`
	case BoxSetBackdropSlot:
		sql = `UPDATE tmdb_movie_collections SET backdrop_path = $3, backdrop_thumbhash = $4, updated_at = now()
			WHERE tmdb_collection_id = $1 AND backdrop_source_path = $2`
	default:
		return fmt.Errorf("unknown collection artwork slot %q", slot)
	}
	if _, err := r.pool.Exec(ctx, sql, id, sourcePath, path, thumbhash); err != nil {
		return fmt.Errorf("recording cached collection artwork: %w", err)
	}
	return nil
}

// TouchCollection marks a collection fetched without changing it, so one
// TMDB no longer serves is not fetched again on every run.
func (r *BoxSetRepository) TouchCollection(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `UPDATE tmdb_movie_collections SET fetched_at = now() WHERE tmdb_collection_id = $1`, id)
	if err != nil {
		return fmt.Errorf("touching tmdb collection: %w", err)
	}
	return nil
}

// BoxSetLibrary is a library's box set settings.
type BoxSetLibrary struct {
	ID        int
	Type      string
	MinMovies int
	InSearch  bool
}

// BoxSetLibraries returns the enabled libraries with box sets on.
func (r *BoxSetRepository) BoxSetLibraries(ctx context.Context) ([]BoxSetLibrary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, type, box_set_min_movies, box_sets_in_search
		FROM media_folders WHERE box_sets_enabled AND enabled IS NOT FALSE ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("querying box set libraries: %w", err)
	}
	defer rows.Close()
	var out []BoxSetLibrary
	for rows.Next() {
		var l BoxSetLibrary
		if err := rows.Scan(&l.ID, &l.Type, &l.MinMovies, &l.InSearch); err != nil {
			return nil, fmt.Errorf("scanning box set library: %w", err)
		}
		if LibraryTypeSupportsBoxSets(l.Type) {
			out = append(out, l)
		}
	}
	return out, rows.Err()
}

// LibraryBoxSetMinMovies returns the minimum movie count of a library with
// box sets on, or ok=false when the library has them off or does not exist.
func (r *BoxSetRepository) LibraryBoxSetMinMovies(ctx context.Context, libraryID int) (minMovies int, ok bool, err error) {
	var libraryType string
	err = r.pool.QueryRow(ctx, `
		SELECT type, box_set_min_movies FROM media_folders
		WHERE id = $1 AND box_sets_enabled AND enabled IS NOT FALSE`, libraryID).Scan(&libraryType, &minMovies)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("querying library box set settings: %w", err)
	}
	if !LibraryTypeSupportsBoxSets(libraryType) {
		return 0, false, nil
	}
	return max(minMovies, MinBoxSetMinMovies), true, nil
}

// BoxSetGroups returns the box sets of one library as the viewer sees them:
// every stored collection with at least minMovies of its movies among the
// library movies access lets the viewer see. onlyCollections, when not nil,
// narrows the result to those collections.
func (r *CatalogResolver) BoxSetGroups(ctx context.Context, libraryID int, minMovies int, access AccessFilter, onlyCollections []int64) ([]BoxSetGroup, error) {
	if r == nil || r.itemRepo == nil {
		return nil, errors.New("catalog resolver requires an item repository")
	}
	def := QueryDefinition{MediaScope: playableTypeMovie, LibraryIDs: []int{libraryID}}
	executor := r.queryExecutorForScope(def.MediaScope, nil)
	return executor.boxSetGroups(ctx, def, access, minMovies, onlyCollections)
}

func (e *QueryExecutor) boxSetGroups(ctx context.Context, def QueryDefinition, access AccessFilter, minMovies int, onlyCollections []int64) ([]BoxSetGroup, error) {
	if e == nil || e.Pool == nil {
		return nil, errors.New("query executor requires a database pool")
	}
	access.NamePrefix = ""
	plan, err := e.buildPreviewPagePlan(def, access, 1, 0)
	if err != nil {
		return nil, err
	}
	args := append([]any{}, plan.cteArgs...)
	args = append(args, plan.args...)
	args = append(args, max(minMovies, MinBoxSetMinMovies))
	minIdx := len(args)
	only := ""
	if onlyCollections != nil {
		args = append(args, onlyCollections)
		only = fmt.Sprintf(" AND bsm.tmdb_collection_id = ANY($%d)", len(args))
	}
	where := plan.whereClause
	if where == "" {
		where = "WHERE TRUE"
	}
	sql := fmt.Sprintf(`%sSELECT bsm.tmdb_collection_id,
			array_agg(mi.content_id ORDER BY array_position(c.member_tmdb_ids, bsm.checked_tmdb_id) NULLS LAST, mi.year NULLS LAST, mi.content_id),
			COALESCE(min(mi.year), 0), COALESCE(max(mi.year), 0)
		%s
		JOIN media_item_tmdb_collections bsm ON bsm.content_id = mi.content_id AND bsm.checked_tmdb_id::text = mi.tmdb_id
		JOIN tmdb_movie_collections c ON c.tmdb_collection_id = bsm.tmdb_collection_id
		%s AND bsm.tmdb_collection_id IS NOT NULL%s
		GROUP BY bsm.tmdb_collection_id
		HAVING count(*) >= $%d
		ORDER BY bsm.tmdb_collection_id`,
		plan.withClause(), plan.fromClauseCount, where, only, minIdx)
	rows, err := e.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("querying box sets: %w", err)
	}
	defer rows.Close()
	var groups []BoxSetGroup
	for rows.Next() {
		var g BoxSetGroup
		if err := rows.Scan(&g.CollectionID, &g.MemberContentIDs, &g.FirstYear, &g.LastYear); err != nil {
			return nil, fmt.Errorf("scanning box set: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading box sets: %w", err)
	}
	return groups, nil
}

// nonNilSlice binds an absent skip list as an empty array: ANY of NULL is
// NULL, which would exclude every row.
func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
