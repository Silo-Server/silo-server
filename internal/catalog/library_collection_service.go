package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/collage"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"
)

// TMDBCollectionEntry is a lightweight TMDB preset result used by the collection sync.
type TMDBCollectionEntry struct {
	ID        int
	MediaType string
	Title     string
	IMDbID    string
	TVDBID    int
	// ReleaseDate is the TMDB primary_release_date (movies) or first_air_date
	// (TV) in "YYYY-MM-DD" format. An empty string means the date is unknown.
	ReleaseDate string
}

// TMDBCollectionFetcher abstracts the TMDB preset API so the catalog package
// does not import the tmdb package directly.
type TMDBCollectionFetcher interface {
	GetCollectionPreset(ctx context.Context, preset, mediaType, timeWindow string, limit int) ([]TMDBCollectionEntry, error)
}

// TMDBCollectionByIDFetcher abstracts TMDB's /collection/{id} franchise/saga
// endpoint. It returns the curated, ordered list of parts in a TMDB
// collection (e.g. all MCU films in MCU collection 86311). Entries carry only
// their TMDB IDs; MatchTMDBEntries asks the fetcher for more when it needs
// them.
//
// Implementations are expected to deal with pagination internally — TMDB
// collection responses are single-page, so this is currently trivial.
type TMDBCollectionByIDFetcher interface {
	GetCollection(ctx context.Context, id int) ([]TMDBCollectionEntry, error)
}

// TMDBDiscoverParams mirrors templates.TMDBDiscoverSpec so the catalog package
// can hand discover params to the fetcher without importing the tmdb package
// directly.
type TMDBDiscoverParams struct {
	WithGenres       []int
	WithoutGenres    []int
	SortBy           string
	VoteCountGte     int
	VoteAverageGte   float64
	ReleaseDateGte   string
	ReleaseDateLte   string
	Certifications   []string
	CertificationLte string
	WithRuntimeGte   int
	WithRuntimeLte   int
	OriginalLanguage string
}

// TMDBDiscoverFetcher abstracts TMDB's `/discover/{movie,tv}` endpoint so the
// catalog package does not import the tmdb package directly.
type TMDBDiscoverFetcher interface {
	Discover(ctx context.Context, mediaType string, params TMDBDiscoverParams, limit int) ([]TMDBCollectionEntry, error)
}

type TMDBDigitalReleaseChecker interface {
	HasDigitalRelease(ctx context.Context, tmdbID int) (bool, error)
}

// MDBListAPIFetcher fetches a user's list through MDBList's authenticated,
// cursor-paginated items endpoint. It is satisfied by *mdblist.Client.
type MDBListAPIFetcher interface {
	ListItems(ctx context.Context, user, list string, maxItems int) ([]mdblist.ListItem, error)
}

// theatricalReleaseGate memoizes digital-release lookups for one sync run so
// overlapping entries cost a single TMDB call per title.
type theatricalReleaseGate struct {
	checker        TMDBDigitalReleaseChecker
	lookup         func(ctx context.Context, tmdbID int) (bool, error)
	lookupProvider func(ctx context.Context, tmdbID int) (bool, error)
	memo           map[int]bool
	memoMu         sync.Mutex
	inflight       singleflight.Group
	overrides      ReleaseOverrideLookup
	// canonicalIDs unions source-entry IDs with a catalog-resident same
	// movie's IDs. Nil outside collection sync; set by releaseGate.
	// Conflicts fail closed: on error the entry IDs are kept and the
	// error is recorded on the collection tracker so the sync surfaces
	// it instead of deciding on a partial identity set.
	//
	// canonicalFullIDs returns the canonical content ID plus the complete
	// identity set including provider-table matches. Used by the prefilter
	// to ensure override decisions see every known alias.
	canonicalFullIDs func(ctx context.Context, tmdbID int, imdbID string) (string, []ReleaseIdentity, error)
	canonicalIDs     func(ctx context.Context, tmdbID int, imdbID string) (int, string, error)
}

func newTheatricalReleaseGate(checker TMDBDigitalReleaseChecker, overrides ...ReleaseOverrideLookup) *theatricalReleaseGate {
	gate := &theatricalReleaseGate{checker: checker, memo: map[int]bool{}}
	if len(overrides) > 0 {
		gate.overrides = overrides[0]
	}
	// lookupProvider is provider evidence only: memoized TMDB digital-release
	// answers with no override evaluation. Override decisions belong to the
	// callers' validated snapshots. The memo is mutex-guarded and concurrent
	// callers for the same id collapse into one TMDB call via singleflight so
	// prefetch and the sequential pass cannot duplicate work.
	lookupProvider := func(ctx context.Context, tmdbID int) (bool, error) {
		if cached, ok := gate.memoValue(tmdbID); ok {
			return cached, nil
		}
		if gate.checker == nil || tmdbID <= 0 {
			return false, fmt.Errorf("%w: movie home release evidence unavailable", ErrProviderUnavailable)
		}
		if ctx == nil {
			ctx = context.Background()
		}
		value, err, _ := gate.inflight.Do(strconv.Itoa(tmdbID), func() (any, error) {
			if cached, ok := gate.memoValue(tmdbID); ok {
				return cached, nil
			}
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			digital, err := gate.checker.HasDigitalRelease(checkCtx, tmdbID)
			if err != nil {
				return false, fmt.Errorf("%w: movie home release lookup: %w", ErrProviderUnavailable, err)
			}
			gate.memoStore(tmdbID, digital)
			return digital, nil
		})
		if err != nil {
			return false, err
		}
		released, _ := value.(bool)
		return released, nil
	}
	gate.lookupProvider = lookupProvider
	// lookup preserves the historical override-aware behavior for direct
	// callers; release flows decide from validated snapshots and use
	// lookupProvider for evidence.
	gate.lookup = func(ctx context.Context, tmdbID int) (bool, error) {
		if tmdbID > 0 {
			allowed, active, err := releaseOverrideDecision(ctx, gate.overrides, releaseIdentities("movie", strconv.Itoa(tmdbID), "", "", 0, 0))
			if err != nil || active {
				return allowed, err
			}
		}
		return lookupProvider(ctx, tmdbID)
	}
	return gate
}

// theatricalPrefetchWorkers bounds the concurrent TMDB release lookups issued
// when warming the gate memo before a sequential materialize loop.
const theatricalPrefetchWorkers = 6

func (g *theatricalReleaseGate) memoValue(tmdbID int) (bool, bool) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	value, ok := g.memo[tmdbID]
	return value, ok
}

func (g *theatricalReleaseGate) memoStore(tmdbID int, released bool) {
	g.memoMu.Lock()
	g.memo[tmdbID] = released
	g.memoMu.Unlock()
}

// prefetch warms the memo for tmdbIDs with a small bounded worker pool so a
// batch of unmatched movies does not serialize N x 5s TMDB calls. Each lookup
// keeps the 5s per-call bound and single-flight dedupe. Errors are swallowed:
// the sequential gate callers fail open on inconclusive evidence and retry.
func (g *theatricalReleaseGate) prefetch(ctx context.Context, tmdbIDs []int) {
	if g == nil || len(tmdbIDs) == 0 || g.checker == nil {
		// No provider to warm: the sequential gate fails open immediately, so
		// spinning up workers would only churn.
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workers := theatricalPrefetchWorkers
	if len(tmdbIDs) < workers {
		workers = len(tmdbIDs)
	}
	ids := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for id := range ids {
				if ctx.Err() != nil {
					return
				}
				// Fail open: the sequential pass re-evaluates this id and
				// defers to the authoritative materialization decision.
				if _, err := g.lookupProvider(ctx, id); err != nil {
					continue
				}
			}
		}()
	}
	for _, id := range tmdbIDs {
		select {
		case <-ctx.Done():
			close(ids)
			wg.Wait()
			return
		case ids <- id:
		}
	}
	close(ids)
	wg.Wait()
}

func isFutureDate(year int, releaseDate string) bool {
	now := time.Now().UTC()
	rd := strings.TrimSpace(releaseDate)
	if len(rd) >= 10 {
		rd = rd[:10]
	}
	if rd != "" {
		if t, err := time.Parse("2006-01-02", rd); err == nil {
			return t.After(now)
		}
		if len(rd) == 4 && year == 0 {
			if y, err := strconv.Atoi(rd); err == nil {
				year = y
			}
		}
	}
	if year > 0 {
		return year > now.Year()
	}
	return false
}

// canonicalMovieIDs unions source-entry IDs with a catalog-resident same
// movie's IDs so a stored permitting alias is visible to the prefilter even
// when the resident item lives outside the target libraries. Conflicting
// owners across the supplied aliases fail closed to the entry IDs (no
// arbitrary LIMIT 1 winner); query errors are returned to the caller so
// infrastructure failure is never confused with absence.
func (s *LibraryCollectionService) canonicalMovieIDs(ctx context.Context, tmdbID int, imdbID string) (int, string, error) {
	return s.canonicalMovieIdentities(ctx, tmdbID, imdbID)
}

// canonicalMovieIdentities is the error-returning core of canonicalMovieIDs.
// Multiple distinct content owners across the supplied aliases is a conflict:
// the entry IDs are returned along with an error so callers can fail closed.
func (s *LibraryCollectionService) canonicalMovieIdentities(ctx context.Context, tmdbID int, imdbID string) (int, string, error) {
	if s == nil || s.items == nil {
		return tmdbID, imdbID, nil
	}
	tmdbText := ""
	if tmdbID > 0 {
		tmdbText = strconv.Itoa(tmdbID)
	}
	imdbID = strings.TrimSpace(imdbID)
	if tmdbText == "" && imdbID == "" {
		return tmdbID, imdbID, nil
	}
	item, err := s.items.GetByExternalID(ctx, tmdbText, imdbID, "", "movie")
	if err != nil && !errors.Is(err, ErrItemNotFound) {
		return tmdbID, imdbID, fmt.Errorf("resolve canonical movie by external ID: %w", err)
	}
	contentIDs := map[string]struct{}{}
	if item != nil {
		contentIDs[item.ContentID] = struct{}{}
	}
	pool := (*pgxpool.Pool)(nil)
	if s.collections != nil {
		pool = s.collections.pool
	}
	// Always merge primary and alias-table owners: a primary hit on one
	// alias must not mask a divergent owner recorded for the other alias
	// in the provider table.
	if pool != nil {
		rows, err := pool.Query(ctx, `
			SELECT DISTINCT content_id FROM media_item_provider_ids
			WHERE item_type = 'movie' AND (
				(provider = 'tmdb' AND provider_id = $1 AND $1 <> '') OR
				(provider = 'imdb' AND provider_id = $2 AND $2 <> '')
			)`, tmdbText, imdbID)
		if err != nil {
			return tmdbID, imdbID, fmt.Errorf("resolve canonical movie by provider alias: %w", err)
		}
		cids, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil {
			return tmdbID, imdbID, fmt.Errorf("collect canonical movie aliases: %w", qerr)
		}
		for _, cid := range cids {
			if cid != "" {
				contentIDs[cid] = struct{}{}
			}
		}
	}
	if len(contentIDs) > 1 {
		return tmdbID, imdbID, fmt.Errorf("%w: tmdb %q and imdb %q resolve to %d distinct movies", ErrReleaseOverrideConflict, tmdbText, imdbID, len(contentIDs))
	}
	if item == nil {
		for cid := range contentIDs {
			loaded, lerr := s.items.GetByID(ctx, cid)
			if lerr != nil {
				return tmdbID, imdbID, fmt.Errorf("load canonical movie %q: %w", cid, lerr)
			}
			item = loaded
		}
	}
	contentID := ""
	if item != nil {
		contentID = item.ContentID
		if tmdbID <= 0 {
			if n, convErr := strconv.Atoi(strings.TrimSpace(item.TmdbID)); convErr == nil && n > 0 {
				tmdbID = n
			}
		}
		if imdbID == "" {
			imdbID = strings.TrimSpace(item.ImdbID)
		}
	}
	if contentID != "" && pool != nil {
		if imdbID == "" {
			var provIMDb string
			if qerr := pool.QueryRow(ctx, `SELECT provider_id FROM media_item_provider_ids WHERE content_id = $1 AND provider = 'imdb' LIMIT 1`, contentID).Scan(&provIMDb); qerr != nil && !errors.Is(qerr, pgx.ErrNoRows) {
				return tmdbID, imdbID, fmt.Errorf("backfill canonical imdb alias: %w", qerr)
			} else if qerr == nil {
				imdbID = strings.TrimSpace(provIMDb)
			}
		}
		if tmdbID <= 0 {
			var provTMDB string
			if qerr := pool.QueryRow(ctx, `SELECT provider_id FROM media_item_provider_ids WHERE content_id = $1 AND provider = 'tmdb' LIMIT 1`, contentID).Scan(&provTMDB); qerr != nil && !errors.Is(qerr, pgx.ErrNoRows) {
				return tmdbID, imdbID, fmt.Errorf("backfill canonical tmdb alias: %w", qerr)
			} else if qerr == nil {
				if n, convErr := strconv.Atoi(strings.TrimSpace(provTMDB)); convErr == nil && n > 0 {
					tmdbID = n
				}
			}
		}
	}
	return tmdbID, imdbID, nil
}

