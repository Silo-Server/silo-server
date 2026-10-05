package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A personal collection without an uploaded or imported poster shows a
// collage of its first titles' posters, as a server collection does
// (collection_collages.go). Its titles come from the Postgres user store:
// a hand-picked or imported collection's members in their stored order, or a
// smart collection's first matches in its query order. Callers pass the
// filter the viewer reads the collection with (PersonalCollectionFilter), so
// another profile's shared collection shows only titles both its owner and
// the viewer can see. The collection's display filter does not narrow its
// collage. Collections kept in a per-user SQLite store have no collage.

// ErrPersonalCollectionNotFound reports a personal collection that is gone.
var ErrPersonalCollectionNotFound = errors.New("personal collection not found")

const (
	// smartCollageScanLimit is how many of a smart collection's first matches
	// are read to find its posters.
	smartCollageScanLimit = 3 * CollectionCollageSourceLimit
	// defaultPersonalCollageRefreshDelay coalesces a burst of changes to one
	// collection, such as titles added one at a time, into one build.
	defaultPersonalCollageRefreshDelay = 2 * time.Second
)

// PersonalCollectionCollages serves and builds personal collection collages.
// A nil receiver, or one without a CollageGen, serves and builds nothing.
type PersonalCollectionCollages struct {
	pool *pgxpool.Pool
	// CollageGen composes and stores collages; nil when artwork storage is
	// not configured.
	CollageGen CollageGenerator
	// RefreshDelay is how long Refresh waits before reading a collection's
	// titles, so later changes in a burst join the same refresh.
	RefreshDelay time.Duration

	queueOnce sync.Once
	queue     *collageBuildQueue

	refreshMu sync.Mutex
	refreshes map[string]personalCollageRefresh
}

// personalCollageRefresh is a pending Refresh of one collection: the latest
// definition and filter it was asked for.
type personalCollageRefresh struct {
	userID     int
	collection PersonalCollectionDefinition
	access     AccessFilter
}

// NewPersonalCollectionCollages serves the collages of the personal
// collections in pool's user store, composed and stored by gen.
func NewPersonalCollectionCollages(pool *pgxpool.Pool, gen CollageGenerator) *PersonalCollectionCollages {
	return &PersonalCollectionCollages{pool: pool, CollageGen: gen, RefreshDelay: defaultPersonalCollageRefreshDelay}
}

func (p *PersonalCollectionCollages) enabled() bool {
	return p != nil && p.pool != nil && p.CollageGen != nil
}

func (p *PersonalCollectionCollages) collageSet(userID int) collageSet {
	p.queueOnce.Do(func() { p.queue = newCollageBuildQueue() })
	return collageSet{store: personalCollageStore{pool: p.pool, userID: userID}, gen: p.CollageGen, queue: p.queue}
}

// Posters returns the collage each of account userID's collections shows the
// viewer described by access, keyed by collection ID. Pass only collections
// without an uploaded or imported poster. A collage not built yet is left out
// and built in the background; collections with no title the viewer can see
// that has a poster are absent.
func (p *PersonalCollectionCollages) Posters(ctx context.Context, userID int, collections []PersonalCollectionDefinition, access AccessFilter) map[string]CollectionPoster {
	if !p.enabled() || len(collections) == 0 {
		return map[string]CollectionPoster{}
	}
	sources, err := p.ListSources(ctx, userID, collections, access)
	if err != nil {
		// A failed definition is only absent from sources; serve the rest.
		slog.WarnContext(ctx, "collage: failed to select personal collection collages", "component", "catalog", "error", err)
	}
	ids := make([]string, 0, len(collections))
	for _, c := range collections {
		ids = append(ids, c.ID)
	}
	return p.collageSet(userID).serve(ctx, ids, sources)
}

// Prepare builds the collage the viewer described by access sees for c,
// unless it is already stored. It returns collage.ErrNotEnoughImages when that
// viewer can see none of c's titles with a poster.
func (p *PersonalCollectionCollages) Prepare(ctx context.Context, userID int, c PersonalCollectionDefinition, access AccessFilter) error {
	if !p.enabled() {
		return nil
	}
	sources, err := p.ListSources(ctx, userID, []PersonalCollectionDefinition{c}, access)
	if err != nil {
		return err
	}
	return p.collageSet(userID).prepare(ctx, c.ID, sources[c.ID])
}

