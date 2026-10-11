package catalog

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
//
// A smart collection's matches are too costly to read on every list read, so
// a background refresh reads them and records which stored collage each
// viewer's access sees (user_personal_collection_smart_collages). A list read
// serves that record without running the query, and asks for a refresh when
// there is none for the collection's current definition or it is older than
// SmartRefreshInterval, so a new match reaches the collage within that time.

// ErrPersonalCollectionNotFound reports a personal collection that is gone.
var ErrPersonalCollectionNotFound = errors.New("personal collection not found")

const (
	// smartCollageScanLimit is how many of a smart collection's first matches
	// are read to find its posters.
	smartCollageScanLimit = 3 * CollectionCollageSourceLimit
	// defaultPersonalCollageRefreshDelay coalesces a burst of changes to one
	// collection, such as titles added one at a time, into one build.
	defaultPersonalCollageRefreshDelay = 2 * time.Second
	// defaultSmartCollageRefreshInterval is how long a smart collection's
	// recorded collage is served before a read refreshes it.
	defaultSmartCollageRefreshInterval = 15 * time.Minute
)

// PersonalCollectionCollages serves and builds personal collection collages.
// A nil receiver, or one without a generator, serves and builds nothing.
type PersonalCollectionCollages struct {
	pool *pgxpool.Pool
	// gen composes and stores collages. It is set once the API router has
	// built its poster signer, possibly after scheduled syncs have started
	// calling Refresh, so it is read and written atomically; it stays unset
	// when artwork storage is not configured.
	gen atomic.Pointer[collageGeneratorRef]
	// RefreshDelay is how long Refresh waits before reading a collection's
	// titles, so later changes in a burst join the same refresh.
	RefreshDelay time.Duration
	// SmartRefreshInterval is how long a read serves a smart collection's
	// recorded collage before it asks for a refresh of the collection's
	// matches.
	SmartRefreshInterval time.Duration

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

// collageGeneratorRef boxes a CollageGenerator for atomic.Pointer.
type collageGeneratorRef struct{ CollageGenerator }

// NewPersonalCollectionCollages serves the collages of the personal
// collections in pool's user store, composed and stored by gen. A nil gen
// serves and builds nothing until SetCollageGenerator supplies one.
func NewPersonalCollectionCollages(pool *pgxpool.Pool, gen CollageGenerator) *PersonalCollectionCollages {
	p := &PersonalCollectionCollages{
		pool:                 pool,
		RefreshDelay:         defaultPersonalCollageRefreshDelay,
		SmartRefreshInterval: defaultSmartCollageRefreshInterval,
	}
	p.SetCollageGenerator(gen)
	return p
}

// SetCollageGenerator sets the generator that composes and stores collages.
// It is safe to call while collages are served and refreshed.
func (p *PersonalCollectionCollages) SetCollageGenerator(gen CollageGenerator) {
	if gen == nil {
		p.gen.Store(nil)
		return
	}
	p.gen.Store(&collageGeneratorRef{gen})
}

// generator returns the collage generator, or nil when there is none.
func (p *PersonalCollectionCollages) generator() CollageGenerator {
	if p == nil || p.pool == nil {
		return nil
	}
	if ref := p.gen.Load(); ref != nil {
		return ref.CollageGenerator
	}
	return nil
}

func (p *PersonalCollectionCollages) enabled() bool {
	return p.generator() != nil
}

func (p *PersonalCollectionCollages) buildQueue() *collageBuildQueue {
	p.queueOnce.Do(func() { p.queue = newCollageBuildQueue() })
	return p.queue
}

func (p *PersonalCollectionCollages) collageSet(userID int) collageSet {
	return collageSet{store: personalCollageStore{pool: p.pool, userID: userID}, gen: p.generator(), queue: p.buildQueue()}
}

// Posters returns the collage each of account userID's collections shows the
// viewer described by access, keyed by collection ID. Pass only collections
// without an uploaded or imported poster. A collage not built yet is left out
// and built in the background; collections with no title the viewer can see
// that has a poster are absent. Smart collections are served from their
// recorded collages without running their queries.
func (p *PersonalCollectionCollages) Posters(ctx context.Context, userID int, collections []PersonalCollectionDefinition, access AccessFilter) map[string]CollectionPoster {
	if !p.enabled() || len(collections) == 0 {
		return map[string]CollectionPoster{}
	}
	var members, smart []PersonalCollectionDefinition
	for _, c := range collections {
		if IsLiveQueryType(c.CollectionType) {
			smart = append(smart, c)
		} else {
			members = append(members, c)
		}
	}
	posters := p.serveSmart(ctx, userID, smart, access)
	if len(members) == 0 {
		return posters
	}
	sources, err := p.ListSources(ctx, userID, members, access)
	if err != nil {
		slog.WarnContext(ctx, "collage: failed to select personal collection collages", "component", "catalog", "error", err)
		return posters
	}
	ids := make([]string, 0, len(members))
	for _, c := range members {
		ids = append(ids, c.ID)
	}
	maps.Copy(posters, p.collageSet(userID).serve(ctx, ids, sources))
	return posters
}

// serveSmart returns the recorded collage each smart collection shows the
// viewer described by access, and asks for a background refresh of each one
// whose record is missing, made for another definition, or due.
func (p *PersonalCollectionCollages) serveSmart(ctx context.Context, userID int, collections []PersonalCollectionDefinition, access AccessFilter) map[string]CollectionPoster {
	posters := make(map[string]CollectionPoster, len(collections))
	if len(collections) == 0 || (access.AllowedLibraryIDs != nil && len(access.AllowedLibraryIDs) == 0) {
		return posters
	}
	ids := make([]string, 0, len(collections))
	for _, c := range collections {
		ids = append(ids, c.ID)
	}
	store := personalCollageStore{pool: p.pool, userID: userID}
	recorded, err := store.getSmartCollages(ctx, ids, collageAccessKey(access))
	if err != nil {
		slog.WarnContext(ctx, "collage: failed to load smart collection collages", "component", "catalog", "error", err)
		return posters
	}
	var touch []CollectionCollageRef
	now := time.Now()
	for _, c := range collections {
		r, ok := recorded[c.ID]
		if !ok || r.definitionKey != smartDefinitionKey(c.QueryDefinition) {
			p.refreshSmartLater(userID, c, access)
			continue
		}
		if now.Sub(r.refreshedAt) >= p.SmartRefreshInterval {
			p.refreshSmartLater(userID, c, access)
		}
		if r.collage.Path == "" {
			continue
		}
		posters[c.ID] = CollectionPoster{Path: r.collage.Path, Thumbhash: r.collage.Thumbhash, CollageKey: r.collage.Key}
		if now.Sub(r.collage.LastUsedAt) > collectionCollageTouchInterval {
			touch = append(touch, r.collage.CollectionCollageRef)
		}
	}
	if err := store.TouchCollectionCollages(ctx, touch); err != nil {
		slog.DebugContext(ctx, "collage: failed to touch collection collages", "component", "catalog", "error", err)
	}
	return posters
}

// refreshSmartLater runs refreshSmart off the request. Refreshes of one
// collection for one viewer and definition share a run.
func (p *PersonalCollectionCollages) refreshSmartLater(userID int, c PersonalCollectionDefinition, access AccessFilter) {
	ref := CollectionCollageRef{CollectionID: c.ID, Key: "smart:" + collageAccessKey(access) + ":" + smartDefinitionKey(c.QueryDefinition)}
	p.buildQueue().runLater(ref, func(ctx context.Context) error { return p.refreshSmart(ctx, userID, c, access) })
}

// refreshSmart reads smart collection c's first matches the viewer described
// by access can see, builds their collage unless it is stored, and records it
// as the collage that viewer sees. When no match has a poster it records that
// there is none and succeeds: the record holds off the next refresh until
// SmartRefreshInterval, so the build queue keeps no retry marker for it.
func (p *PersonalCollectionCollages) refreshSmart(ctx context.Context, userID int, c PersonalCollectionDefinition, access AccessFilter) error {
	sources, err := p.smartSources(ctx, c.QueryDefinition, access)
	if err != nil {
		return err
	}
	key := ""
	if len(sources) > 0 {
		if err := p.collageSet(userID).prepare(ctx, c.ID, sources); err != nil {
			return err
		}
		key = CollectionCollageKey(sources)
	}
	store := personalCollageStore{pool: p.pool, userID: userID}
	return store.saveSmartCollage(ctx, c.ID, collageAccessKey(access), smartDefinitionKey(c.QueryDefinition), key)
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

	if IsLiveQueryType(r.collection.CollectionType) {
		p.refreshSmartLater(r.userID, r.collection, r.access)
		return
	}
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

// smartCollage is the collage recorded for one smart collection and viewer
// access. collage.Path is empty when the viewer can see no match with a
// poster.
type smartCollage struct {
	definitionKey string
	refreshedAt   time.Time
	collage       CollectionCollage
}

// getSmartCollages returns the collages recorded for the viewer access
// accessKey names, keyed by collection ID.
func (s personalCollageStore) getSmartCollages(ctx context.Context, collectionIDs []string, accessKey string) (map[string]smartCollage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sc.collection_id, sc.definition_key, sc.refreshed_at,
		       COALESCE(v.variant_key, ''), COALESCE(v.poster_path, ''), COALESCE(v.poster_thumbhash, ''),
		       COALESCE(v.last_used_at, sc.refreshed_at)
		FROM user_personal_collection_smart_collages sc
		LEFT JOIN user_personal_collection_poster_variants v
		  ON v.user_id = sc.user_id AND v.collection_id = sc.collection_id AND v.variant_key = sc.variant_key
		WHERE sc.user_id = $1 AND sc.access_key = $2 AND sc.collection_id = ANY($3)
	`, s.userID, accessKey, collectionIDs)
	if err != nil {
		return nil, fmt.Errorf("loading smart collection collages: %w", err)
	}
	defer rows.Close()
	recorded := make(map[string]smartCollage, len(collectionIDs))
	for rows.Next() {
		var r smartCollage
		if err := rows.Scan(&r.collage.CollectionID, &r.definitionKey, &r.refreshedAt, &r.collage.Key, &r.collage.Path, &r.collage.Thumbhash, &r.collage.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scanning smart collection collage: %w", err)
		}
		recorded[r.collage.CollectionID] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating smart collection collages: %w", err)
	}
	return recorded, nil
}

// saveSmartCollage records variantKey, empty for none, as the collage the
// viewer access accessKey names sees for a smart collection's definition.
func (s personalCollageStore) saveSmartCollage(ctx context.Context, collectionID, accessKey, definitionKey, variantKey string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO user_personal_collection_smart_collages (user_id, collection_id, access_key, definition_key, variant_key)
		SELECT $1, $2, $3, $4, NULLIF($5, '')
		WHERE EXISTS (SELECT 1 FROM user_personal_collections WHERE user_id = $1 AND id = $2)
		ON CONFLICT (user_id, collection_id, access_key) DO UPDATE
		SET definition_key = EXCLUDED.definition_key,
		    variant_key = EXCLUDED.variant_key,
		    refreshed_at = NOW()
	`, s.userID, collectionID, accessKey, definitionKey, variantKey); err != nil {
		return fmt.Errorf("recording smart collection collage: %w", err)
	}
	return nil
}

// collageAccessKey names the access a viewer reads collections with: every
// field of the filter that can change which titles a query matches or which
// posters they show. Equal filters always get the same key.
func collageAccessKey(access AccessFilter) string {
	access.DeviceID = ""
	access.ImageSize = ""
	access.AllowedLibraryIDs = sortedClone(access.AllowedLibraryIDs)
	access.DisabledLibraryIDs = sortedClone(access.DisabledLibraryIDs)
	access.AllowedContentIDs = sortedClone(access.AllowedContentIDs)
	access.ExcludedMediaTypes = sortedClone(access.ExcludedMediaTypes)
	encoded, err := json.Marshal(access)
	if err != nil {
		// Every field marshals; an unexpected failure must still separate filters.
		encoded = fmt.Appendf(nil, "%#v", access)
	}
	return shortHash(encoded)
}

// sortedClone returns a sorted copy of s, nil when s is nil, so an
// unrestricted list stays distinct from an empty one.
func sortedClone[S ~[]E, E cmp.Ordered](s S) S {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

// smartDefinitionKey names a smart collection's query definition.
func smartDefinitionKey(queryDefinition string) string {
	return shortHash([]byte(queryDefinition))
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func (s personalCollageStore) ReserveCollectionCollagePath(ctx context.Context, path string) (bool, error) {
	return reserveCollectionCollagePath(ctx, s.pool, path)
}