// canonicalMovieFullIdentities resolves the canonical content ID and the
// complete identity set including provider-table matches. It returns all
// known release aliases so callers can make full override decisions.
// Lookup errors are propagated: incomplete discovery must never be treated
// as permission to make a definitive rejection.
func (s *LibraryCollectionService) canonicalMovieFullIdentities(ctx context.Context, tmdbID int, imdbID string) (string, []ReleaseIdentity, error) {
	if s == nil || s.items == nil {
		return "", nil, nil
	}
	tmdbText := ""
	if tmdbID > 0 {
		tmdbText = strconv.Itoa(tmdbID)
	}
	imdbID = strings.TrimSpace(imdbID)
	if tmdbText == "" && imdbID == "" {
		return "", nil, nil
	}
	item, err := s.items.GetByExternalID(ctx, tmdbText, imdbID, "", "movie")
	if err != nil && !errors.Is(err, ErrItemNotFound) {
		return "", nil, fmt.Errorf("resolve canonical movie by external ID: %w", err)
	}
	contentIDs := map[string]struct{}{}
	if item != nil {
		contentIDs[item.ContentID] = struct{}{}
	}
	pool := (*pgxpool.Pool)(nil)
	if s.collections != nil {
		pool = s.collections.pool
	}
	if pool != nil {
		rows, err := pool.Query(ctx, `
			SELECT DISTINCT content_id FROM media_item_provider_ids
			WHERE item_type = 'movie' AND (
				(provider = 'tmdb' AND provider_id = $1 AND $1 <> '') OR
				(provider = 'imdb' AND provider_id = $2 AND $2 <> '')
			)`, tmdbText, imdbID)
		if err != nil {
			return "", nil, fmt.Errorf("resolve canonical movie by provider alias: %w", err)
		}
		cids, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil {
			return "", nil, fmt.Errorf("collect canonical movie aliases: %w", qerr)
		}
		for _, cid := range cids {
			if cid != "" {
				contentIDs[cid] = struct{}{}
			}
		}
	}
	if len(contentIDs) > 1 {
		return "", nil, fmt.Errorf("%w: tmdb %q and imdb %q resolve to %d distinct movies", ErrReleaseOverrideConflict, tmdbText, imdbID, len(contentIDs))
	}
	contentID := ""
	if item != nil {
		contentID = item.ContentID
	} else {
		for cid := range contentIDs {
			contentID = cid
		}
	}
	if contentID == "" {
		return "", releaseIdentities("movie", tmdbText, "", imdbID, 0, 0), nil
	}
	// Use the content-lock-safe resolution for the canonical item's full identity set.
	ids, idErr := releaseIdentitiesForContent(ctx, pool, "movie", contentID, "movie", tmdbText, "", imdbID, 0, 0)
	if idErr != nil {
		return contentID, nil, idErr
	}
	return contentID, ids, nil
}

func (g *theatricalReleaseGate) skipTheatricalMovie(ctx context.Context, tmdbID int, imdbID, title string, year int, releaseDate string) bool {
	// Union the source entry with a catalog-resident same movie so a stored
	// permitting alias is visible even outside the target libraries.
	// Canonical conflicts fail closed: the entry is skipped and the error
	// is recorded so the sync surfaces it instead of deciding on stale IDs.
	if g.canonicalIDs != nil {
		resolvedTMDB, resolvedIMDb, canonicalErr := g.canonicalIDs(ctx, tmdbID, imdbID)
		if canonicalErr != nil {
			if ctx != nil {
				if tracker, _ := ctx.Value(collectionVirtualCreationTrackerKey{}).(*collectionVirtualCreationTracker); tracker != nil {
					tracker.err = canonicalErr
				}
			}
			return true
		}
		tmdbID, imdbID = resolvedTMDB, resolvedIMDb
	}
	tmdbText := ""
	if tmdbID > 0 {
		tmdbText = strconv.Itoa(tmdbID)
	}
	// The prefilter sees the complete source identity set so an override on
	// any alias (e.g. a past IMDb override for a TMDB-listed entry) applies
	// before the entry can be rejected. When canonicalFullIDs is available,
	// use the complete identity set including provider-table matches.
	ids := releaseIdentities("movie", tmdbText, "", imdbID, 0, 0)
	if g.canonicalFullIDs != nil {
		if _, fullIDs, fullErr := g.canonicalFullIDs(ctx, tmdbID, imdbID); fullErr != nil {
			if ctx != nil {
				if tracker, _ := ctx.Value(collectionVirtualCreationTrackerKey{}).(*collectionVirtualCreationTracker); tracker != nil {
					tracker.err = fullErr
				}
			}
			return true
		} else if len(fullIDs) > 0 {
			ids = fullIDs
		}
	}
	if len(ids) > 0 {
		allowed, active, err := releaseOverrideDecision(ctx, g.overrides, ids)
		if err != nil || active {
			if err != nil && ctx != nil {
				if tracker, _ := ctx.Value(collectionVirtualCreationTrackerKey{}).(*collectionVirtualCreationTracker); tracker != nil {
					tracker.err = err
				}
			}
			return err != nil || !allowed
		}
	}
	if isFutureDate(year, releaseDate) {
		return true
	}
	released, err := g.lookupProvider(ctx, tmdbID)
	if err != nil {
		// Inconclusive (no TMDB identity or provider failure): do not reject
		// here. The authoritative materialization decision records the error
		// if a virtual item is actually created.
		return false
	}
	return !released
}

// TMDBListFetcher abstracts TMDB's `/list/{id}` endpoint for public,
// user-authored lists. Entries come back in list order, carrying only their
// TMDB IDs like the other TMDB fetchers. A limit <= 0 reads the whole list.
type TMDBListFetcher interface {
	GetList(ctx context.Context, id, limit int) ([]TMDBCollectionEntry, error)
}

// CollageGenerator composes and stores collection collages. Stored collages
// are deleted through the artwork revision collector, never directly.
type CollageGenerator interface {
	// CollectionCollagePath returns the path ComposeCollectionCollage stores
	// the collection's collage key under.
	CollectionCollagePath(collectionID, key string) string
	// ComposeCollectionCollage composes a poster from the source poster paths,
	// in order, and stores it as the collection's collage key. It returns the
	// stored poster path and its thumbhash, or collage.ErrNotEnoughImages when
	// no source image is usable.
	ComposeCollectionCollage(ctx context.Context, collectionID, key string, sources []string) (path, thumbhash string, err error)
}

var ErrLibraryCollectionSyncUnsupported = errors.New("smart collections cannot be synchronized")

// ErrLibraryCollectionSyncModeUnsupported reports a collection whose source
// mode has no importer. Manual collections carry no mode at all, so a sync
// request for one lands here; it is a caller mistake, not a server fault.
var ErrLibraryCollectionSyncModeUnsupported = errors.New("unsupported collection sync mode")

// Library collection source modes (the `mode` field of the stored source
// config). Only the modes that appear in a sync dispatch decision are named;
// the rest of the switch below stays on literals.
const (
	libraryCollectionSourceModeSmart       = "smart"
	libraryCollectionSourceModeMDBListJSON = "mdblist_json"
)

const (
	virtualMetadataRefreshWorkers = 4
	virtualMetadataRefreshQueue   = 256
	virtualMetadataRefreshTimeout = 2 * time.Minute
)

type LibraryCollectionService struct {
	collections  *LibraryCollectionRepository
	items        *ItemRepository
	libraryItems *LibraryItemRepository
	httpClient   *http.Client

	// TMDBCollections is nil when TMDB is not configured.
	TMDBCollections TMDBCollectionFetcher

	// TMDBFranchises is nil when TMDB is not configured. It serves the
	// `tmdb_collection` source mode (curated franchises / sagas).
	TMDBFranchises TMDBCollectionByIDFetcher

	// TMDBDiscovers is nil when TMDB is not configured. It serves the
	// `tmdb_discover` source mode (genre matrices, decade filters, etc.).
	TMDBDiscovers TMDBDiscoverFetcher

	// TMDBDigitalReleases is nil when theatrical gating is not configured.
	// When set, synced collections skip movies that are still
	// theatrical-only (no Digital/Physical/TV release on TMDB yet) instead of
	// materializing unplayable placeholders.
	TMDBDigitalReleases TMDBDigitalReleaseChecker

	// TMDBLists is nil when TMDB is not configured. It serves the `tmdb_list`
	// source mode (public user-authored lists).
	TMDBLists TMDBListFetcher

	// MDBListAPI is the authenticated, cursor-paginated MDBList list-items
	// client. Nil (no api_key configured) falls back to the public /json
	// single-GET fetch.
	MDBListAPI MDBListAPIFetcher

	// CollageGen is nil when S3/image processing is not configured.
	CollageGen CollageGenerator
	// VirtualVariants returns configured provider-neutral profile placeholders.
	// It must not contact an upstream streaming provider.
	VirtualVariants func(context.Context, string, string) ([]VirtualPlaybackVariant, error)
	// RefreshVirtualItem is invoked after a collection-only item is materialized
	// so metadata is enriched immediately instead of waiting for the six-hour
	// refresh-debt task.
	RefreshVirtualItem func(context.Context, string) error

	virtualRefreshOnce  sync.Once
	virtualRefreshQueue chan string

	collageBuildsOnce sync.Once
	collageBuilds     *collageBuildQueue
}

type collectionVirtualCreationTracker struct {
	items       map[string]preparedCollectionItem
	err         error
	releaseGate *theatricalReleaseGate
}

// movieReleaseIdentities unions an item's scalar external IDs with its
// stored provider-table aliases for override decisions. Alias-read failures
// are returned: callers must not decide on a potentially partial set.
func (s *LibraryCollectionService) movieReleaseIdentities(ctx context.Context, item *models.MediaItem) ([]ReleaseIdentity, error) {
	if item == nil {
		return nil, nil
	}
	if s == nil || s.collections == nil || s.collections.pool == nil || item.ContentID == "" {
		return releaseIdentities("movie", item.TmdbID, "", item.ImdbID, 0, 0), nil
	}
	return releaseIdentitiesForContent(ctx, s.collections.pool, "movie", item.ContentID, "movie", item.TmdbID, "", item.ImdbID, 0, 0)
}

// hasPhysicalMovieFiles reports whether the catalog holds a non-virtual
// file for the item, used as release evidence of last resort.
func (s *LibraryCollectionService) hasPhysicalMovieFiles(ctx context.Context, item *models.MediaItem) (bool, error) {
	if s == nil || s.collections == nil || s.collections.pool == nil || item == nil || item.ContentID == "" {
		return false, nil
	}
	return hasPhysicalMediaFiles(ctx, s.collections.pool, item.ContentID)
}

func (s *LibraryCollectionService) releaseGate(ctx context.Context) *theatricalReleaseGate {
	var overrides ReleaseOverrideLookup
	if s.items != nil && s.items.pool != nil {
		overrides = NewReleaseOverrideRepository(s.items.pool)
	}
	newGate := func() *theatricalReleaseGate {
		gate := newTheatricalReleaseGate(s.TMDBDigitalReleases, overrides)
		gate.canonicalIDs = s.canonicalMovieIDs
		gate.canonicalFullIDs = s.canonicalMovieFullIdentities
		return gate
	}
	if tracker, _ := ctx.Value(collectionVirtualCreationTrackerKey{}).(*collectionVirtualCreationTracker); tracker != nil {
		if tracker.releaseGate == nil {
			tracker.releaseGate = newGate()
		}
		return tracker.releaseGate
	}
	return newGate()
}