// Refresh builds, in the background and after RefreshDelay, the collage the
// viewer described by access sees for c, unless it is already stored. Call it
// when c's titles or definition change, or its poster is removed, so the
// collage is ready before the next read; a read builds a missing collage
// anyway. Refreshes of one collection within the delay share one build, made
// from the latest definition and filter.
func (p *PersonalCollectionCollages) Refresh(userID int, c PersonalCollectionDefinition, access AccessFilter) {
	if !p.enabled() {
		return
	}
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	if p.refreshes == nil {
		p.refreshes = make(map[string]personalCollageRefresh)
	}
	_, pending := p.refreshes[c.ID]
	p.refreshes[c.ID] = personalCollageRefresh{userID: userID, collection: c, access: access}
	if pending {
		return
	}
	time.AfterFunc(p.RefreshDelay, func() { p.runRefresh(c.ID) })
}

func (p *PersonalCollectionCollages) runRefresh(collectionID string) {
	p.refreshMu.Lock()
	r := p.refreshes[collectionID]
	delete(p.refreshes, collectionID)
	p.refreshMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), collageBuildTimeout)
	defer cancel()
	// Posters builds a missing collage through the build queue.
	p.Posters(ctx, r.userID, []PersonalCollectionDefinition{r.collection}, r.access)
}

// ListSources returns, for each of account userID's collections, the poster
// paths of up to CollectionCollageSourceLimit of its titles the viewer
// described by access can see, in the collection's order. Collections with no
// such title are absent. A smart definition that fails is absent too, and its
// error is returned beside the other collections' sources.
func (p *PersonalCollectionCollages) ListSources(ctx context.Context, userID int, collections []PersonalCollectionDefinition, access AccessFilter) (map[string][]string, error) {
	sources := make(map[string][]string, len(collections))
	if p == nil || p.pool == nil || len(collections) == 0 || (access.AllowedLibraryIDs != nil && len(access.AllowedLibraryIDs) == 0) {
		return sources, nil
	}
	var memberIDs []string
	var smart []PersonalCollectionDefinition
	for _, c := range collections {
		if IsLiveQueryType(c.CollectionType) {
			smart = append(smart, c)
		} else {
			memberIDs = append(memberIDs, c.ID)
		}
	}
	if len(memberIDs) > 0 {
		if err := p.listMemberSources(ctx, userID, memberIDs, access, sources); err != nil {
			return sources, err
		}
	}
	// Identical definitions match the same titles; read each once.
	byDefinition := make(map[string][]string, len(smart))
	var failures []error
	for _, c := range smart {
		src, seen := byDefinition[c.QueryDefinition]
		if !seen {
			var err error
			src, err = p.smartSources(ctx, c.QueryDefinition, access)
			if err != nil {
				failures = append(failures, fmt.Errorf("collection %s: %w", c.ID, err))
				continue
			}
			byDefinition[c.QueryDefinition] = src
		}
		if len(src) > 0 {
			sources[c.ID] = src
		}
	}
	return sources, errors.Join(failures...)
}

// listMemberSources adds the sources of hand-picked and imported collections,
// read from their stored members with the predicates their catalog view and
// item counts apply (CountVisiblePersonalCollectionMembers).
func (p *PersonalCollectionCollages) listMemberSources(ctx context.Context, userID int, collectionIDs []string, access AccessFilter, sources map[string][]string) error {
	ids := slices.Clone(collectionIDs)
	slices.Sort(ids)
	sql, args := buildPersonalCollectionCollageSourcesSQL(userID, slices.Compact(ids), access)
	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("listing personal collection collage sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return fmt.Errorf("scanning personal collection collage source: %w", err)
		}
		sources[id] = append(sources[id], path)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating personal collection collage sources: %w", err)
	}
	return nil
}

func buildPersonalCollectionCollageSourcesSQL(userID int, collectionIDs []string, access AccessFilter) (string, []any) {
	args := []any{userID, collectionIDs, CollectionCollageSourceLimit}
	argIdx := 4
	var memberWhere strings.Builder
	for _, condition := range itemAccessConditions(access, &args, &argIdx) {
		memberWhere.WriteString("\n\t\t\t  AND " + condition)
	}
	return `
		SELECT c.id, src.poster_path
		FROM unnest($2::text[]) WITH ORDINALITY AS c(id, ord)
		CROSS JOIN LATERAL (
			SELECT mi.poster_path, upci.position, upci.media_item_id
			FROM user_personal_collection_items upci
			JOIN media_items mi ON mi.content_id = upci.media_item_id
			WHERE upci.user_id = $1
			  AND upci.collection_id = c.id
			  AND upci.sub_item_id = ''
			  AND mi.poster_path <> ''
			  AND ` + MangaChapterExclusionWhere("mi") + memberWhere.String() + `
			ORDER BY upci.position, upci.media_item_id
			LIMIT $3
		) src
		ORDER BY c.ord, src.position, src.media_item_id`, args
}

// smartSources reads a smart collection's first matches as its catalog view
// lists them and keeps the posters of the first ones that have one.
func (p *PersonalCollectionCollages) smartSources(ctx context.Context, queryDefinition string, access AccessFilter) ([]string, error) {
	var def QueryDefinition
	if err := json.Unmarshal([]byte(queryDefinition), &def); err != nil {
		return nil, fmt.Errorf("parsing smart collection query: %w", err)
	}
	def = ApplySmartCollectionItemLimit(def.Normalize())
	if err := def.ValidateWithOptions(true, true); err != nil {
		return nil, err
	}
	items, _, _, err := (&QueryExecutor{Pool: p.pool}).PreviewPage(ctx, def, access, smartCollageScanLimit, 0, false)
	if err != nil {
		return nil, fmt.Errorf("reading smart collection matches: %w", err)
	}
	var sources []string
	for _, item := range items {
		if item.PosterPath != "" {
			sources = append(sources, item.PosterPath)
			if len(sources) == CollectionCollageSourceLimit {
				break
			}
		}
	}
	return sources, nil
}

// personalCollageStore keeps account userID's personal collection collages
// in user_personal_collection_poster_variants.
type personalCollageStore struct {
	pool   *pgxpool.Pool
	userID int
}

func (s personalCollageStore) GetCollectionCollages(ctx context.Context, refs []CollectionCollageRef) (map[CollectionCollageRef]CollectionCollage, error) {
	collages := make(map[CollectionCollageRef]CollectionCollage, len(refs))
	if len(refs) == 0 {
		return collages, nil
	}
	ids, keys := splitCollectionCollageRefs(refs)
	rows, err := s.pool.Query(ctx, `
		SELECT v.collection_id, v.variant_key, v.poster_path, v.poster_thumbhash, v.last_used_at
		FROM unnest($2::text[], $3::text[]) AS ref(collection_id, variant_key)
		JOIN user_personal_collection_poster_variants v
		  ON v.user_id = $1 AND v.collection_id = ref.collection_id AND v.variant_key = ref.variant_key
	`, s.userID, ids, keys)
	if err != nil {
		return nil, fmt.Errorf("loading personal collection collages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c CollectionCollage
		if err := rows.Scan(&c.CollectionID, &c.Key, &c.Path, &c.Thumbhash, &c.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scanning personal collection collage: %w", err)
		}
		collages[c.CollectionCollageRef] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating personal collection collages: %w", err)
	}
	return collages, nil
}

func (s personalCollageStore) TouchCollectionCollages(ctx context.Context, refs []CollectionCollageRef) error {
	if len(refs) == 0 {
		return nil
	}
	ids, keys := splitCollectionCollageRefs(refs)
	if _, err := s.pool.Exec(ctx, `
		UPDATE user_personal_collection_poster_variants v
		SET last_used_at = NOW()
		FROM unnest($2::text[], $3::text[]) AS ref(collection_id, variant_key)
		WHERE v.user_id = $1 AND v.collection_id = ref.collection_id AND v.variant_key = ref.variant_key
	`, s.userID, ids, keys); err != nil {
		return fmt.Errorf("touching personal collection collages: %w", err)
	}
	return nil
}

// SaveCollectionCollage stores a built collage, replacing one with the same
// key, and in the same transaction releases its path's reservation. It
// returns ErrPersonalCollectionNotFound when the collection is gone, leaving
// the reservation for the collector.
func (s personalCollageStore) SaveCollectionCollage(ctx context.Context, c CollectionCollage) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning personal collection collage save: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		INSERT INTO user_personal_collection_poster_variants (user_id, collection_id, variant_key, poster_path, poster_thumbhash)
		SELECT $1, $2, $3, $4, $5
		WHERE EXISTS (SELECT 1 FROM user_personal_collections WHERE user_id = $1 AND id = $2)
		ON CONFLICT (user_id, collection_id, variant_key) DO UPDATE
		SET poster_path = EXCLUDED.poster_path,
		    poster_thumbhash = EXCLUDED.poster_thumbhash,
		    last_used_at = NOW()
	`, s.userID, c.CollectionID, c.Key, c.Path, c.Thumbhash)
	if err != nil {
		return fmt.Errorf("saving personal collection collage: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPersonalCollectionNotFound
	}
	if err := releaseCollectionCollageReservation(ctx, tx, c.Path); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing personal collection collage save: %w", err)
	}
	return nil
}

func (s personalCollageStore) RetireUnusedCollectionCollages(ctx context.Context, collectionID string, cutoff time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM user_personal_collection_poster_variants
		WHERE user_id = $1 AND collection_id = $2 AND last_used_at < $3
	`, s.userID, collectionID, cutoff)
	if err != nil {
		return 0, fmt.Errorf("retiring unused personal collection collages: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s personalCollageStore) ReserveCollectionCollagePath(ctx context.Context, path string) (bool, error) {
	return reserveCollectionCollagePath(ctx, s.pool, path)
}