type preparedCollectionItem struct {
	item            *models.MediaItem
	variants        []VirtualPlaybackVariant
	releaseSnapshot []ReleaseOverride
}

type collectionVirtualCreationTrackerKey struct{}
type collectionVirtualVariantCacheKey struct{}

type collectionVirtualVariantCache struct {
	mu      sync.Mutex
	entries map[string][]VirtualPlaybackVariant
}

// ErrMemberVariantsUnavailable marks a variant resolution failure for a specific
// collection member. It wraps ErrProviderUnavailable so errors.Is matches both,
// but lets acceptance distinguish member-level variant gaps from collection-level
// release policy rejections.
var ErrMemberVariantsUnavailable = fmt.Errorf("%w: getting virtual profile variants", ErrProviderUnavailable)

func (s *LibraryCollectionService) hasCommittedCollectionState(ctx context.Context, collectionID, itemID string) (bool, error) {
	if s == nil || s.collections == nil || s.collections.pool == nil {
		return false, nil
	}
	var hasState bool
	err := s.collections.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM library_collection_items WHERE collection_id=$1 AND media_item_id=$2
		) AND EXISTS(
			SELECT 1 FROM virtual_media_source_claims WHERE source_key=$3 AND content_id=$2
		)`, collectionID, itemID, "collection:"+collectionID).Scan(&hasState)
	if err != nil {
		return false, fmt.Errorf("checking resident collection item state: %w", err)
	}
	return hasState, nil
}

func (s *LibraryCollectionService) findPhysicalContentMap(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := s.collections.pool.Query(ctx, `
		SELECT DISTINCT mf.content_id FROM media_files mf
		WHERE mf.content_id = ANY($1::text[])
		  AND mf.container IS DISTINCT FROM 'virtual'
		  AND mf.file_path NOT LIKE 'virtual://%'`, ids)
	if err != nil {
		return nil, err
	}
	physical := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		physical[id] = true
	}
	rows.Close()
	return physical, rows.Err()
}

func (s *LibraryCollectionService) resolveAcceptanceMember(
	ctx context.Context,
	collection *models.LibraryCollection,
	member LibraryCollectionItemInput,
	tracker *collectionVirtualCreationTracker,
) (preparedCollectionItem, bool, string, error) {
	if candidate, ok := tracker.items[member.MediaItemID]; ok {
		return candidate, true, "", nil
	}
	item, err := s.items.GetByID(ctx, member.MediaItemID)
	if err != nil {
		return preparedCollectionItem{}, false, "", err
	}
	_, err = s.EnsureCollectionItemMaterializedWithOptions(ctx, collection, item, VirtualMaterializeOptions{})
	if err == nil {
		return tracker.items[member.MediaItemID], true, "", nil
	}
	if tracker.err != nil {
		return preparedCollectionItem{}, false, "", tracker.err
	}
	if !errors.Is(err, ErrMemberVariantsUnavailable) {
		return preparedCollectionItem{}, false, "", err
	}
	hasState, stateErr := s.hasCommittedCollectionState(ctx, collection.ID, member.MediaItemID)
	if stateErr != nil {
		return preparedCollectionItem{}, false, "", stateErr
	}
	warningMsg := fmt.Sprintf("materializing virtual item %q: %v", item.Title, err)
	if hasState {
		slog.WarnContext(ctx, "failed to refresh resident virtual item during acceptance; preserving existing valid state",
			"component", "catalog", "collection_id", collection.ID, "title", item.Title, "error", err)
		return preparedCollectionItem{}, true, warningMsg, nil
	}
	return preparedCollectionItem{}, false, warningMsg, nil
}

func (s *LibraryCollectionService) prepareVirtualAcceptanceMembers(
	ctx context.Context,
	collection *models.LibraryCollection,
	matched []LibraryCollectionItemInput,
	tracker *collectionVirtualCreationTracker,
) ([]LibraryCollectionItemInput, map[string]preparedCollectionItem, []string, error) {
	ids := make([]string, 0, len(matched))
	for _, member := range matched {
		ids = append(ids, member.MediaItemID)
	}
	physical, err := s.findPhysicalContentMap(ctx, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	prepared := make(map[string]preparedCollectionItem, len(matched))
	accepted := make([]LibraryCollectionItemInput, 0, len(matched))
	var warnings []string
	for _, member := range matched {
		if physical[member.MediaItemID] {
			accepted = append(accepted, member)
			continue
		}
		itemCandidate, keep, warn, prepErr := s.resolveAcceptanceMember(ctx, collection, member, tracker)
		if prepErr != nil {
			return nil, nil, nil, prepErr
		}
		if warn != "" {
			warnings = append(warnings, warn)
		}
		if keep {
			if itemCandidate.item != nil {
				prepared[member.MediaItemID] = itemCandidate
			}
			accepted = append(accepted, member)
		}
	}
	return accepted, prepared, warnings, nil
}

func (s *LibraryCollectionService) acceptCollectionItems(ctx context.Context, collection *models.LibraryCollection, matched []LibraryCollectionItemInput) ([]LibraryCollectionItemInput, []string, error) {
	tracker, _ := ctx.Value(collectionVirtualCreationTrackerKey{}).(*collectionVirtualCreationTracker)
	if tracker == nil {
		return nil, nil, errors.New("collection sync preparation is missing")
	}
	if tracker.err != nil {
		return nil, nil, tracker.err
	}
	var (
		prepared           = make(map[string]preparedCollectionItem, len(matched))
		accepted           = matched
		acceptanceWarnings []string
		err                error
	)
	if collection != nil && sourceEnablesVirtualPlayback(collection.SourceConfig) {
		accepted, prepared, acceptanceWarnings, err = s.prepareVirtualAcceptanceMembers(ctx, collection, matched, tracker)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(accepted) == 0 && len(matched) > 0 {
		return nil, nil, ErrProviderUnavailable
	}
	for i := range accepted {
		accepted[i].Position = i
	}
	if collection != nil && s.collections != nil {
		if err := s.collections.AcceptPreparedItems(ctx, collection, accepted, prepared, s.items); err != nil {
			slog.WarnContext(ctx, "collection sync accept failed", "component", "catalog",
				"collection_id", collection.ID, "error", err)
			return nil, nil, err
		}
	}
	for id := range prepared {
		s.queueVirtualMetadataRefresh(id)
	}
	return accepted, acceptanceWarnings, nil
}

func (s *LibraryCollectionService) configuredVirtualVariants(ctx context.Context, virtualURI, mediaType string) ([]VirtualPlaybackVariant, error) {
	if s == nil || s.VirtualVariants == nil {
		return nil, nil
	}
	cache, _ := ctx.Value(collectionVirtualVariantCacheKey{}).(*collectionVirtualVariantCache)
	if cache == nil {
		return s.VirtualVariants(ctx, virtualURI, mediaType)
	}
	cache.mu.Lock()
	template, ok := cache.entries[mediaType]
	cache.mu.Unlock()
	if !ok {
		variants, err := s.VirtualVariants(ctx, virtualURI, mediaType)
		if err != nil {
			return nil, err
		}
		cache.mu.Lock()
		cache.entries[mediaType] = append([]VirtualPlaybackVariant(nil), variants...)
		cache.mu.Unlock()
		return variants, nil
	}
	target, err := url.Parse(virtualURI)
	if err != nil || target.Scheme != "virtual" {
		return nil, fmt.Errorf("invalid virtual playback URI %q", virtualURI)
	}
	variants := make([]VirtualPlaybackVariant, 0, len(template))
	for _, variant := range template {
		parsed, parseErr := url.Parse(variant.VirtualURI)
		if parseErr != nil || parsed.Scheme != "virtual" {
			return nil, fmt.Errorf("invalid cached virtual profile URI %q", variant.VirtualURI)
		}
		rebased := *target
		rebased.RawQuery = parsed.RawQuery
		variant.VirtualURI = rebased.String()
		variants = append(variants, variant)
	}
	return variants, nil
}

func NewLibraryCollectionService(
	collections *LibraryCollectionRepository,
	items *ItemRepository,
	libraryItems *LibraryItemRepository,
	httpClient *http.Client,
) *LibraryCollectionService {
	httpClient = collectionutil.MDBListHTTPClient(httpClient)

	return &LibraryCollectionService{
		collections:  collections,
		items:        items,
		libraryItems: libraryItems,
		httpClient:   httpClient,
	}
}

// CollectionBuilders holds per-builder item limits for synced collections.
type CollectionBuilders struct {
	MDBList int `json:"mdblist,omitempty"`
}

type SyncCollectionOptions struct {
	SkipCollage bool
}

type libraryCollectionSourceConfig struct {
	Mode            string              `json:"mode"`
	Provider        string              `json:"provider,omitempty"`
	Preset          string              `json:"preset,omitempty"`
	URL             string              `json:"url,omitempty"`
	ListURL         string              `json:"list_url,omitempty"`
	MediaType       string              `json:"media_type,omitempty"`
	TimeWindow      string              `json:"time_window,omitempty"`
	ProfileID       string              `json:"profile_id,omitempty"`
	Limit           *int                `json:"limit,omitempty"`
	VirtualPlayback bool                `json:"virtual_playback,omitempty"`
	Builders        *CollectionBuilders `json:"builders,omitempty"`
	// CollectionID is the TMDB collection ID for the `tmdb_collection` mode.
	// Stored as a plain int (not *int) so zero round-trips as "unset" via the
	// omitempty tag — the sync path treats 0 as a placeholder sentinel.
	CollectionID int `json:"collection_id,omitempty"`

	// Discover holds the TMDB /discover parameters for the `tmdb_discover`
	// mode. The pointer lets the JSON omit the object entirely for non-
	// discover modes so existing tmdb_preset / mdblist_json
	// configs round-trip unchanged.
	Discover *libraryCollectionDiscoverConfig `json:"discover,omitempty"`
}

// TMDBCollectionSourceUnset reports whether a collection's source names no
// TMDB collection: the placeholder a franchise template creates until an
// administrator sets one.
func TMDBCollectionSourceUnset(sourceConfig json.RawMessage) bool {
	var cfg libraryCollectionSourceConfig
	return json.Unmarshal(sourceConfig, &cfg) != nil || cfg.CollectionID <= 0
}

// TMDBCollectionSourceID is the TMDB collection a collection syncs from, or
// false when its source is not a TMDB collection.
func TMDBCollectionSourceID(sourceConfig json.RawMessage) (int64, bool) {
	var cfg libraryCollectionSourceConfig
	if json.Unmarshal(sourceConfig, &cfg) != nil || cfg.Mode != "tmdb_collection" || cfg.CollectionID <= 0 {
		return 0, false
	}
	return int64(cfg.CollectionID), true
}

// libraryCollectionDiscoverConfig persists TMDB /discover filters in the
// collection's source_config JSON. Field names follow the TMDBDiscoverSpec
// JSON contract so the on-disk shape matches the template spec.
type libraryCollectionDiscoverConfig struct {
	WithGenres       []int    `json:"with_genres,omitempty"`
	WithoutGenres    []int    `json:"without_genres,omitempty"`
	SortBy           string   `json:"sort_by"`
	VoteCountGte     int      `json:"vote_count_gte,omitempty"`
	VoteAverageGte   float64  `json:"vote_average_gte,omitempty"`
	ReleaseDateGte   string   `json:"release_date_gte,omitempty"`
	ReleaseDateLte   string   `json:"release_date_lte,omitempty"`
	Certifications   []string `json:"certifications,omitempty"`
	CertificationLte string   `json:"certification_lte,omitempty"`
	WithRuntimeGte   int      `json:"with_runtime_gte,omitempty"`
	WithRuntimeLte   int      `json:"with_runtime_lte,omitempty"`
	OriginalLanguage string   `json:"original_language,omitempty"`
}

type mdblistEntry struct {
	ID          int    `json:"id"`
	Rank        int    `json:"rank"`
	TVDBID      *int   `json:"tvdbid"`
	IMDbID      string `json:"imdb_id"`
	MediaType   string `json:"mediatype"`
	Title       string `json:"title"`
	ReleaseYear int    `json:"release_year"`
	Released    string `json:"released"`
}

func SourceEnablesVirtualPlayback(raw json.RawMessage) bool {
	return sourceEnablesVirtualPlayback(raw)
}

func sourceEnablesVirtualPlayback(raw json.RawMessage) bool {
	var cfg struct {
		VirtualPlayback bool `json:"virtual_playback"`
	}
	return json.Unmarshal(raw, &cfg) == nil && cfg.VirtualPlayback
}

func virtualPlaybackIdentityAvailable(mediaType, imdbID string, tmdbID, tvdbID int) bool {
	itemType := "movie"
	if mediaType == "show" || mediaType == "tv" || mediaType == "series" {
		itemType = "series"
	}
	item := &models.MediaItem{
		Type:   itemType,
		ImdbID: strings.TrimSpace(imdbID),
	}
	if tmdbID > 0 {
		item.TmdbID = strconv.Itoa(tmdbID)
	}
	if tvdbID > 0 {
		item.TvdbID = strconv.Itoa(tvdbID)
	}
	_, err := virtualPlaybackItemURI(item)
	return err == nil
}

// observeCollectionMovieReleaseRejection records a best-effort queue entry
// when a collection movie is rejected for release reasons, so the admin
// queue reflects all blocked work (not just registrar-originated blocks).
func (s *LibraryCollectionService) observeCollectionMovieReleaseRejection(ctx context.Context, ids []ReleaseIdentity, reason string) {
	if s == nil || s.collections == nil || s.collections.pool == nil || len(ids) == 0 || reason == "" {
		return
	}
	tx, err := s.collections.pool.Begin(ctx)
	if err != nil {
		slog.WarnContext(ctx, "collection release observation: failed to begin tx", "error", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort observation; commit failure is logged
	if err := recordReleaseMetadata(ctx, tx, ids, reason); err != nil {
		slog.WarnContext(ctx, "collection release observation: record failed", "reason", reason, "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.WarnContext(ctx, "collection release observation: commit failed", "reason", reason, "error", err)
	}
}

func (s *LibraryCollectionService) queueVirtualMetadataRefresh(contentID string) {
	contentID = strings.TrimSpace(contentID)
	if s == nil || s.RefreshVirtualItem == nil || contentID == "" {
		return
	}
	s.virtualRefreshOnce.Do(func() {
		s.virtualRefreshQueue = make(chan string, virtualMetadataRefreshQueue)
		for range virtualMetadataRefreshWorkers {
			go func() {
				for queuedID := range s.virtualRefreshQueue {
					ctx, cancel := context.WithTimeout(context.Background(), virtualMetadataRefreshTimeout)
					if err := s.RefreshVirtualItem(ctx, queuedID); err != nil {
						slog.WarnContext(ctx, "collection virtual metadata refresh failed",
							"component", "catalog", "content_id", queuedID, "error", err)
					} else if strings.HasPrefix(queuedID, "series-") && s.items != nil {
						if err := s.items.MaterializeVirtualPlaybackEpisodes(ctx, queuedID); err != nil {
							slog.WarnContext(ctx, "failed to materialize virtual episodes after refresh",
								"component", "catalog", "content_id", queuedID, "error", err)
						}
					}
					cancel()
				}
			}()
		}
	})
	select {
	case s.virtualRefreshQueue <- contentID:
	default:
		// Materialization inserted durable metadata_refresh_debt in the same
		// transaction, so saturation delays enrichment without losing it.
		slog.Warn("collection virtual metadata refresh queue is full",
			"component", "catalog", "content_id", contentID)
	}
}

func (s *LibraryCollectionService) materializeVirtualPlayback(ctx context.Context, collection *models.LibraryCollection, item *models.MediaItem) error {
	_, err := s.EnsureCollectionItemMaterializedWithOptions(ctx, collection, item, VirtualMaterializeOptions{RequireMembership: false})
	return err
}

// EnsureCollectionItemMaterialized ensures that a collection item has its virtual
// base files, released episodes (for series), and collection claims established
// atomically, requiring pre-existing collection membership.
func (s *LibraryCollectionService) EnsureCollectionItemMaterialized(ctx context.Context, collection *models.LibraryCollection, item *models.MediaItem) (*MaterializeResult, error) {
	return s.EnsureCollectionItemMaterializedWithOptions(ctx, collection, item, VirtualMaterializeOptions{RequireMembership: true})
}

// EnsureCollectionItemMaterializedWithOptions ensures that a collection item has its virtual
// base files, released episodes (for series), and collection claims established
// atomically with configurable membership requirements.
func (s *LibraryCollectionService) EnsureCollectionItemMaterializedWithOptions(
	ctx context.Context,
	collection *models.LibraryCollection,
	item *models.MediaItem,
	opts VirtualMaterializeOptions,
) (*MaterializeResult, error) {
	if s == nil {
		return nil, errors.New("library collection service is not configured")
	}
	if collection == nil || item == nil {
		return nil, errors.New("collection and item are required")
	}
	if item.Type != "movie" && item.Type != "series" {
		return nil, fmt.Errorf("item type %q is not eligible for virtual materialization", item.Type)
	}
	if !sourceEnablesVirtualPlayback(collection.SourceConfig) {
		return nil, ErrVirtualPlaybackDisabled
	}

	libraryIDs := collection.LibraryIDs
	if len(libraryIDs) == 0 && collection.LibraryID > 0 {
		libraryIDs = []int{collection.LibraryID}
	}
	if len(libraryIDs) == 0 {
		return nil, errors.New("collection has no target libraries configured")
	}

	tracker, _ := ctx.Value(collectionVirtualCreationTrackerKey{}).(*collectionVirtualCreationTracker)
	if item.Type == "movie" {
		relDate := ""
		if item.ReleaseDate != nil {
			relDate = *item.ReleaseDate
		}
		gate := s.releaseGate(ctx)
		// Decide over every known alias (scalars plus provider-table rows)
		// so a stored alias the source entry omitted cannot bypass an
		// override recorded against it.
		movieIDs, err := s.movieReleaseIdentities(ctx, item)
		if err != nil {
			return nil, err
		}
		// Single decision from the captured entries at one evaluation time.
		now := time.Now().UTC()
		opts.releaseSnapshot, err = captureReleaseOverrides(ctx, gate.overrides, movieIDs)
		if err != nil {
			return nil, err
		}
		opts.preparedExplicitly = true
		allowed, active := decideReleaseOverrides(now, opts.releaseSnapshot)
		released := allowed
		var lookupErr error
		if !active {
			// One eligibility rule across all paths: active overrides first,
			// then physical possession, then provider/date evidence.
			physical, err := s.hasPhysicalMovieFiles(ctx, item)
			if err != nil {
				return nil, err
			}
			if physical {
				released = true
			} else if isFutureDate(item.Year, relDate) {
				err := fmt.Errorf("%w: movie is not yet released", ErrProviderUnavailable)
				s.observeCollectionMovieReleaseRejection(ctx, movieIDs, "future_date")
				if tracker != nil {
					tracker.err = err
				}
				return nil, err
			} else {
				tmdbID, _ := strconv.Atoi(item.TmdbID)
				released, lookupErr = gate.lookupProvider(ctx, tmdbID)
			}
		}
		if lookupErr != nil || !released {
			err := lookupErr
			if err == nil {
				err = fmt.Errorf("%w: movie has no confirmed home release", ErrProviderUnavailable)
			}
			s.observeCollectionMovieReleaseRejection(ctx, movieIDs, "no_home_release")
			if tracker != nil {
				tracker.err = err
			}
			return nil, err
		}
	}
	var variants []VirtualPlaybackVariant
	if s.VirtualVariants != nil {
		uri, err := virtualPlaybackItemURI(item)
		if err != nil {
			return nil, err
		}
		variants, err = s.configuredVirtualVariants(ctx, uri, item.Type)
		if err != nil {
			// Provider variant resolution is member-scoped: this item has no
			// playable variants right now, but the rest of the collection can
			// still sync. Callers record the failure as a run warning and skip
			// the member, and the next sync retries it. Do not poison the
			// collection run through tracker.err.
			return nil, fmt.Errorf("%w: %w", ErrMemberVariantsUnavailable, err)
		}
	}
	if len(variants) == 0 {
		// Same member-scoped outcome as a failed provider call: this item
		// resolved no variants, so skip it without failing the collection.
		return nil, fmt.Errorf("%w: no variants resolved for source", ErrMemberVariantsUnavailable)
	}
	if tracker != nil {
		if tracker.items == nil {
			tracker.items = make(map[string]preparedCollectionItem)
		}
		copyItem := *item
		tracker.items[item.ContentID] = preparedCollectionItem{item: &copyItem, variants: variants, releaseSnapshot: opts.releaseSnapshot}
		return &MaterializeResult{ContentID: item.ContentID, MediaType: item.Type}, nil
	}

	if s.items == nil {
		return nil, errors.New("library collection service items repository is not configured")
	}

	opts.sourceConfig = collection.SourceConfig
	res, err := s.items.EnsureVirtualCollectionItemMaterializedWithOptions(ctx, collection.ID, item, libraryIDs, variants, opts)
	if err != nil {
		return nil, err
	}

	s.queueVirtualMetadataRefresh(item.ContentID)
	return res, nil
}

// RepairVirtualPlaybackItem materializes a collection member that has catalog
// metadata but no provider-owned virtual media_files. It shares normal sync's
// profile/owner selection, then creates released episode placeholders for a
// series. It never resolves a provider URL, so it is safe to use as an admin
// recovery operation when provider configuration was unavailable during sync.
func (s *LibraryCollectionService) RepairVirtualPlaybackItem(ctx context.Context, collection *models.LibraryCollection, item *models.MediaItem) (*MaterializeResult, error) {
	return s.EnsureCollectionItemMaterialized(ctx, collection, item)
}

// ReconcileMissingCollectionVirtualItems discovers collection items that have no
// local files and no virtual base files, and ensures they are materialized.
// Recoverable errors on individual items are logged and aggregated without aborting the batch.
func (s *LibraryCollectionService) ReconcileMissingCollectionVirtualItems(ctx context.Context, collection *models.LibraryCollection) (int, error) {
	if s == nil || s.items == nil || collection == nil {
		return 0, nil
	}
	if !sourceEnablesVirtualPlayback(collection.SourceConfig) {
		return 0, nil
	}
	missingItems, err := s.items.FindCollectionItemsMissingVirtualBase(ctx, collection.ID, 50)
	if err != nil {
		return 0, fmt.Errorf("finding collection items missing virtual base: %w", err)
	}
	if len(missingItems) == 0 {
		return 0, nil
	}
	repairedCount := 0
	var batchErrors error
	for _, item := range missingItems {
		// Record reconciliation progress independently of virtual ownership freshness.
		if s.collections != nil {
			_ = s.collections.TouchCollectionItem(ctx, collection.ID, item.ContentID)
		}
		if s.items != nil {
			_ = s.items.TouchVirtualItemAttempt(ctx, item.ContentID)
		}
		if _, err := s.EnsureCollectionItemMaterialized(ctx, collection, item); err != nil {
			if errors.Is(err, ErrCollectionItemNotMember) {
				// Benign concurrent modification: item was removed from collection while batch was processing
				continue
			}
			slog.WarnContext(ctx, "failed to materialize missing collection virtual item during routine reconciliation",
				"component", "catalog", "collection_id", collection.ID, "content_id", item.ContentID, "error", err)
			batchErrors = errors.Join(batchErrors, fmt.Errorf("%s: %w", item.ContentID, err))
			continue
		}
		repairedCount++
	}
	return repairedCount, batchErrors
}

// CleanupLegacyUnscopedCollectionClaims executes a bounded cleanup of legacy unscoped
// collection claims that have no surviving membership evidence or that have been superseded
// by scoped claims.
func (s *LibraryCollectionService) CleanupLegacyUnscopedCollectionClaims(ctx context.Context, limit int) (int64, error) {
	if s == nil || s.items == nil {
		return 0, nil
	}
	return s.items.CleanupLegacyUnscopedCollectionClaims(ctx, limit)
}

func (s *LibraryCollectionService) SyncCollection(ctx context.Context, collectionID string) (*models.LibraryCollectionSyncRun, error) {
	return s.SyncCollectionWithOptions(ctx, collectionID, SyncCollectionOptions{})
}

func (s *LibraryCollectionService) SyncCollectionWithOptions(ctx context.Context, collectionID string, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	collection, err := s.collections.GetByID(ctx, collectionID)
	if err != nil {
		return nil, err
	}
	if IsLiveQueryType(collection.CollectionType) {
		return nil, ErrLibraryCollectionSyncUnsupported
	}
	reconciliationCtx := ctx
	tracker := &collectionVirtualCreationTracker{}
	ctx = context.WithValue(ctx, collectionVirtualCreationTrackerKey{}, tracker)
	ctx = context.WithValue(ctx, collectionVirtualVariantCacheKey{}, &collectionVirtualVariantCache{
		entries: make(map[string][]VirtualPlaybackVariant, 2),
	})

	var source libraryCollectionSourceConfig
	if err := json.Unmarshal(collection.SourceConfig, &source); err != nil {
		return nil, fmt.Errorf("parsing collection source config: %w", err)
	}

	syncOnce := func() (*models.LibraryCollectionSyncRun, error) {
		switch source.Mode {
		case libraryCollectionSourceModeSmart:
			return nil, ErrLibraryCollectionSyncUnsupported
		case libraryCollectionSourceModeMDBListJSON:
			return s.syncMDBListCollection(ctx, collection, collectionutil.MDBListURLCandidates(source.URL, collection.SourceURL), source.Limit, opts)
		case "tmdb_preset":
			return s.syncTMDBPresetCollection(ctx, collection, source, opts)
		case "tmdb_collection":
			return s.syncTMDBFranchiseCollection(ctx, collection, source, opts)
		case "tmdb_discover":
			return s.syncTMDBDiscoverCollection(ctx, collection, source, opts)
		case "tmdb_list":
			return s.syncTMDBListCollection(ctx, collection, source, opts)
		default:
			return nil, fmt.Errorf("%w: %s", ErrLibraryCollectionSyncModeUnsupported, source.Mode)
		}
	}
	run, err := retryCollectionSync(ctx, syncOnce)
	if err != nil && run == nil {
		// Any source path that returns before RecordSyncRun — an accept
		// failure, a provider error, or an exhausted retry — must still leave
		// a durable failed run so last_sync_status cannot keep reporting
		// success. Skipped when a run was already recorded (run != nil) to
		// avoid duplicate history rows. The insert uses a detached context
		// because the triggering context may already be done (e.g. the
		// scheduler's per-collection timeout).
		message := fmt.Sprintf("sync failed: %v", logredact.SanitizeURLError(err))
		if ctx.Err() != nil {
			message = fmt.Sprintf("sync context ended: %v", logredact.SanitizeURLError(err))
		}
		if _, recordErr := s.recordFailedCollectionSync(context.WithoutCancel(reconciliationCtx), collection.ID, syncTimestamp(), message); recordErr != nil {
			slog.ErrorContext(reconciliationCtx, "recording failed collection sync run",
				"component", "catalog",
				"collection_id", collection.ID,
				"error", recordErr,
			)
		}
	}
	if err == nil {
		if _, reconcileErr := s.ReconcileMissingCollectionVirtualItems(reconciliationCtx, collection); reconcileErr != nil {
			return run, fmt.Errorf("reconciling collection virtual items: %w", reconcileErr)
		}
	}

	return run, err
}

func (s *LibraryCollectionService) syncMDBListCollection(ctx context.Context, collection *models.LibraryCollection, listURLs []string, limit *int, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	startedAt := syncTimestamp()

	if len(listURLs) == 0 {
		return nil, fmt.Errorf("mdblist sync: url is required")
	}
	entries, err := s.fetchMDBListEntriesWithAPI(ctx, listURLs, limit)
	if err != nil {
		return nil, err
	}

	// Trim the entry list to the same fetch-multiplier bound used for TMDB
	// sources. MDBList lists can hold thousands of entries; without this,
	// the two GetByExternalIDs IN arrays balloon to the full list size even
	// when the user's limit is small.
	if fetchLimit := collectionutil.SourceFetchLimit(limit); fetchLimit > 0 && len(entries) > fetchLimit {
		entries = entries[:fetchLimit]
	}

	// Pre-fetch all external-ID lookups grouped by item type (movie vs series)
	// in two batched queries instead of up to 3×N GetByExternalID calls
	// (audit 2026-05-01 §3.7).
	var movieBatch, seriesBatch ExternalIDBatch
	for _, entry := range entries {
		itemType := mdbListEntryItemType(entry)
		if entry.ID > 0 {
			idStr := fmt.Sprintf("%d", entry.ID)
			if itemType == "movie" {
				movieBatch.TMDBIDs = append(movieBatch.TMDBIDs, idStr)
			} else {
				seriesBatch.TMDBIDs = append(seriesBatch.TMDBIDs, idStr)
			}
		}
		if entry.IMDbID != "" {
			if itemType == "movie" {
				movieBatch.IMDbIDs = append(movieBatch.IMDbIDs, entry.IMDbID)
			} else {
				seriesBatch.IMDbIDs = append(seriesBatch.IMDbIDs, entry.IMDbID)
			}
		}
		if entry.TVDBID != nil && *entry.TVDBID > 0 && itemType != "movie" {
			seriesBatch.TVDBIDs = append(seriesBatch.TVDBIDs, fmt.Sprintf("%d", *entry.TVDBID))
		}
	}

	movieLookup, err := s.items.GetByExternalIDs(ctx, movieBatch, "movie")
	if err != nil {
		return nil, err
	}
	seriesLookup, err := s.items.GetByExternalIDs(ctx, seriesBatch, "series")
	if err != nil {
		return nil, err
	}

	warnings := make([]string, 0)
	preparedVirtual := make(map[string]struct{})

	if sourceEnablesVirtualPlayback(collection.SourceConfig) {
		// MDBList fetches beyond the configured item limit to compensate for
		// duplicates and local misses. Do not materialize that entire lookahead
		// set: rows beyond the final collection limit would be left as orphaned
		// library items even though they can never become collection members.
		materializeEntries := entries
		if limit != nil && *limit > 0 && len(materializeEntries) > *limit {
			materializeEntries = materializeEntries[:*limit]
		}
		theatricalGate := s.releaseGate(ctx)
		preCandidateSet := make(map[string]struct{})
		for _, entry := range materializeEntries {
			itemType := mdbListEntryItemType(entry)
			lookup := movieLookup
			if itemType == "series" {
				lookup = seriesLookup
			}
			for _, c := range pickCandidatesByPriority(lookup, entry, itemType) {
				preCandidateSet[c] = struct{}{}
			}
		}
		preMembers := map[string]bool{}
		if len(preCandidateSet) > 0 && s.libraryItems != nil {
			preIDs := make([]string, 0, len(preCandidateSet))
			for id := range preCandidateSet {
				preIDs = append(preIDs, id)
			}
			var preErr error
			preMembers, preErr = s.libraryItems.GetItemsInFolders(ctx, preIDs, collection.LibraryIDs)
			if preErr != nil {
				return nil, preErr
			}
		}
		// The sequential loop below consults the theatrical gate only for
		// movie entries that reach it: a TMDB id, a non-future release date
		// (future entries short-circuit inside the gate without a lookup), no
		// matching catalog candidate (a candidate takes the repair/continue
		// path), and an available virtual identity. Prefetch exactly those ids
		// so the loop's per-movie TMDB lookups become memo hits rather than N
		// serial calls bounded at 5s each.
		prefetchSet := map[int]struct{}{}
		for _, entry := range materializeEntries {
			if mdbListEntryItemType(entry) != "movie" || entry.ID <= 0 {
				continue
			}
			if isFutureDate(entry.ReleaseYear, entry.Released) {
				continue
			}
			tvdbID := 0
			if entry.TVDBID != nil {
				tvdbID = *entry.TVDBID
			}
			if !virtualPlaybackIdentityAvailable(entry.MediaType, entry.IMDbID, entry.ID, tvdbID) {
				continue
			}
			if len(pickCandidatesByPriority(movieLookup, entry, "movie")) > 0 {
				continue
			}
			prefetchSet[entry.ID] = struct{}{}
		}
		if len(prefetchSet) > 0 {
			prefetchIDs := make([]int, 0, len(prefetchSet))
			for id := range prefetchSet {
				prefetchIDs = append(prefetchIDs, id)
			}
			theatricalGate.prefetch(ctx, prefetchIDs)
		}
		for _, entry := range materializeEntries {
			if mdbListEntryItemType(entry) != "movie" && isFutureDate(entry.ReleaseYear, entry.Released) {
				slog.DebugContext(ctx, "MDBList sync: skipping unreleased entry", "component", "catalog", "title", entry.Title, "year", entry.ReleaseYear)
				continue
			}
			tvdbID := 0
			if entry.TVDBID != nil {
				tvdbID = *entry.TVDBID
			}
			if !virtualPlaybackIdentityAvailable(entry.MediaType, entry.IMDbID, entry.ID, tvdbID) {
				continue
			}
			itemType := mdbListEntryItemType(entry)
			lookup := movieLookup
			if itemType == "series" {
				lookup = seriesLookup
			}
			if candidates := pickCandidatesByPriority(lookup, entry, itemType); len(candidates) > 0 {
				// An existing library-resident match wins; the second pass
				// accepts it and post-acceptance repair heals it if needed.
				// Only stage a new virtual candidate when no candidate is
				// already resident in a target library.
				resident := false
				for _, c := range candidates {
					if preMembers[c] {
						resident = true
						break
					}
				}
				if resident {
					continue
				}
				if s.items != nil {
					if existingItem, getErr := s.items.GetByID(ctx, candidates[0]); getErr != nil {
						warnings = append(warnings, fmt.Sprintf("checking existing virtual item %q: %v", candidates[0], getErr))
					} else if _, matErr := s.EnsureCollectionItemMaterializedWithOptions(ctx, collection, existingItem, VirtualMaterializeOptions{RequireMembership: false}); matErr != nil {
						warnings = append(warnings, fmt.Sprintf("repairing existing virtual item %q: %v", candidates[0], matErr))
					} else {
						preparedVirtual[candidates[0]] = struct{}{}
					}
				}
				continue
			}
			if itemType == "movie" && theatricalGate.skipTheatricalMovie(ctx, entry.ID, entry.IMDbID, entry.Title, entry.ReleaseYear, entry.Released) {
				slog.InfoContext(ctx, "MDBList sync: skipping theatrical-only movie", "component", "catalog", "title", entry.Title, "tmdb_id", entry.ID)
				continue
			}
			item := &models.MediaItem{
				Type: itemType, Title: entry.Title,
				SortTitle: entry.Title, Year: entry.ReleaseYear, ImdbID: entry.IMDbID,
				TmdbID: fmt.Sprintf("%d", entry.ID), Status: "matched",
			}
			if entry.Released != "" {
				item.ReleaseDate = &entry.Released
			}
			if entry.ID <= 0 {
				item.TmdbID = ""
			}
			if tvdbID > 0 {
				item.TvdbID = strconv.Itoa(tvdbID)
			}
			contentID, err := virtualPlaybackContentID(item)
			if err != nil {
				return nil, fmt.Errorf("building canonical virtual media id: %w", err)
			}
			item.ContentID = contentID
			if err := s.materializeVirtualPlayback(ctx, collection, item); err != nil {
				if ctx.Err() != nil {
					return nil, fmt.Errorf("materializing virtual item %q: %w", entry.Title, err)
				}
				slog.WarnContext(ctx, "failed to materialize virtual playback item for collection entry",
					"component", "catalog",
					"collection_id", collection.ID,
					"title", entry.Title,
					"error", err,
				)
				warnings = append(warnings, fmt.Sprintf("materializing virtual item %q: %v", entry.Title, err))
				continue
			}
			preparedVirtual[item.ContentID] = struct{}{}
			if item.ImdbID != "" {
				lookup.ByIMDb[item.ImdbID] = contentID
			}
			if item.TmdbID != "" {
				lookup.ByTMDB[item.TmdbID] = contentID
			}
			if item.TvdbID != "" {
				lookup.ByTVDB[item.TvdbID] = contentID
			}
		}
	}

	// First pass: collect ALL candidate content_ids per entry in priority
	// order. The legacy resolveMDBListEntry walked every external-ID hit and
	// returned the first library-resident match, so we must keep the full
	// candidate list (not just the highest-priority hit) for the membership
	// check below.
	type resolvedEntry struct {
		entryIndex int
		candidates []string
		sourceRank int
	}
	resolved := make([]resolvedEntry, 0, len(entries))
	candidateIDs := make([]string, 0, len(entries))
	candidateSet := make(map[string]struct{}, len(entries))
	matchedItems := make([]LibraryCollectionItemInput, 0, len(entries))

	for index, entry := range entries {
		itemType := mdbListEntryItemType(entry)
		var lookup *ExternalIDLookup
		if itemType == "movie" {
			lookup = movieLookup
		} else {
			lookup = seriesLookup
		}
		candidates := pickCandidatesByPriority(lookup, entry, itemType)
		if len(candidates) == 0 {
			continue
		}
		sourceRank := entry.Rank
		if sourceRank <= 0 {
			sourceRank = index + 1
		}
		resolved = append(resolved, resolvedEntry{entryIndex: index, candidates: candidates, sourceRank: sourceRank})
		for _, candidate := range candidates {
			if _, exists := candidateSet[candidate]; !exists {
				candidateSet[candidate] = struct{}{}
				candidateIDs = append(candidateIDs, candidate)
			}
		}
	}

	// Single batched library membership query covering EVERY candidate across
	// all entries (preserves the libraryID filter from the legacy
	// resolveMDBListEntry).
	libraryMembers, err := s.libraryItems.GetItemsInFolders(ctx, candidateIDs, collection.LibraryIDs)
	if err != nil {
		return nil, err
	}

	resolvedByIndex := make(map[int]resolvedEntry, len(resolved))
	for _, r := range resolved {
		resolvedByIndex[r.entryIndex] = r
	}

	scannedEntries := 0
	limitReached := false
	for index, entry := range entries {
		scannedEntries = index + 1
		r, ok := resolvedByIndex[index]
		if !ok {
			continue
		}
		var chosen string
		for _, candidate := range r.candidates {
			if libraryMembers[candidate] {
				chosen = candidate
				break
			}
		}
		if chosen == "" {
			for _, candidate := range r.candidates {
				if _, prepared := preparedVirtual[candidate]; prepared {
					chosen = candidate
					break
				}
			}
		}
		if chosen == "" {
			warnings = append(warnings, fmt.Sprintf("No match in libraries %v for %s", collection.LibraryIDs, entry.Title))
			continue
		}
		matchedItems = append(matchedItems, LibraryCollectionItemInput{
			MediaItemID: chosen,
			Position:    len(matchedItems),
			SourceRank:  r.sourceRank,
		})
		if collectionutil.ItemLimitReached(len(matchedItems), limit) {
			limitReached = true
			break
		}
	}

	acceptedItems, acceptanceWarnings, err := s.acceptCollectionItems(ctx, collection, matchedItems)
	if err != nil {
		return nil, err
	}
	matchedItems = acceptedItems
	warnings = appendUniqueCollectionWarnings(warnings, acceptanceWarnings)

	status := "success"
	if len(warnings) > 0 {
		status = "warning"
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return nil, fmt.Errorf("marshaling sync warnings: %w", err)
	}

	// Report the full source size as the denominator so operators can tell
	// "limit reached" apart from "perfect coverage of a small source"; the
	// extra clause exposes how many entries were actually scanned before the
	// break.
	message := fmt.Sprintf("Matched %d of %d entries", len(matchedItems), len(entries))
	if limitReached {
		message = fmt.Sprintf("%s (item limit reached after %d scanned)", message, scannedEntries)
	}

	completedAt := syncTimestamp()
	run, err := s.collections.RecordSyncRun(ctx, RecordLibraryCollectionSyncRunInput{
		CollectionID:   collection.ID,
		Status:         status,
		Message:        message,
		ItemsAdded:     len(matchedItems),
		ItemsRemoved:   0,
		ItemsMatched:   len(matchedItems),
		ItemsUnmatched: scannedEntries - len(matchedItems),
		Warnings:       warningsJSON,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	})
	if err != nil {
		return nil, err
	}

	if !opts.SkipCollage {
		s.MaybeGenerateCollage(ctx, collection.ID)
	}

	return run, nil
}

func (s *LibraryCollectionService) syncTMDBPresetCollection(ctx context.Context, collection *models.LibraryCollection, cfg libraryCollectionSourceConfig, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	startedAt := syncTimestamp()

	if s.TMDBCollections == nil {
		return nil, fmt.Errorf("TMDB preset sync requires configured TMDB access")
	}

	preset := cfg.Preset
	mediaType := cfg.MediaType
	timeWindow := cfg.TimeWindow
	if timeWindow == "" && preset == "trending" {
		timeWindow = "day"
	}
	if mediaType == "" {
		switch preset {
		case "trending":
			mediaType = "all"
		case "popular", "top_rated", "now_playing", "upcoming":
			mediaType = "movie"
		case "airing_today", "on_the_air":
			mediaType = "tv"
		}
	}

	fetchLimit := collectionutil.SourceFetchLimit(cfg.Limit)
	results, err := s.TMDBCollections.GetCollectionPreset(ctx, preset, mediaType, timeWindow, fetchLimit)
	if err != nil {
		return nil, fmt.Errorf("fetching TMDB preset: %w", err)
	}

	slog.InfoContext(ctx, "TMDB preset sync: fetched results", "component", "catalog",
		"collection_id", collection.ID,
		"preset", preset,
		"media_type", mediaType,
		"time_window", timeWindow,
		"count", len(results),
	)

	return s.completeTMDBEntrySync(ctx, collection, s.TMDBCollections, results, cfg.Limit, cfg.VirtualPlayback, startedAt, opts, "preset")
}

// syncTMDBFranchiseCollection populates a collection from TMDB's curated
// /collection/{id} franchise/saga endpoint. Items are written in TMDB's
// returned order (typically chronological by release date) without any
// re-sorting; the matcher resolves each part against the local library via
// TMDB / IMDb / TVDB IDs.
//
// A configured CollectionID of 0 is treated as a placeholder sentinel — the
// generic "TMDB Franchise" catalog template ships with collection_id=0 so
// admins can edit it after apply. Sync records a failed run with a clear
// message rather than fetching collection 0 (which TMDB does not have).
// validateTMDBFranchiseConfig returns a non-empty admin-facing failure message
// when the collection_id field is unsuitable for sync (0 = placeholder, < 0 =
// programmer error). Extracted as a pure helper so the message format can be
// unit-tested without spinning up the full sync stack.
func validateTMDBFranchiseConfig(collectionID int) string {
	switch {
	case collectionID == 0:
		return "TMDB franchise template requires a collection_id — edit the collection's source config and supply a real TMDB collection ID"
	case collectionID < 0:
		return fmt.Sprintf("TMDB collection_id must be > 0 (got %d)", collectionID)
	default:
		return ""
	}
}

func (s *LibraryCollectionService) syncTMDBFranchiseCollection(ctx context.Context, collection *models.LibraryCollection, cfg libraryCollectionSourceConfig, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	startedAt := syncTimestamp()

	if reason := validateTMDBFranchiseConfig(cfg.CollectionID); reason != "" {
		return s.recordFailedCollectionSync(ctx, collection.ID, startedAt, reason)
	}
	if s.TMDBFranchises == nil {
		return nil, fmt.Errorf("TMDB franchise sync requires configured TMDB access")
	}

	results, err := s.TMDBFranchises.GetCollection(ctx, cfg.CollectionID)
	if err != nil {
		return nil, fmt.Errorf("fetching TMDB collection: %w", err)
	}

	slog.InfoContext(ctx, "TMDB franchise sync: fetched results", "component", "catalog",
		"collection_id", collection.ID,
		"tmdb_collection_id", cfg.CollectionID,
		"count", len(results),
	)

	return s.completeTMDBEntrySync(ctx, collection, s.TMDBFranchises, results, cfg.Limit, cfg.VirtualPlayback, startedAt, opts, "collection")
}

// validateTMDBDiscoverConfig returns a non-empty admin-facing failure message
// when the source_config is unsuitable for the discover sync path. Extracted
// as a pure helper so the message format can be unit-tested without spinning
// up the full sync stack.
//
// Returns the normalized media_type alongside the failure message: when the
// config is valid, the second return is the canonical media_type to use.
func validateTMDBDiscoverConfig(cfg libraryCollectionSourceConfig) (string, string) {
	if cfg.Discover == nil {
		return "TMDB discover sync requires a discover spec — edit the collection's source config", ""
	}
	mediaType := strings.TrimSpace(cfg.MediaType)
	if mediaType == "" {
		// Default to "movie" if unset so older configs without an explicit
		// media_type still run; discover collections must pick one or the
		// other on the TMDB side.
		mediaType = "movie"
	}
	if mediaType != "movie" && mediaType != "tv" {
		return fmt.Sprintf("TMDB discover sync: unsupported media_type %q", mediaType), ""
	}
	if strings.TrimSpace(cfg.Discover.SortBy) == "" {
		return "TMDB discover sync: sort_by is required", ""
	}
	return "", mediaType
}

func (s *LibraryCollectionService) syncTMDBDiscoverCollection(ctx context.Context, collection *models.LibraryCollection, cfg libraryCollectionSourceConfig, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	startedAt := syncTimestamp()

	if s.TMDBDiscovers == nil {
		return nil, fmt.Errorf("TMDB discover sync requires configured TMDB access")
	}
	reason, mediaType := validateTMDBDiscoverConfig(cfg)
	if reason != "" {
		return s.recordFailedCollectionSync(ctx, collection.ID, startedAt, reason)
	}

	params := TMDBDiscoverParams{
		WithGenres:       cfg.Discover.WithGenres,
		WithoutGenres:    cfg.Discover.WithoutGenres,
		SortBy:           cfg.Discover.SortBy,
		VoteCountGte:     cfg.Discover.VoteCountGte,
		VoteAverageGte:   cfg.Discover.VoteAverageGte,
		ReleaseDateGte:   cfg.Discover.ReleaseDateGte,
		ReleaseDateLte:   cfg.Discover.ReleaseDateLte,
		Certifications:   cfg.Discover.Certifications,
		CertificationLte: cfg.Discover.CertificationLte,
		WithRuntimeGte:   cfg.Discover.WithRuntimeGte,
		WithRuntimeLte:   cfg.Discover.WithRuntimeLte,
		OriginalLanguage: cfg.Discover.OriginalLanguage,
	}

	fetchLimit := collectionutil.SourceFetchLimit(cfg.Limit)
	results, err := s.TMDBDiscovers.Discover(ctx, mediaType, params, fetchLimit)
	if err != nil {
		return nil, fmt.Errorf("fetching TMDB discover: %w", err)
	}

	slog.InfoContext(ctx, "TMDB discover sync: fetched results", "component", "catalog",
		"collection_id", collection.ID,
		"media_type", mediaType,
		"sort_by", params.SortBy,
		"count", len(results),
	)

	return s.completeTMDBEntrySync(ctx, collection, s.TMDBDiscovers, results, cfg.Limit, cfg.VirtualPlayback, startedAt, opts, "discover")
}

// syncTMDBListCollection populates a collection from a public, user-authored
// TMDB list. The list comes from cfg.URL (a themoviedb.org list page URL),
// falling back to the collection's source_url. Lists mix movies and shows and
// are synced in list order.
func (s *LibraryCollectionService) syncTMDBListCollection(ctx context.Context, collection *models.LibraryCollection, cfg libraryCollectionSourceConfig, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	startedAt := syncTimestamp()

	listURL := strings.TrimSpace(cfg.URL)
	if listURL == "" {
		listURL = strings.TrimSpace(collection.SourceURL)
	}
	listID, err := collectionutil.ParseTMDBListURL(listURL)
	if err != nil {
		return s.recordFailedCollectionSync(ctx, collection.ID, startedAt, "TMDB list sync: expected a URL like https://www.themoviedb.org/list/{id}")
	}
	if s.TMDBLists == nil {
		return nil, fmt.Errorf("TMDB list sync requires configured TMDB access")
	}

	results, err := s.TMDBLists.GetList(ctx, listID, collectionutil.SourceFetchLimit(cfg.Limit))
	if err != nil {
		return nil, fmt.Errorf("fetching TMDB list %d: %w", listID, err)
	}

	slog.InfoContext(ctx, "TMDB list sync: fetched results", "component", "catalog",
		"collection_id", collection.ID,
		"tmdb_list_id", listID,
		"count", len(results),
	)

	return s.completeTMDBEntrySync(ctx, collection, s.TMDBLists, results, cfg.Limit, cfg.VirtualPlayback, startedAt, opts, "list")
}

// completeTMDBEntrySync matches the entries fetcher returned against the
// collection's libraries in source order and records the sync. fetcher fills
// in external IDs for entries their TMDB ID leaves unmatched; see
// MatchTMDBEntries. Shared by every TMDB source mode; source names the mode in
// logs. virtualPlayback materializes a placeholder for an entry no library
// holds, subject to the release gate.
func (s *LibraryCollectionService) completeTMDBEntrySync(ctx context.Context, collection *models.LibraryCollection, fetcher any, results []TMDBCollectionEntry, limit *int, virtualPlayback bool, startedAt time.Time, opts SyncCollectionOptions, source string) (*models.LibraryCollectionSyncRun, error) { //nolint:funlen // upstream completeTMDBEntrySync exceeds Vio's 100-line limit; refactor tracked separately
	contentIDs, err := s.matchTMDBEntriesInLibraries(ctx, collection.LibraryIDs, fetcher, results)
	if err != nil {
		return nil, err
	}

	matchedItems := make([]LibraryCollectionItemInput, 0, len(results))
	seenContentIDs := make(map[string]int, len(results))
	warnings := make([]string, 0)
	unmatchedCount := 0
	duplicateCount := 0
	scannedEntries := 0
	limitReached := false
	theatricalGate := s.releaseGate(ctx)

	for i, entry := range results {
		scannedEntries = i + 1
		if entry.MediaType != "movie" && tmdbEntryIsUnreleased(entry) {
			slog.DebugContext(ctx, "TMDB sync: skipping unreleased entry", "component", "catalog",
				"source", source,
				"rank", i+1, "title", entry.Title, "release_date", entry.ReleaseDate)
			unmatchedCount++
			warnings = append(warnings, fmt.Sprintf("Skipped unreleased %s (release: %s)", entry.Title, entry.ReleaseDate))
			continue
		}
		contentID := contentIDs[i]
		if contentID == "" { //nolint:nestif // upstream completeTMDBEntrySync in library_collection_service.go exceeds Vio's nestif complexity-4 limit; refactor tracked separately
			// No library-resident match: the release gate applies only to
			// new virtual candidates.
			entryYear := tmdbReleaseYear(entry.ReleaseDate)
			if entry.MediaType == "movie" && theatricalGate.skipTheatricalMovie(ctx, entry.ID, entry.IMDbID, entry.Title, entryYear, entry.ReleaseDate) {
				unmatchedCount++
				warnings = append(warnings, fmt.Sprintf("Skipped theatrical-only movie %q (no digital release yet)", entry.Title))
				continue
			}
			if virtualPlayback && virtualPlaybackIdentityAvailable(entry.MediaType, entry.IMDbID, entry.ID, entry.TVDBID) {
				item, vErr := s.createVirtualCollectionItem(ctx, collection, entry.MediaType, entry.Title, entryYear, entry.IMDbID, entry.ID, entry.TVDBID, entry.ReleaseDate)
				if vErr != nil {
					if ctx.Err() != nil {
						return nil, vErr
					}
					slog.WarnContext(ctx, "failed to materialize virtual item for collection entry",
						"component", "catalog",
						"collection_id", collection.ID,
						"title", entry.Title,
						"error", vErr,
					)
					warnings = append(warnings, fmt.Sprintf("materializing virtual item %q: %v", entry.Title, vErr))
				} else if item != nil {
					contentID = item.ContentID
				}
			}
		}
		if contentID == "" {
			slog.DebugContext(ctx, "TMDB sync: no match", "component", "catalog",
				"source", source,
				"rank", i+1,
				"title", entry.Title,
				"type", entry.MediaType,
				"tmdb_id", entry.ID,
				"imdb_id", entry.IMDbID,
				"tvdb_id", entry.TVDBID,
			)
			unmatchedCount++
			continue
		}
		if firstRank, exists := seenContentIDs[contentID]; exists {
			slog.DebugContext(ctx, "TMDB sync: duplicate match skipped", "component", "catalog",
				"source", source,
				"rank", i+1,
				"title", entry.Title,
				"type", entry.MediaType,
				"tmdb_id", entry.ID,
				"content_id", contentID,
				"first_rank", firstRank,
			)
			duplicateCount++
			warnings = append(warnings, fmt.Sprintf("Duplicate TMDB entry for %s matched existing item %s", entry.Title, contentID))
			continue
		}
		seenContentIDs[contentID] = i + 1

		slog.DebugContext(ctx, "TMDB sync: matched", "component", "catalog",
			"source", source,
			"rank", i+1,
			"title", entry.Title,
			"type", entry.MediaType,
			"tmdb_id", entry.ID,
			"content_id", contentID,
		)
		matchedItems = append(matchedItems, LibraryCollectionItemInput{
			MediaItemID: contentID,
			Position:    len(matchedItems),
			SourceRank:  i + 1,
		})
		if collectionutil.ItemLimitReached(len(matchedItems), limit) {
			limitReached = true
			break
		}
	}

	slog.InfoContext(ctx, "TMDB sync: complete", "component", "catalog",
		"collection_id", collection.ID,
		"source", source,
		"matched", len(matchedItems),
		"unmatched", unmatchedCount,
		"duplicates", duplicateCount,
		"scanned", scannedEntries,
		"total", len(results),
	)

	return s.recordTMDBEntrySync(ctx, collection, tmdbEntrySyncTally{
		matched:      matchedItems,
		warnings:     warnings,
		unmatched:    unmatchedCount,
		duplicates:   duplicateCount,
		scanned:      scannedEntries,
		limitReached: limitReached,
	}, len(results), startedAt, opts)
}

// tmdbEntrySyncTally is what a TMDB sync found walking its entries.
type tmdbEntrySyncTally struct {
	matched      []LibraryCollectionItemInput
	warnings     []string
	unmatched    int
	duplicates   int
	scanned      int
	limitReached bool
}

// recordTMDBEntrySync stores a TMDB sync's members and its run record out of
// total fetched entries.
func (s *LibraryCollectionService) recordTMDBEntrySync(ctx context.Context, collection *models.LibraryCollection, tally tmdbEntrySyncTally, total int, startedAt time.Time, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	accepted, acceptanceWarnings, err := s.acceptCollectionItems(ctx, collection, tally.matched)
	if err != nil {
		return nil, err
	}
	droppedCount := len(tally.matched) - len(accepted)
	tally.matched = accepted
	tally.unmatched += droppedCount
	tally.warnings = appendUniqueCollectionWarnings(tally.warnings, acceptanceWarnings)

	status := "success"
	if len(tally.warnings) > 0 {
		status = "warning"
	}
	message := fmt.Sprintf("Matched %d of %d entries", len(tally.matched), total)
	if tally.limitReached {
		message = fmt.Sprintf("%s (item limit reached after %d scanned)", message, tally.scanned)
	}
	if tally.duplicates > 0 {
		message = fmt.Sprintf("%s (%d duplicates skipped)", message, tally.duplicates)
	}
	warningsJSON, _ := json.Marshal(tally.warnings)

	completedAt := syncTimestamp()
	run, err := s.collections.RecordSyncRun(ctx, RecordLibraryCollectionSyncRunInput{
		CollectionID:   collection.ID,
		Status:         status,
		Message:        message,
		ItemsAdded:     len(tally.matched),
		ItemsRemoved:   0,
		ItemsMatched:   len(tally.matched),
		ItemsUnmatched: tally.unmatched,
		Warnings:       warningsJSON,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	})
	if err != nil {
		return nil, err
	}

	if !opts.SkipCollage {
		s.MaybeGenerateCollage(ctx, collection.ID)
	}

	return run, nil
}

func appendUniqueCollectionWarnings(dest []string, extra []string) []string {
	seen := make(map[string]bool, len(dest))
	for _, w := range dest {
		seen[w] = true
	}
	for _, w := range extra {
		if !seen[w] {
			seen[w] = true
			dest = append(dest, w)
		}
	}
	return dest
}

func (s *LibraryCollectionService) recordFailedCollectionSync(ctx context.Context, collectionID string, startedAt time.Time, message string) (*models.LibraryCollectionSyncRun, error) {
	warningsJSON, err := json.Marshal([]string{message})
	if err != nil {
		return nil, fmt.Errorf("marshaling sync warnings: %w", err)
	}
	return s.collections.RecordSyncRun(ctx, RecordLibraryCollectionSyncRunInput{
		CollectionID: collectionID,
		Status:       "failed",
		Message:      message,
		Warnings:     warningsJSON,
		StartedAt:    startedAt,
		CompletedAt:  syncTimestamp(),
	})
}

func (s *LibraryCollectionService) createVirtualCollectionItem(ctx context.Context, collection *models.LibraryCollection, mediaType, title string, year int, imdbID string, tmdbID, tvdbID int, releaseDate ...string) (*models.MediaItem, error) {
	itemType := "movie"
	if mediaType == "show" || mediaType == "tv" || mediaType == "series" {
		itemType = "series"
	}
	item := &models.MediaItem{Type: itemType, Title: title, SortTitle: title, Year: year, ImdbID: strings.TrimSpace(imdbID), Status: "matched"}
	if len(releaseDate) > 0 && releaseDate[0] != "" {
		item.ReleaseDate = &releaseDate[0]
	}
	if tmdbID > 0 {
		item.TmdbID = fmt.Sprintf("%d", tmdbID)
	}
	if tvdbID > 0 {
		item.TvdbID = fmt.Sprintf("%d", tvdbID)
	}
	contentID, err := virtualPlaybackContentID(item)
	if err != nil {
		return nil, fmt.Errorf("building canonical virtual media ID for %q: %w", title, err)
	}
	item.ContentID = contentID
	_, err = s.EnsureCollectionItemMaterializedWithOptions(ctx, collection, item, VirtualMaterializeOptions{RequireMembership: false})
	if err != nil {
		return nil, fmt.Errorf("materializing virtual item %q: %w", title, err)
	}
	return item, nil
}

// resolveTMDBEntry finds a media item in the library matching a TMDB preset entry.
// It tries TMDB ID, IMDb ID, and TVDB ID (for TV shows) to maximize match rate.
func (s *LibraryCollectionService) resolveTMDBEntry(ctx context.Context, libraryIDs []int, entry TMDBCollectionEntry) (*models.MediaItem, error) {
	itemType := "movie"
	if entry.MediaType == "tv" {
		itemType = "series"
	}

	tmdbID := ""
	if entry.ID > 0 {
		tmdbID = fmt.Sprintf("%d", entry.ID)
	}
	tvdbID := ""
	if entry.TVDBID > 0 {
		tvdbID = fmt.Sprintf("%d", entry.TVDBID)
	}

	item, err := s.items.GetByExternalID(ctx, tmdbID, entry.IMDbID, tvdbID, itemType)
	if err != nil {
		if !errors.Is(err, ErrItemNotFound) {
			return nil, err
		}
		return nil, nil
	}

	membership, err := s.libraryItems.GetItemsInFolders(ctx, []string{item.ContentID}, libraryIDs)
	if err != nil {
		return nil, err
	}
	if !membership[item.ContentID] {
		return nil, nil
	}

	return item, nil
}

// matchTMDBEntriesInLibraries returns, for each entry in order, the
// content_id of the title it names in one of libraryIDs, or "" when it names
// none there.
func (s *LibraryCollectionService) matchTMDBEntriesInLibraries(ctx context.Context, libraryIDs []int, fetcher any, entries []TMDBCollectionEntry) ([]string, error) {
	contentIDs, err := MatchTMDBEntries(ctx, s.items, fetcher, entries)
	if err != nil {
		return nil, err
	}
	matched := make([]string, 0, len(contentIDs))
	for _, id := range contentIDs {
		if id != "" {
			matched = append(matched, id)
		}
	}
	inLibraries, err := s.libraryItems.GetItemsInFolders(ctx, matched, libraryIDs)
	if err != nil {
		return nil, err
	}
	for i, id := range contentIDs {
		if !inLibraries[id] {
			contentIDs[i] = ""
		}
	}
	return contentIDs, nil
}

// fetchMDBListEntries fetches a list from MDBList's public /json feed through
// the shared paging helper. The feed truncates at 2000 entries by default but
// honors undocumented limit/offset parameters, so paging is required for
// larger lists (see collectionutil.FetchMDBListJSONPaged).
//
// Entries are bounded by MaxExplicitItemLimit (100k); callers apply the
// user-facing fetch multiplier afterwards.
func (s *LibraryCollectionService) fetchMDBListEntries(ctx context.Context, listURL string) ([]mdblistEntry, error) {
	return collectionutil.FetchMDBListJSONPaged[mdblistEntry](ctx, s.httpClient, listURL, collectionutil.MaxExplicitItemLimit)
}

// fetchMDBListEntriesWithAPI fetches a list through the authenticated,
// paginated MDBList API when the fetcher is wired and the URL parses to a
// user/slug pair; otherwise it falls back to the public /json paged fetch.
//
// It deliberately does not fall back on every API error once a key is in play:
// a bad or expired key (ErrUnauthorized) must be visible rather than silently
// masked by the unauthenticated feed. The scheduler's per-collection deadline
// plus next_sync_at advance bound the blast radius of a persistent failure.
//
// These are treated as fallback conditions because /json is the authoritative
// source and the API path cannot safely produce a membership decision:
//   - ErrNotConfigured: no key (including a key cleared by the config watcher).
//   - ErrEmptyItemsWithTotal / ErrIncompleteItems: the API returned fewer items
//     than it says exist; blindly accepting would wipe or shrink membership.
//   - ErrListNotFound / ErrRateLimit: temporary or protocol-level problems the
//     public feed can still serve.
func (s *LibraryCollectionService) fetchMDBListEntriesWithAPI(ctx context.Context, listURLs []string, limit *int) ([]mdblistEntry, error) {
	if s.MDBListAPI != nil {
	apiLoop:
		for _, listURL := range listURLs {
			user, list, ok := collectionutil.ParseMDBListListURL(listURL)
			if !ok {
				continue
			}
			maxItems := collectionutil.SourceFetchLimit(limit)
			if maxItems <= 0 {
				maxItems = collectionutil.MaxExplicitItemLimit
			}
			items, err := s.MDBListAPI.ListItems(ctx, user, list, maxItems)
			switch {
			case err == nil:
				return apiListItemsToEntries(items), nil
			case isMDBListFallbackError(err):
				slog.WarnContext(ctx, "MDBList API fetch inconclusive; falling back to the public JSON feed",
					"component", "catalog",
					"user", user,
					"list", list,
					"error", err,
				)
				break apiLoop
			default:
				return nil, fmt.Errorf("fetching mdblist list %s/%s: %w", user, list, err)
			}
		}
	}
	return collectionutil.FetchMDBListWithFallback(listURLs, func(listURL string) ([]mdblistEntry, error) {
		return s.fetchMDBListEntries(ctx, listURL)
	})
}

// isMDBListFallbackError reports errors that should defer to the public /json
// feed instead of failing the sync. ErrUnauthorized and generic/parse errors
// are intentionally absent so a bad key stays visible.
func isMDBListFallbackError(err error) bool {
	for _, sentinel := range []error{
		mdblist.ErrNotConfigured,
		mdblist.ErrEmptyItemsWithTotal,
		mdblist.ErrIncompleteItems,
		mdblist.ErrListNotFound,
		mdblist.ErrRateLimit,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// apiListItemsToEntries maps authenticated API items onto the public-feed
// entry shape. The API's release_date fills Released so the future/ theatrical
// window keeps working; media_type stays as returned ("show" etc.) for
// mdbListEntryItemType to normalize.
func apiListItemsToEntries(items []mdblist.ListItem) []mdblistEntry {
	entries := make([]mdblistEntry, 0, len(items))
	for _, item := range items {
		var tvdbID *int
		if item.TVDBID != nil {
			id := *item.TVDBID
			tvdbID = &id
		}
		entries = append(entries, mdblistEntry{
			ID:          item.TMDBID,
			Rank:        item.Rank,
			TVDBID:      tvdbID,
			IMDbID:      item.IMDbID,
			MediaType:   item.MediaType,
			Title:       item.Title,
			ReleaseYear: item.ReleaseYear,
			Released:    item.ReleaseDate,
		})
	}
	return entries
}

// mdbListEntryItemType normalizes an MDBList entry's media_type field to the
// internal "movie"/"series" item type taxonomy.
func mdbListEntryItemType(entry mdblistEntry) string {
	switch strings.ToLower(entry.MediaType) {
	case "show", "tv", "series":
		return "series"
	default:
		return "movie"
	}
}

// pickCandidatesByPriority returns all matching content_ids in priority order
// (highest first), deduped. The caller resolves library membership against
// this list and picks the first hit — matching the legacy resolveMDBListEntry
// fallback semantic where any library-resident candidate wins regardless of
// which external ID resolved it.
//
// Priority order: for series TVDB > TMDB > IMDb, for movies TMDB > IMDb.
func pickCandidatesByPriority(lookup *ExternalIDLookup, entry mdblistEntry, itemType string) []string {
	if lookup == nil {
		return nil
	}
	var candidates []string
	seen := make(map[string]bool, 3)
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		candidates = append(candidates, id)
	}
	if itemType == "series" && entry.TVDBID != nil && *entry.TVDBID > 0 {
		add(lookup.ByTVDB[fmt.Sprintf("%d", *entry.TVDBID)])
	}
	if entry.ID > 0 {
		add(lookup.ByTMDB[fmt.Sprintf("%d", entry.ID)])
	}
	if entry.IMDbID != "" {
		add(lookup.ByIMDb[entry.IMDbID])
	}
	return candidates
}

//nolint:unused // Retained for compatibility with dormant integration paths.
func slugifyCollectionTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	title = strings.ReplaceAll(title, "'", "")
	var builder strings.Builder
	lastHyphen := false
	for _, r := range title {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			builder.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

// MaybeGenerateCollage builds the collage an unrestricted viewer sees after a
// sync, unless the collection has an uploaded or template poster, so the
// common case is ready before anyone asks. For a smart collection, saved
// rather than synced, it queues the build in the background. Restricted
// viewers' collages are built when first read (CollectionPosters). Errors are
// logged but never propagated so that sync operations are not blocked by
// collage failures.
func (s *LibraryCollectionService) MaybeGenerateCollage(ctx context.Context, collectionID string) {
	if s.CollageGen == nil {
		return
	}

	collection, err := s.collections.GetByID(ctx, collectionID)
	if err != nil {
		slog.WarnContext(ctx, "collage: failed to load collection", "component", "catalog", "collection_id", collectionID, "error", err)
		return
	}
	if _, assigned := AssignedCollectionPoster(collection); assigned {
		return
	}

	if collectionUsesLiveQuery(collection) {
		// Through the build queue, so a read of the saved collection that
		// asked for the same refresh shares this one.
		s.smartCollageSet().refreshLater(s.smartCollection(collection, AccessFilter{}), serverSmartCollageAccessKey(AccessFilter{}))
		return
	}

	if err := s.PrepareCollectionCollage(ctx, collectionID, AccessFilter{}); err != nil {
		if errors.Is(err, collage.ErrNotEnoughImages) {
			slog.DebugContext(ctx, "collage: not enough images", "component", "catalog", "collection_id", collectionID)
		} else {
			slog.WarnContext(ctx, "collage: poster generation failed", "component", "catalog", "collection_id", collectionID, "error", err)
		}
	}
}

func syncTimestamp() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

var (
	bracketYearRegex  = regexp.MustCompile(`[([{](19\d\d|20\d\d)[)\]}]`)
	trailingYearRegex = regexp.MustCompile(`(?:\s+-\s*|\s+)(19\d\d|20\d\d)\s*$`)
)

func extractTitleYear(title string) int {
	if m := bracketYearRegex.FindStringSubmatch(title); len(m) > 1 {
		if y, err := strconv.Atoi(m[1]); err == nil {
			return y
		}
	}
	if m := trailingYearRegex.FindStringSubmatch(title); len(m) > 1 {
		if y, err := strconv.Atoi(m[1]); err == nil {
			return y
		}
	}
	return 0
}

// tmdbEntryIsUnreleased reports whether a TMDB collection entry should be
// excluded from collection sync because it has not yet been released. Both
// movies and TV series are gated: for movies this is the primary_release_date,
// for TV it is the first_air_date. An empty or unparseable ReleaseDate is
// treated as unreleased to avoid surfacing placeholder entries that have no
// confirmed air/release date.
//
// When a title includes an explicit future or current release year (e.g. from
// its title "Fuze (2026)") that postdates a past festival premiere date (e.g.
// TIFF/Sundance), the entry is gated as unreleased so festival screenings do
// not bypass release gating.
func tmdbEntryIsUnreleased(entry TMDBCollectionEntry) bool {
	rd := strings.TrimSpace(entry.ReleaseDate)
	if len(rd) >= 10 {
		rd = rd[:10]
	}
	if rd == "" {
		return true // no release/air date known yet
	}
	t, err := time.Parse("2006-01-02", rd)
	if err != nil {
		if len(rd) == 4 {
			if y, yerr := strconv.Atoi(rd); yerr == nil {
				return y >= time.Now().UTC().Year()
			}
		}
		// Malformed date — treat as unreleased.
		return true
	}
	now := time.Now().UTC().Truncate(24 * time.Hour)
	if t.After(now) {
		return true
	}

	// Check if the title indicates a future or current theatrical release year
	// that postdates this premiere date (e.g. TIFF/Sundance festival premiere
	// in a prior year, or theatrical release later this year/next year).
	if titleYear := extractTitleYear(entry.Title); titleYear > 0 {
		if titleYear > now.Year() || (titleYear >= now.Year() && titleYear > t.Year()) {
			return true
		}
	}

	return false
}

// isUnreleasedYearOrDate reports whether a release year or release date is in
// the future relative to the server clock. A release date that cannot be
// parsed falls back to the release year comparison when one is available.
// If a movie has a release year indicating a current or future theatrical
// release that postdates a past festival premiere date (e.g. TIFF/Sundance),
// it is treated as unreleased so early festival screenings do not bypass
// release gating.
func isUnreleasedYearOrDate(year int, releaseDate string) bool {
	now := time.Now().UTC().Truncate(24 * time.Hour)
	currentYear := now.Year()

	if year > currentYear {
		return true
	}

	rd := strings.TrimSpace(releaseDate)
	if len(rd) >= 10 {
		rd = rd[:10]
	}
	if rd != "" {
		if t, err := time.Parse("2006-01-02", rd); err == nil {
			if t.After(now) {
				return true
			}
			// Future theatrical movie with past festival premiere date (e.g. TIFF/Sundance):
			// If the movie's release year is current or future, but the recorded date was from
			// a prior year, it was an early festival premiere and has not had its general release.
			if year >= currentYear && year > t.Year() {
				return true
			}
			return false
		}
		if len(rd) == 4 {
			if y, err := strconv.Atoi(rd); err == nil {
				return y >= currentYear
			}
		}
		// Unparseable non-empty date fails closed.
		return true
	}

	if year > 0 {
		return year >= currentYear
	}
	// Undated fails closed.
	return true
}

func tmdbReleaseYear(releaseDate string) int {
	rd := strings.TrimSpace(releaseDate)
	if len(rd) >= 4 {
		if y, err := strconv.Atoi(rd[:4]); err == nil && y > 0 {
			return y
		}
	}
	return 0
}
