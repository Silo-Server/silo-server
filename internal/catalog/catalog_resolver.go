package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

var ErrInvalidCatalogRequest = errors.New("invalid catalog request")
var ErrCatalogSourceNotFound = errors.New("catalog source not found")

type CatalogResult struct {
	Items      []*models.MediaItem
	Total      int
	HasMore    bool
	TotalExact bool
	SnapshotAt time.Time // pagination fence timestamp
	// Provider, Mode, SemanticUsed, FallbackReason and IndexPendingEvents are
	// per-query search diagnostics. They are only populated on the direct-search
	// path (where a CatalogSearchProvider actually ran); browse / preview /
	// grouped paths leave them zero-valued so the handler omits
	// search_diagnostics.
	Provider           string
	Mode               string
	SemanticUsed       bool
	FallbackReason     string
	IndexPendingEvents int
}

type CatalogFiltersResult struct {
	Genres            []string
	Studios           []string
	Networks          []string
	Countries         []string
	OriginalLanguages []string
	ContentRatings    []string
	Resolutions       []string
	AudioLanguages    []string
	SubtitleLanguages []string
	// Authors, Narrators and Series are book-native facets. Authors and
	// Narrators use item_people; Series uses the active book series table.
	// Video-only scopes return these empty.
	Authors   []string
	Narrators []string
	Series    []string
}

type CatalogFilterOptions struct {
	IncludeTechnical bool
}

// facetFetcher is the seam used by ListFiltersWithOptions to load each facet
// (genres, studios, networks, …). The production implementation queries the
// pgx pool; tests substitute a stub that records concurrent invocations.
type facetFetcher interface {
	DistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error)
	DistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error)
	Resolutions(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error)
	JSONBLanguages(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error)
	SubtitleLanguages(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error)
	// PeopleByKind returns distinct people names for the given PersonKind
	// across the scoped result set. Used for Authors / Narrators facets.
	PeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error)
	// AudiobookSeries returns distinct series_name values from the active
	// book series table joined onto the scoped result set.
	AudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error)
	// SearchDistinctArrayColumn prefix-searches an array-column facet
	// (genres, studios, networks, countries). Returns up to limit
	// alphabetical matches and hasMore=true when more would be available.
	SearchDistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error)
	// SearchDistinctScalarColumn is the scalar-column variant (e.g.
	// content_rating, original_language).
	SearchDistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error)
	// SearchPeopleByKind is the typeahead equivalent of PeopleByKind.
	SearchPeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error)
	// SearchAudiobookSeries is the typeahead equivalent of AudiobookSeries.
	SearchAudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error)
}

// pgxFacetFetcher is the production facetFetcher. It dispatches each method to
// the existing package-level helpers using the supplied pool.
type pgxFacetFetcher struct {
	pool *pgxpool.Pool
}

func (f *pgxFacetFetcher) DistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return listDistinctArrayColumnWithSource(ctx, f.pool, column, filters, baseRelation, mediaScope)
}

func (f *pgxFacetFetcher) DistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return listDistinctScalarColumnWithSource(ctx, f.pool, column, filters, baseRelation, mediaScope)
}

func (f *pgxFacetFetcher) Resolutions(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return listResolutionsWithSource(ctx, f.pool, filters, baseRelation, mediaScope)
}

func (f *pgxFacetFetcher) JSONBLanguages(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return listDistinctJSONBLanguageWithSource(ctx, f.pool, column, filters, baseRelation, mediaScope)
}

func (f *pgxFacetFetcher) SubtitleLanguages(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return listSubtitleLanguagesWithSource(ctx, f.pool, filters, baseRelation, mediaScope)
}

func (f *pgxFacetFetcher) PeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return listDistinctPeopleByKindWithSource(ctx, f.pool, kind, filters, baseRelation, mediaScope)
}

func (f *pgxFacetFetcher) AudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return listDistinctAudiobookSeriesWithSource(ctx, f.pool, filters, baseRelation, mediaScope)
}

func (f *pgxFacetFetcher) SearchDistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	return searchDistinctArrayColumnWithSource(ctx, f.pool, column, filters, baseRelation, mediaScope, prefix, limit)
}

func (f *pgxFacetFetcher) SearchDistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	return searchDistinctScalarColumnWithSource(ctx, f.pool, column, filters, baseRelation, mediaScope, prefix, limit)
}

func (f *pgxFacetFetcher) SearchPeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	return searchDistinctPeopleByKindWithSource(ctx, f.pool, kind, filters, baseRelation, mediaScope, prefix, limit)
}

func (f *pgxFacetFetcher) SearchAudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	return searchDistinctAudiobookSeriesWithSource(ctx, f.pool, filters, baseRelation, mediaScope, prefix, limit)
}

// previewExecutor is the seam consumed by previewQuerySource so tests can
// substitute a stub that records call counts without touching the database.
// The production implementation is *QueryExecutor.
type previewExecutor interface {
	PreviewPage(ctx context.Context, def QueryDefinition, access AccessFilter, limit int, offset int, includeTotal bool) ([]*models.MediaItem, int, bool, error)
}

type CatalogResolver struct {
	browseRepo     *BrowseRepository
	itemRepo       *ItemRepository
	episodeRepo    *EpisodeRepository
	searchProvider CatalogSearchProvider
	storeProvider  userstore.UserStoreProvider
	facets         facetFetcher
	// previewExecutorForScope, when non-nil, is used by previewQuerySource
	// instead of the default queryExecutorForScope. Tests inject a stub here
	// to observe how many times the executor is asked for a result page.
	previewExecutorForScope func(scope string, snapshot *time.Time) previewExecutor
}

func NewCatalogResolver(browseRepo *BrowseRepository, itemRepo *ItemRepository) *CatalogResolver {
	r := &CatalogResolver{
		browseRepo:     browseRepo,
		itemRepo:       itemRepo,
		searchProvider: NewPostgresSearchProvider(itemRepo),
	}
	if browseRepo != nil {
		r.facets = &pgxFacetFetcher{pool: browseRepo.pool}
	}
	return r
}

func (r *CatalogResolver) WithUserStoreProvider(provider userstore.UserStoreProvider) *CatalogResolver {
	if r == nil {
		return nil
	}
	r.storeProvider = provider
	return r
}

func (r *CatalogResolver) WithEpisodeRepository(repo *EpisodeRepository) *CatalogResolver {
	if r == nil {
		return nil
	}
	r.episodeRepo = repo
	return r
}

func (r *CatalogResolver) WithSearchProvider(provider CatalogSearchProvider) *CatalogResolver {
	if r == nil {
		return nil
	}
	if provider != nil {
		r.searchProvider = provider
	}
	return r
}

func (r *CatalogResolver) queryExecutorForScope(scope string, snapshot *time.Time) *QueryExecutor {
	return &QueryExecutor{
		Pool:            r.itemRepo.pool,
		Scope:           scope,
		BaseRelationSQL: catalogBaseRelationForScope(scope),
		SnapshotAt:      snapshot,
	}
}

func (r *CatalogResolver) Resolve(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	if r == nil || r.browseRepo == nil || r.itemRepo == nil {
		return nil, fmt.Errorf("catalog resolver requires browse and item repositories")
	}
	switch req.Source {
	case CatalogSourceQuery:
		if err := validateCatalogQueryRequest(req, strings.TrimSpace(access.ProfileID) != ""); err != nil {
			return nil, err
		}
		return r.resolveQuerySource(ctx, req, access)
	case CatalogSourceSection:
		if err := validateCatalogSectionRequest(req); err != nil {
			return nil, err
		}
		return r.resolveSectionSource(ctx, req, access)
	case CatalogSourceLibraryCollection:
		if err := validateCatalogCollectionRequest(req, strings.TrimSpace(access.ProfileID) != ""); err != nil {
			return nil, err
		}
		return r.resolveLibraryCollectionSource(ctx, req, access)
	case CatalogSourceUserCollection:
		if err := validateCatalogCollectionRequest(req, strings.TrimSpace(access.ProfileID) != ""); err != nil {
			return nil, err
		}
		return r.resolveUserCollectionSource(ctx, req, access)
	case CatalogSourceFavorites, CatalogSourceWatchlist, CatalogSourceHistory:
		if err := validateCatalogPersonalRequest(req); err != nil {
			return nil, err
		}
		return r.resolvePersonalSource(ctx, req, access)
	case CatalogSourcePerson:
		if err := validateCatalogPersonRequest(req); err != nil {
			return nil, err
		}
		return r.resolvePersonSource(ctx, req, access)
	default:
		return nil, fmt.Errorf("%w: unsupported catalog source %q", ErrInvalidCatalogRequest, req.Source)
	}
}

func (r *CatalogResolver) resolveQuerySource(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	if strings.TrimSpace(req.SearchQuery) == "" {
		return r.previewQuerySource(ctx, req, access)
	}

	if useDirectSearchPath(req) {
		return r.resolveDirectSearchSource(ctx, req, access)
	}

	items, err := r.fetchAllSearchCandidates(ctx, req, access)
	if err != nil {
		return nil, err
	}

	if NormalizeQuerySort(req.Query.Sort).Field == "relevance" {
		items = filterCatalogSearchItems(items, req.SearchQuery)
		items = filterCatalogNamePrefix(items, req.NamePrefix)
		items = filterCatalogItems(items, req.Query)
		total := len(items)
		paged := paginateCatalogItems(items, req.Offset, req.Limit)
		return &CatalogResult{
			Items:      paged,
			Total:      total,
			HasMore:    req.Offset+len(paged) < total,
			TotalExact: true,
		}, nil
	}
	return r.resolveCandidateItemsWithQuery(ctx, req, access, items, true)
}

func (r *CatalogResolver) resolveDirectSearchSource(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	searchAccess, itemTypes, earlyEmpty := catalogSearchAccess(req, access)
	if earlyEmpty {
		return &CatalogResult{Items: []*models.MediaItem{}, Total: 0, HasMore: false, TotalExact: true}, nil
	}

	provider := r.searchProvider
	if provider == nil {
		provider = NewPostgresSearchProvider(r.itemRepo)
	}
	result, err := provider.Search(ctx, CatalogSearchRequest{
		Query:     req.SearchQuery,
		ItemTypes: itemTypes,
		Limit:     req.Limit,
		Offset:    req.Offset,
		Access:    searchAccess,
		SkipTotal: req.SkipTotal,
	})
	if err != nil {
		return nil, fmt.Errorf("searching catalog items: %w", err)
	}

	return &CatalogResult{
		Items:              result.Items,
		Total:              result.Total,
		HasMore:            result.HasMore,
		TotalExact:         result.TotalExact,
		Provider:           result.Provider,
		Mode:               result.Mode,
		SemanticUsed:       result.SemanticUsed,
		FallbackReason:     result.FallbackReason,
		IndexPendingEvents: result.IndexPendingEvents,
	}, nil
}

func (r *CatalogResolver) previewQuerySource(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	snapshot := time.Now()
	if req.SnapshotAt != nil {
		snapshot = *req.SnapshotAt
	}
	var executor previewExecutor
	if r.previewExecutorForScope != nil {
		executor = r.previewExecutorForScope(req.Query.MediaScope, &snapshot)
	} else {
		executor = r.queryExecutorForScope(req.Query.MediaScope, &snapshot)
	}

	// Push NamePrefix into the SQL WHERE clause so the database can use
	// idx_media_items_sort_key instead of returning every match for an
	// in-memory filter pass.
	access.NamePrefix = req.NamePrefix

	items, total, hasMore, err := executor.PreviewPage(ctx, req.Query, access, req.Limit, req.Offset, !req.SkipTotal)
	if err != nil {
		return nil, err
	}
	return &CatalogResult{
		Items:      items,
		Total:      total,
		HasMore:    hasMore,
		TotalExact: !req.SkipTotal,
		SnapshotAt: snapshot,
	}, nil
}

func (r *CatalogResolver) resolveSectionSource(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	section, err := r.loadCatalogSection(ctx, req)
	if err != nil {
		return nil, err
	}

	switch section.SectionType {
	case "collection":
		// User collection takes precedence when set.
		if userCollID := strings.TrimSpace(section.UserCollectionID); userCollID != "" {
			return r.resolveUserCollectionSource(ctx, CatalogRequest{
				Source:         CatalogSourceUserCollection,
				CollectionID:   userCollID,
				Limit:          req.Limit,
				Offset:         req.Offset,
				SkipTotal:      req.SkipTotal,
				UseSourceOrder: true,
			}, access)
		}
		collectionID := strings.TrimSpace(section.CollectionID)
		if collectionID == "" {
			return &CatalogResult{Items: []*models.MediaItem{}, Total: 0, HasMore: false, TotalExact: true}, nil
		}
		return r.resolveLibraryCollectionSource(ctx, CatalogRequest{
			Source:         CatalogSourceLibraryCollection,
			CollectionID:   collectionID,
			Limit:          req.Limit,
			Offset:         req.Offset,
			SkipTotal:      req.SkipTotal,
			UseSourceOrder: true,
		}, access)
	case "favorites", "watchlist":
		source := CatalogSourceFavorites
		if section.SectionType == "watchlist" {
			source = CatalogSourceWatchlist
		}
		// Personal list sections may carry optional type/library filters and a
		// sort; apply them as an overlay query so the "see all" page matches
		// the rail. Without a configured sort the stored list order is kept.
		sectionFilters := parseCatalogSectionFilters(section.Config)
		query := QueryDefinition{
			MediaScope: sectionFilters.FilterType,
			LibraryIDs: append([]int(nil), sectionFilters.LibraryIDs...),
		}.Normalize()
		if section.Scope == "library" && section.LibraryID != nil {
			query.LibraryIDs = []int{*section.LibraryID}
		}
		useSourceOrder := true
		if qs, ok := NormalizePersonalListSort(parseCatalogSectionSort(section.Config)); ok {
			query.Sort = qs
			// added_at (date added to the list) is applied by
			// loadPersonalSourceIDs, which keeps the source order path;
			// metadata sorts go through the query executor instead.
			useSourceOrder = qs.Field == "added_at"
		}
		return r.resolvePersonalSource(ctx, CatalogRequest{
			Source:         source,
			Query:          query,
			Limit:          req.Limit,
			Offset:         req.Offset,
			SkipTotal:      req.SkipTotal,
			UseSourceOrder: useSourceOrder,
		}, access)
	case "recently_added":
		return r.resolveSectionBrowseSource(ctx, req, access, section, "added_at", "desc")
	case "recently_released":
		return r.resolveSectionBrowseSource(ctx, req, access, section, "release_date", "desc")
	case "random":
		return r.resolveSectionBrowseSource(ctx, req, access, section, "random", "desc")
	case "genre", "custom_filter":
		def, err := parseCatalogSectionQueryDefinition(section.Config)
		if err != nil {
			return nil, fmt.Errorf("%w: parsing section query definition: %v", ErrInvalidCatalogRequest, err)
		}
		if section.Scope == "library" && section.LibraryID != nil {
			def.LibraryIDs = []int{*section.LibraryID}
		}
		return r.resolveQuerySource(ctx, CatalogRequest{
			Source:    CatalogSourceQuery,
			Query:     def,
			Limit:     req.Limit,
			Offset:    req.Offset,
			SkipTotal: req.SkipTotal,
		}, stripCatalogUserScope(access))
	default:
		return &CatalogResult{Items: []*models.MediaItem{}, Total: 0, HasMore: false, TotalExact: true}, nil
	}
}

func (r *CatalogResolver) resolveSectionBrowseSource(ctx context.Context, req CatalogRequest, access AccessFilter, section catalogPageSection, sort, order string) (*CatalogResult, error) {
	snapshot := time.Now()
	if req.SnapshotAt != nil {
		snapshot = *req.SnapshotAt
	}

	sectionFilters := parseCatalogSectionFilters(section.Config)
	query := QueryDefinition{
		MediaScope: sectionFilters.FilterType,
		LibraryIDs: append([]int(nil), sectionFilters.LibraryIDs...),
		Sort: QuerySort{
			Field: sort,
			Order: order,
		},
	}.Normalize()
	if section.Scope == "library" && section.LibraryID != nil {
		query.LibraryIDs = []int{*section.LibraryID}
	}

	browseReq := CatalogRequest{
		Source:     CatalogSourceQuery,
		Query:      query,
		Limit:      req.Limit,
		Offset:     req.Offset,
		NamePrefix: req.NamePrefix,
		SnapshotAt: req.SnapshotAt,
	}
	browseAccess := stripCatalogUserScope(access)
	filters, earlyEmpty, err := catalogBrowseFilters(browseReq, browseAccess)
	if err != nil {
		return nil, err
	}
	if earlyEmpty {
		return &CatalogResult{Items: []*models.MediaItem{}, Total: 0, HasMore: false, TotalExact: true}, nil
	}
	filters.Sort = sort
	filters.Order = order
	filters.Limit = req.Limit
	filters.Offset = req.Offset
	filters.SnapshotAt = &snapshot

	result, err := r.browseRepo.BrowsePage(ctx, filters, !req.SkipTotal)
	if err != nil {
		return nil, fmt.Errorf("browsing section source: %w", err)
	}
	return &CatalogResult{
		Items:      result.Items,
		Total:      result.Total,
		HasMore:    result.HasMore,
		TotalExact: !req.SkipTotal,
		SnapshotAt: snapshot,
	}, nil
}

func (r *CatalogResolver) resolveLibraryCollectionSource(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	collectionRepo := NewLibraryCollectionRepository(r.itemRepo.pool)
	collection, err := collectionRepo.GetByID(ctx, req.CollectionID)
	if err != nil || collection.Visibility != "visible" {
		return nil, ErrCatalogSourceNotFound
	}

	if IsLiveQueryType(collection.CollectionType) {
		return r.resolveLiveLibraryCollectionSource(ctx, req, access, collection)
	}

	if catalogCollectionUsesLiveQuery(collection.QueryDefinition) {
		return r.resolveLiveLibraryCollectionSource(ctx, req, access, collection)
	}

	collectionItems, err := collectionRepo.ListItems(ctx, collection.ID)
	if err != nil {
		return nil, err
	}
	contentIDs := make([]string, 0, len(collectionItems))
	for _, item := range collectionItems {
		contentIDs = append(contentIDs, item.MediaItemID)
	}
	return r.resolveExactOrderedItems(ctx, contentIDs, req, access)
}

func (r *CatalogResolver) resolveLiveLibraryCollectionSource(ctx context.Context, req CatalogRequest, access AccessFilter, collection *models.LibraryCollection) (*CatalogResult, error) {
	def, err := parseCatalogCollectionQueryDefinition(collection.QueryDefinition)
	if err != nil {
		return nil, fmt.Errorf("%w: parsing library collection query_definition: %v", ErrInvalidCatalogRequest, err)
	}
	if len(collection.LibraryIDs) > 0 {
		def.LibraryIDs = intersectCatalogDefinitionLibraries(def.LibraryIDs, collection.LibraryIDs)
	} else if collection.LibraryID > 0 {
		def.LibraryIDs = intersectCatalogDefinitionLibraries(def.LibraryIDs, []int{collection.LibraryID})
	}
	def = ApplySmartCollectionItemLimit(def)
	if catalogRequestHasOverlay(req) {
		items, err := r.resolveCollectionQueryBaseItems(ctx, def, stripCatalogUserScope(access))
		if err != nil {
			return nil, err
		}
		return r.resolveExactOrderedMediaItems(ctx, items, req, access)
	}
	return r.resolveQuerySource(ctx, CatalogRequest{
		Source:    CatalogSourceQuery,
		Query:     def,
		Limit:     req.Limit,
		Offset:    req.Offset,
		SkipTotal: req.SkipTotal,
	}, stripCatalogUserScope(access))
}

func (r *CatalogResolver) resolveUserCollectionSource(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	store, err := r.catalogStoreForAccess(ctx, access)
	if err != nil {
		return nil, err
	}

	collection, err := store.GetCollection(ctx, req.CollectionID)
	if err != nil || !catalogProfileCanAccessCollection(collection, access.ProfileID) {
		return nil, ErrCatalogSourceNotFound
	}

	if IsLiveQueryType(collection.CollectionType) {
		def, err := parseCatalogCollectionQueryDefinition([]byte(collection.QueryDefinition))
		if err != nil {
			return nil, fmt.Errorf("%w: parsing user collection query_definition: %v", ErrInvalidCatalogRequest, err)
		}
		def = ApplySmartCollectionItemLimit(def)
		if catalogRequestHasOverlay(req) || strings.TrimSpace(collection.DisplayQueryDefinition) != "" {
			items, err := r.resolveCollectionQueryBaseItems(ctx, def, access)
			if err != nil {
				return nil, err
			}
			items, err = FilterCollectionItemsByDisplayQuery(ctx, r.itemRepo.pool, items, collection.DisplayQueryDefinition, access)
			if err != nil {
				return nil, err
			}
			return r.resolveExactOrderedMediaItems(ctx, items, req, access)
		}
		return r.resolveQuerySource(ctx, CatalogRequest{
			Source:    CatalogSourceQuery,
			Query:     def,
			Limit:     req.Limit,
			Offset:    req.Offset,
			SkipTotal: req.SkipTotal,
		}, access)
	}

	items, err := store.ListCollectionItems(ctx, collection.ID)
	if err != nil {
		return nil, err
	}
	contentIDs := make([]string, 0, len(items))
	for _, item := range items {
		contentIDs = append(contentIDs, item.MediaItemID)
	}
	mediaItems, err := r.fetchAccessibleItemsByID(ctx, contentIDs, catalogBaseCollectionRequest(req), access)
	if err != nil {
		return nil, err
	}
	mediaItems, err = FilterCollectionItemsByDisplayQuery(ctx, r.itemRepo.pool, mediaItems, collection.DisplayQueryDefinition, access)
	if err != nil {
		return nil, err
	}
	return r.resolveExactOrderedMediaItems(ctx, mediaItems, req, access)
}

func (r *CatalogResolver) resolvePersonalSource(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	if historySourceCanUseOptimizedPageQuery(req) {
		return r.resolveHistorySourcePage(ctx, req, access)
	}

	store, err := r.catalogStoreForAccess(ctx, access)
	if err != nil {
		return nil, err
	}

	contentIDs, err := r.loadPersonalSourceIDs(ctx, store, req, access.ProfileID)
	if err != nil {
		return nil, err
	}
	return r.resolveExactOrderedItems(ctx, contentIDs, req, access)
}

func (r *CatalogResolver) resolvePersonSource(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	filters, earlyEmpty, err := catalogBrowseFilters(req, access)
	if err != nil {
		return nil, err
	}
	if earlyEmpty {
		return &CatalogResult{Items: []*models.MediaItem{}, Total: 0, HasMore: false, TotalExact: true}, nil
	}
	filters.PersonID = req.PersonID

	items, err := r.fetchAllBrowseCandidatesByFilters(ctx, filters)
	if err != nil {
		return nil, err
	}
	return r.resolveCandidateItemsWithQuery(ctx, req, access, items, true)
}

func (r *CatalogResolver) resolveExactOrderedItems(ctx context.Context, contentIDs []string, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	fetchReq := req
	if req.Source == CatalogSourceLibraryCollection || req.Source == CatalogSourceUserCollection {
		fetchReq = catalogBaseCollectionRequest(req)
	}
	items, err := r.fetchAccessibleItemsByID(ctx, contentIDs, fetchReq, access)
	if err != nil {
		return nil, err
	}
	return r.resolveExactOrderedMediaItems(ctx, items, req, access)
}

func (r *CatalogResolver) resolveExactOrderedMediaItems(ctx context.Context, items []*models.MediaItem, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	req = normalizeExactCollectionOverlayRequest(req, items)
	items = filterCatalogSearchItems(items, req.SearchQuery)
	items = filterCatalogNamePrefix(items, req.NamePrefix)
	if req.UseSourceOrder {
		var err error
		items, err = r.filterExactSourceItemsByQuery(ctx, items, req.Query, access)
		if err != nil {
			return nil, err
		}
		total := len(items)
		paged := paginateCatalogItems(items, req.Offset, req.Limit)
		return &CatalogResult{
			Items:      paged,
			Total:      total,
			HasMore:    req.Offset+len(paged) < total,
			TotalExact: true,
		}, nil
	}
	return r.resolveCandidateItemsWithQuery(ctx, req, access, items, false)
}

func normalizeExactCollectionOverlayRequest(req CatalogRequest, items []*models.MediaItem) CatalogRequest {
	if req.Source != CatalogSourceLibraryCollection && req.Source != CatalogSourceUserCollection {
		return req
	}
	if !isEpisodeCatalogScope(req.Query.MediaScope) {
		return req
	}
	for _, item := range items {
		if item != nil && item.Type == "episode" {
			return req
		}
	}
	req.Query.MediaScope = ""
	return req
}

func (r *CatalogResolver) resolveCollectionQueryBaseItems(ctx context.Context, def QueryDefinition, access AccessFilter) ([]*models.MediaItem, error) {
	limit := DefaultSmartCollectionItemLimit
	if def.Limit != nil && *def.Limit > 0 {
		limit = *def.Limit
	}
	executor := r.queryExecutorForScope(def.MediaScope, nil)
	items, _, err := executor.Preview(ctx, def, access, limit)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *CatalogResolver) filterExactSourceItemsByQuery(ctx context.Context, items []*models.MediaItem, def QueryDefinition, access AccessFilter) ([]*models.MediaItem, error) {
	if !catalogQueryHasFilter(def) || len(items) == 0 {
		return items, nil
	}

	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil && strings.TrimSpace(item.ContentID) != "" {
			ids = append(ids, item.ContentID)
		}
	}
	if len(ids) == 0 {
		return []*models.MediaItem{}, nil
	}

	filterDef := def
	filterDef.Sort = QuerySort{}
	filterDef.Limit = nil
	queryAccess := access
	queryAccess.AllowedContentIDs = ids
	executor := r.queryExecutorForScope(filterDef.MediaScope, nil)
	matched, _, err := executor.Preview(ctx, filterDef, queryAccess, len(ids))
	if err != nil {
		return nil, err
	}

	matchedIDs := make(map[string]struct{}, len(matched))
	for _, item := range matched {
		if item != nil {
			matchedIDs[item.ContentID] = struct{}{}
		}
	}

	filtered := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		if _, ok := matchedIDs[item.ContentID]; ok {
			filtered = append(filtered, item)
		}
	}
	if def.Limit != nil && *def.Limit > 0 && len(filtered) > *def.Limit {
		filtered = filtered[:*def.Limit]
	}
	return filtered, nil
}

func (r *CatalogResolver) resolveCandidateItemsWithQuery(
	ctx context.Context,
	req CatalogRequest,
	access AccessFilter,
	items []*models.MediaItem,
	applyNamePrefixAfterQuery bool,
) (*CatalogResult, error) {
	contentIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.ContentID) == "" {
			continue
		}
		contentIDs = append(contentIDs, item.ContentID)
	}
	if len(contentIDs) == 0 {
		return &CatalogResult{Items: []*models.MediaItem{}, Total: 0, HasMore: false, TotalExact: true}, nil
	}

	queryAccess := access
	queryAccess.AllowedContentIDs = contentIDs

	executor := r.queryExecutorForScope(req.Query.MediaScope, nil)
	if applyNamePrefixAfterQuery && strings.TrimSpace(req.NamePrefix) != "" {
		sorted, _, err := executor.Preview(ctx, req.Query, queryAccess, len(contentIDs))
		if err != nil {
			return nil, err
		}
		sorted = filterCatalogNamePrefix(sorted, req.NamePrefix)
		total := len(sorted)
		paged := paginateCatalogItems(sorted, req.Offset, req.Limit)
		return &CatalogResult{
			Items:      paged,
			Total:      total,
			HasMore:    req.Offset+len(paged) < total,
			TotalExact: true,
		}, nil
	}

	limit := req.Limit
	if req.Offset > 0 {
		limit = req.Offset + req.Limit
	}
	sorted, total, err := executor.Preview(ctx, req.Query, queryAccess, limit)
	if err != nil {
		return nil, err
	}
	paged := paginateCatalogItems(sorted, req.Offset, req.Limit)
	return &CatalogResult{
		Items:      paged,
		Total:      total,
		HasMore:    req.Offset+len(paged) < total,
		TotalExact: true,
	}, nil
}

func (r *CatalogResolver) fetchAllBrowseCandidatesByFilters(ctx context.Context, filters BrowseFilters) ([]*models.MediaItem, error) {
	allItems := make([]*models.MediaItem, 0)
	page := filters
	page.Limit = 100
	page.Offset = 0
	page.Sort = "created_at"
	page.Order = "desc"

	for {
		result, err := r.browseRepo.Browse(ctx, page)
		if err != nil {
			return nil, fmt.Errorf("browsing catalog items: %w", err)
		}
		allItems = append(allItems, result.Items...)
		if len(allItems) >= result.Total || len(result.Items) == 0 {
			break
		}
		page.Offset += page.Limit
	}

	return allItems, nil
}

func (r *CatalogResolver) ListFilters(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogFiltersResult, error) {
	return r.ListFiltersWithOptions(ctx, req, access, CatalogFilterOptions{IncludeTechnical: true})
}

func (r *CatalogResolver) ListFiltersWithOptions(ctx context.Context, req CatalogRequest, access AccessFilter, options CatalogFilterOptions) (*CatalogFiltersResult, error) {
	if r == nil || r.browseRepo == nil {
		return nil, fmt.Errorf("catalog resolver requires a browse repository")
	}
	var (
		filters    BrowseFilters
		earlyEmpty bool
		err        error
	)
	switch req.Source {
	case CatalogSourceQuery:
		if err := validateCatalogQueryRequest(req, strings.TrimSpace(access.ProfileID) != ""); err != nil {
			return nil, err
		}
		filters, earlyEmpty, err = catalogBrowseFilters(req, access)
		if err != nil {
			return nil, err
		}
	case CatalogSourceFavorites, CatalogSourceWatchlist, CatalogSourceHistory:
		if err := validateCatalogPersonalRequest(req); err != nil {
			return nil, err
		}
		if access.UserID <= 0 || strings.TrimSpace(access.ProfileID) == "" {
			return nil, fmt.Errorf("%w: source %q requires active user scope", ErrInvalidCatalogRequest, "personal")
		}
		store, err := r.catalogStoreForAccess(ctx, access)
		if err != nil {
			return nil, err
		}
		contentIDs, err := r.loadPersonalSourceIDs(ctx, store, req, access.ProfileID)
		if err != nil {
			return nil, err
		}
		filters, earlyEmpty, err = catalogBrowseFilters(req, access)
		if err != nil {
			return nil, err
		}
		filters.ContentIDs = contentIDs
	case CatalogSourcePerson:
		if err := validateCatalogPersonRequest(req); err != nil {
			return nil, err
		}
		filters, earlyEmpty, err = catalogBrowseFilters(req, access)
		if err != nil {
			return nil, err
		}
		filters.PersonID = req.PersonID
	case CatalogSourceLibraryCollection, CatalogSourceUserCollection:
		if err := validateCatalogCollectionRequest(req, strings.TrimSpace(access.ProfileID) != ""); err != nil {
			return nil, err
		}
		contentIDs, err := r.loadCollectionSourceIDs(ctx, req, access)
		if err != nil {
			return nil, err
		}
		filters, earlyEmpty, err = catalogBrowseFilters(req, access)
		if err != nil {
			return nil, err
		}
		filters.ContentIDs = contentIDs
	default:
		return nil, fmt.Errorf("%w: source %q is not supported", ErrInvalidCatalogRequest, req.Source)
	}
	if earlyEmpty {
		return &CatalogFiltersResult{}, nil
	}

	if isEpisodeCatalogScope(req.Query.MediaScope) {
		return r.listFiltersForSource(ctx, filters, options, episodeCatalogBaseRelation, req.Query.MediaScope)
	}

	return r.listFiltersForSource(ctx, filters, options, "media_items mi", req.Query.MediaScope)
}

// CatalogFacetSearchResult is the typed return for SearchFacet. Matches
// is in alphabetical order; HasMore is true when the underlying result
// set held more rows than the supplied limit.
type CatalogFacetSearchResult struct {
	Matches []string
	HasMore bool
}

// catalogFacetSearchMaxLimit caps how many matches a single typeahead
// query can request. 50 is enough for a dropdown without overwhelming
// the page; clients that need more pages should narrow the prefix.
const catalogFacetSearchMaxLimit = 50

// SearchFacet powers /api/v1/catalog/filters/search. The request scope
// (libraries / access / media scope) mirrors ListFiltersWithOptions;
// the additional facet + prefix + limit arguments come from the URL.
// Empty or whitespace-only prefix returns no matches — the caller
// should fall back to the bulk /catalog/filters endpoint for the
// initial dropdown render.
func (r *CatalogResolver) SearchFacet(ctx context.Context, req CatalogRequest, access AccessFilter, facet string, prefix string, limit int) (*CatalogFacetSearchResult, error) {
	if r == nil || r.browseRepo == nil {
		return nil, fmt.Errorf("catalog resolver requires a browse repository")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return &CatalogFacetSearchResult{Matches: []string{}}, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > catalogFacetSearchMaxLimit {
		limit = catalogFacetSearchMaxLimit
	}

	var (
		filters    BrowseFilters
		earlyEmpty bool
		err        error
	)
	switch req.Source {
	case CatalogSourceQuery:
		if err := validateCatalogQueryRequest(req, strings.TrimSpace(access.ProfileID) != ""); err != nil {
			return nil, err
		}
		filters, earlyEmpty, err = catalogBrowseFilters(req, access)
		if err != nil {
			return nil, err
		}
	case CatalogSourceFavorites, CatalogSourceWatchlist, CatalogSourceHistory:
		if err := validateCatalogPersonalRequest(req); err != nil {
			return nil, err
		}
		if access.UserID <= 0 || strings.TrimSpace(access.ProfileID) == "" {
			return nil, fmt.Errorf("%w: source %q requires active user scope", ErrInvalidCatalogRequest, "personal")
		}
		store, err := r.catalogStoreForAccess(ctx, access)
		if err != nil {
			return nil, err
		}
		contentIDs, err := r.loadPersonalSourceIDs(ctx, store, req, access.ProfileID)
		if err != nil {
			return nil, err
		}
		filters, earlyEmpty, err = catalogBrowseFilters(req, access)
		if err != nil {
			return nil, err
		}
		filters.ContentIDs = contentIDs
	case CatalogSourcePerson:
		if err := validateCatalogPersonRequest(req); err != nil {
			return nil, err
		}
		filters, earlyEmpty, err = catalogBrowseFilters(req, access)
		if err != nil {
			return nil, err
		}
		filters.PersonID = req.PersonID
	case CatalogSourceLibraryCollection, CatalogSourceUserCollection:
		if err := validateCatalogCollectionRequest(req, strings.TrimSpace(access.ProfileID) != ""); err != nil {
			return nil, err
		}
		contentIDs, err := r.loadCollectionSourceIDs(ctx, req, access)
		if err != nil {
			return nil, err
		}
		filters, earlyEmpty, err = catalogBrowseFilters(req, access)
		if err != nil {
			return nil, err
		}
		filters.ContentIDs = contentIDs
	default:
		return nil, fmt.Errorf("%w: source %q is not supported", ErrInvalidCatalogRequest, req.Source)
	}
	if earlyEmpty {
		return &CatalogFacetSearchResult{Matches: []string{}}, nil
	}

	baseRelation := "media_items mi"
	mediaScope := req.Query.MediaScope
	if isEpisodeCatalogScope(req.Query.MediaScope) {
		baseRelation = episodeCatalogBaseRelation
	}

	facets := r.facets
	if facets == nil {
		facets = &pgxFacetFetcher{pool: r.browseRepo.pool}
	}

	matches, hasMore, err := dispatchFacetSearch(ctx, facets, facet, filters, baseRelation, mediaScope, prefix, limit)
	if err != nil {
		return nil, err
	}
	return &CatalogFacetSearchResult{Matches: matches, HasMore: hasMore}, nil
}

// dispatchFacetSearch routes a facet name to the right facetFetcher
// method. The set of supported facet names mirrors what
// /api/v1/catalog/filters returns; anything else is rejected as an
// invalid request so callers learn about typos at the API boundary.
func dispatchFacetSearch(ctx context.Context, facets facetFetcher, facet string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	switch facet {
	case "genre":
		return facets.SearchDistinctArrayColumn(ctx, "genres", filters, baseRelation, mediaScope, prefix, limit)
	case "studio":
		return facets.SearchDistinctArrayColumn(ctx, "studios", filters, baseRelation, mediaScope, prefix, limit)
	case "network":
		return facets.SearchDistinctArrayColumn(ctx, "networks", filters, baseRelation, mediaScope, prefix, limit)
	case "country":
		return facets.SearchDistinctArrayColumn(ctx, "countries", filters, baseRelation, mediaScope, prefix, limit)
	case "original_language":
		return facets.SearchDistinctScalarColumn(ctx, "original_language", filters, baseRelation, mediaScope, prefix, limit)
	case "content_rating":
		return facets.SearchDistinctScalarColumn(ctx, "content_rating", filters, baseRelation, mediaScope, prefix, limit)
	case "author":
		return facets.SearchPeopleByKind(ctx, models.PersonKindAuthor, filters, baseRelation, mediaScope, prefix, limit)
	case "narrator":
		if mediaScope == "ebook" {
			return nil, false, fmt.Errorf("%w: narrator facet is not available for ebook scope", ErrInvalidCatalogRequest)
		}
		return facets.SearchPeopleByKind(ctx, models.PersonKindNarrator, filters, baseRelation, mediaScope, prefix, limit)
	case "series":
		return facets.SearchAudiobookSeries(ctx, filters, baseRelation, mediaScope, prefix, limit)
	default:
		return nil, false, fmt.Errorf("%w: unknown facet %q", ErrInvalidCatalogRequest, facet)
	}
}

// catalogFacetConcurrency caps how many facet queries run in parallel for a
// single ListFiltersWithOptions invocation. With nine independent facet
// lookups and a default pgx pool of ten connections, capping at six leaves
// headroom for other concurrent work and avoids saturating the pool.
const catalogFacetConcurrency = 6

// catalogFacetMaxValues caps how many distinct values each facet query
// returns. Above ~1000 entries a typeahead UI is the only sensible way to
// present a dropdown — and the audiobook library on this server has
// >88k distinct authors / >92k narrators / >161k series, which produced
// an 11.7 MB /api/v1/catalog/filters response before this cap landed.
// The SearchableSelect dropdown stays responsive at 1000 client-side
// entries; beyond that, the filter response should switch to a server-
// side typeahead surface (out of scope for the cap-only fix).
const catalogFacetMaxValues = 1000

func (r *CatalogResolver) listFiltersForSource(
	ctx context.Context,
	filters BrowseFilters,
	options CatalogFilterOptions,
	baseRelation string,
	mediaScope string,
) (*CatalogFiltersResult, error) {
	facets := r.facets
	if facets == nil {
		facets = &pgxFacetFetcher{pool: r.browseRepo.pool}
	}

	var (
		genres            []string
		studios           []string
		networks          []string
		countries         []string
		originalLanguages []string
		contentRatings    []string
		resolutions       []string
		audioLanguages    []string
		subtitleLanguages []string
		authors           []string
		narrators         []string
		series            []string
	)

	eg, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, catalogFacetConcurrency)
	withLimit := func(fn func() error) func() error {
		return func() error {
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				return gctx.Err()
			}
			defer func() { <-sem }()
			return fn()
		}
	}

	eg.Go(withLimit(func() error {
		out, err := facets.DistinctArrayColumn(gctx, "genres", filters, baseRelation, mediaScope)
		if err != nil {
			return fmt.Errorf("listing catalog genres: %w", err)
		}
		genres = out
		return nil
	}))
	eg.Go(withLimit(func() error {
		out, err := facets.DistinctArrayColumn(gctx, "studios", filters, baseRelation, mediaScope)
		if err != nil {
			return fmt.Errorf("listing catalog studios: %w", err)
		}
		studios = out
		return nil
	}))
	eg.Go(withLimit(func() error {
		out, err := facets.DistinctArrayColumn(gctx, "networks", filters, baseRelation, mediaScope)
		if err != nil {
			return fmt.Errorf("listing catalog networks: %w", err)
		}
		networks = out
		return nil
	}))
	eg.Go(withLimit(func() error {
		out, err := facets.DistinctArrayColumn(gctx, "countries", filters, baseRelation, mediaScope)
		if err != nil {
			return fmt.Errorf("listing catalog countries: %w", err)
		}
		countries = out
		return nil
	}))
	eg.Go(withLimit(func() error {
		out, err := facets.DistinctScalarColumn(gctx, "original_language", filters, baseRelation, mediaScope)
		if err != nil {
			return fmt.Errorf("listing catalog original languages: %w", err)
		}
		originalLanguages = out
		return nil
	}))
	eg.Go(withLimit(func() error {
		out, err := facets.DistinctScalarColumn(gctx, "content_rating", filters, baseRelation, mediaScope)
		if err != nil {
			return fmt.Errorf("listing catalog content ratings: %w", err)
		}
		contentRatings = out
		return nil
	}))
	eg.Go(withLimit(func() error {
		out, err := facets.PeopleByKind(gctx, models.PersonKindAuthor, filters, baseRelation, mediaScope)
		if err != nil {
			return fmt.Errorf("listing catalog authors: %w", err)
		}
		authors = out
		return nil
	}))
	if mediaScope != "ebook" {
		eg.Go(withLimit(func() error {
			out, err := facets.PeopleByKind(gctx, models.PersonKindNarrator, filters, baseRelation, mediaScope)
			if err != nil {
				return fmt.Errorf("listing catalog narrators: %w", err)
			}
			narrators = out
			return nil
		}))
	}
	eg.Go(withLimit(func() error {
		out, err := facets.AudiobookSeries(gctx, filters, baseRelation, mediaScope)
		if err != nil {
			return fmt.Errorf("listing catalog audiobook series: %w", err)
		}
		series = out
		return nil
	}))

	if options.IncludeTechnical {
		eg.Go(withLimit(func() error {
			out, err := facets.Resolutions(gctx, filters, baseRelation, mediaScope)
			if err != nil {
				return fmt.Errorf("listing catalog resolutions: %w", err)
			}
			resolutions = out
			return nil
		}))
		eg.Go(withLimit(func() error {
			out, err := facets.JSONBLanguages(gctx, "audio_tracks", filters, baseRelation, mediaScope)
			if err != nil {
				return fmt.Errorf("listing catalog audio languages: %w", err)
			}
			audioLanguages = out
			return nil
		}))
		eg.Go(withLimit(func() error {
			out, err := facets.SubtitleLanguages(gctx, filters, baseRelation, mediaScope)
			if err != nil {
				return fmt.Errorf("listing catalog subtitle languages: %w", err)
			}
			subtitleLanguages = out
			return nil
		}))
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	result := &CatalogFiltersResult{
		Genres:            genres,
		Studios:           studios,
		Networks:          networks,
		Countries:         countries,
		OriginalLanguages: originalLanguages,
		ContentRatings:    contentRatings,
		Authors:           authors,
		Narrators:         narrators,
		Series:            series,
	}
	if options.IncludeTechnical {
		result.Resolutions = resolutions
		result.AudioLanguages = audioLanguages
		result.SubtitleLanguages = subtitleLanguages
	}
	return result, nil
}

func validateCatalogQueryRequest(req CatalogRequest, allowPersonalizedSorts bool) error {
	if req.Source != CatalogSourceQuery {
		return fmt.Errorf("%w: source %q is not supported yet", ErrInvalidCatalogRequest, req.Source)
	}
	return validateCatalogOverlayQuery(
		req.SearchQuery,
		req.Query,
		catalogQueryRuleFields,
		QuerySortFieldSet(allowPersonalizedSorts),
		true,
	)
}

func validateCatalogPersonalRequest(req CatalogRequest) error {
	switch req.Source {
	case CatalogSourceFavorites, CatalogSourceWatchlist, CatalogSourceHistory:
	default:
		return fmt.Errorf("%w: source %q is not supported", ErrInvalidCatalogRequest, req.Source)
	}
	return validateCatalogOverlayQuery(req.SearchQuery, req.Query, catalogPersonalRuleFields, catalogPersonalSortFields(), false)
}

func validateCatalogPersonRequest(req CatalogRequest) error {
	if req.Source != CatalogSourcePerson {
		return fmt.Errorf("%w: source %q is not supported", ErrInvalidCatalogRequest, req.Source)
	}
	if req.PersonID <= 0 {
		return fmt.Errorf("%w: person_id is required", ErrInvalidCatalogRequest)
	}
	return validateCatalogOverlayQuery(req.SearchQuery, req.Query, catalogQueryRuleFields, catalogQuerySortFields(), false)
}

func validateCatalogSectionRequest(req CatalogRequest) error {
	if req.Source != CatalogSourceSection {
		return fmt.Errorf("%w: source %q is not supported", ErrInvalidCatalogRequest, req.Source)
	}
	if strings.TrimSpace(req.SectionID) == "" {
		return fmt.Errorf("%w: section_id is required", ErrInvalidCatalogRequest)
	}
	if req.Scope != "home" && req.Scope != "library" {
		return fmt.Errorf("%w: scope must be 'home' or 'library'", ErrInvalidCatalogRequest)
	}
	if req.Scope == "library" && req.LibraryID <= 0 {
		return fmt.Errorf("%w: library_id is required for library sections", ErrInvalidCatalogRequest)
	}
	return nil
}

func validateCatalogExactCollectionRequest(req CatalogRequest) error {
	switch req.Source {
	case CatalogSourceLibraryCollection, CatalogSourceUserCollection:
	default:
		return fmt.Errorf("%w: source %q is not supported", ErrInvalidCatalogRequest, req.Source)
	}
	if strings.TrimSpace(req.CollectionID) == "" {
		return fmt.Errorf("%w: collection_id is required", ErrInvalidCatalogRequest)
	}
	return nil
}

func validateCatalogCollectionRequest(req CatalogRequest, allowPersonalizedSorts bool) error {
	if err := validateCatalogExactCollectionRequest(req); err != nil {
		return err
	}
	return validateCatalogOverlayQuery(req.SearchQuery, req.Query, catalogPersonalRuleFields, QuerySortFieldSet(allowPersonalizedSorts), false)
}

func catalogRequestHasOverlay(req CatalogRequest) bool {
	return strings.TrimSpace(req.SearchQuery) != "" ||
		strings.TrimSpace(req.NamePrefix) != "" ||
		catalogQueryHasFilter(req.Query) ||
		strings.TrimSpace(req.Query.Sort.Field) != ""
}

func catalogQueryHasFilter(def QueryDefinition) bool {
	return strings.TrimSpace(def.MediaScope) != "" ||
		len(def.LibraryIDs) > 0 ||
		len(def.Groups) > 0 ||
		def.Limit != nil
}

func validateCatalogOverlayQuery(searchQuery string, def QueryDefinition, ruleFields, sortFields map[string]bool, allowRelevance bool) error {
	if !IsValidMediaScope(def.MediaScope) {
		return fmt.Errorf("%w: media_scope must be 'movie', 'series', 'episode', 'audiobook', 'ebook', 'manga', or 'video'", ErrInvalidCatalogRequest)
	}
	if def.Match != "" && def.Match != "all" && def.Match != "any" {
		return fmt.Errorf("%w: match must be 'all' or 'any'", ErrInvalidCatalogRequest)
	}

	for _, id := range def.LibraryIDs {
		if id <= 0 {
			return fmt.Errorf("%w: library_ids must contain positive IDs", ErrInvalidCatalogRequest)
		}
	}

	for i, group := range def.Groups {
		if group.Match != "" && group.Match != "all" && group.Match != "any" {
			return fmt.Errorf("%w: groups[%d].match must be 'all' or 'any'", ErrInvalidCatalogRequest, i)
		}
		for j, rule := range group.Rules {
			if !ruleFields[rule.Field] {
				return fmt.Errorf("%w: groups[%d].rules[%d].field %q is not supported", ErrInvalidCatalogRequest, i, j, rule.Field)
			}
			def, ok := queryFieldDefs[rule.Field]
			if !ok || !def.validOps[rule.Op] {
				return fmt.Errorf("%w: groups[%d].rules[%d] is invalid", ErrInvalidCatalogRequest, i, j)
			}
		}
	}

	if def.Sort.Field == "" {
		return nil
	}
	if def.Sort.Order != "" && def.Sort.Order != "asc" && def.Sort.Order != "desc" {
		return fmt.Errorf("%w: sort.order must be 'asc' or 'desc'", ErrInvalidCatalogRequest)
	}
	if def.Sort.Field == "relevance" {
		if !allowRelevance {
			return fmt.Errorf("%w: relevance sort is only supported for query source", ErrInvalidCatalogRequest)
		}
		if strings.TrimSpace(searchQuery) == "" {
			return fmt.Errorf("%w: relevance sort requires q", ErrInvalidCatalogRequest)
		}
		return nil
	}
	if !sortFields[def.Sort.Field] {
		return fmt.Errorf("%w: sort.field %q is not supported", ErrInvalidCatalogRequest, def.Sort.Field)
	}

	return nil
}

var catalogQueryRuleFields = map[string]bool{
	"type":              true,
	"genre":             true,
	"year":              true,
	"rating_imdb":       true,
	"studio":            true,
	"network":           true,
	"country":           true,
	"original_language": true,
	"content_rating":    true,
	"added_at":          true,
	"release_date":      true,
	"status":            true,
	"actor":             true,
	"director":          true,
	"writer":            true,
	"producer":          true,
	"author":            true,
	"narrator":          true,
	"series":            true,
	"watched":           true,
	"favorited":         true,
	"in_watchlist":      true,
	"in_progress":       true,
	"last_watched":      true,
	"resolution":        true,
	"hdr":               true,
	"dolby_vision":      true,
	"bitrate":           true,
	"audio_language":    true,
	"subtitle_language": true,
}

var catalogPersonalRuleFields = map[string]bool{
	"type":              true,
	"genre":             true,
	"year":              true,
	"rating_imdb":       true,
	"studio":            true,
	"network":           true,
	"country":           true,
	"original_language": true,
	"content_rating":    true,
	"added_at":          true,
	"release_date":      true,
	"status":            true,
	"actor":             true,
	"director":          true,
	"writer":            true,
	"producer":          true,
	"author":            true,
	"narrator":          true,
	"series":            true,
	"watched":           true,
	"favorited":         true,
	"in_watchlist":      true,
	"in_progress":       true,
	"last_watched":      true,
	"resolution":        true,
	"hdr":               true,
	"dolby_vision":      true,
	"bitrate":           true,
	"audio_language":    true,
	"subtitle_language": true,
}

func catalogQuerySortFields() map[string]bool {
	return QuerySortFieldSet(false)
}

func catalogPersonalSortFields() map[string]bool {
	return QuerySortFieldSet(false)
}

func requiresAdvancedQueryExecution(def QueryDefinition) bool {
	if def.Sort.Field == "bitrate" {
		return true
	}
	for _, group := range def.Groups {
		for _, rule := range group.Rules {
			switch rule.Field {
			case "actor", "director", "writer", "producer", "author", "narrator", "series", "watched", "favorited", "in_watchlist", "in_progress", "last_watched", "resolution", "hdr", "dolby_vision", "bitrate", "audio_language", "subtitle_language":
				return true
			}
		}
	}
	return false
}

func useDirectSearchPath(req CatalogRequest) bool {
	if req.Source != CatalogSourceQuery {
		return false
	}
	if strings.TrimSpace(req.SearchQuery) == "" || strings.TrimSpace(req.NamePrefix) != "" {
		return false
	}
	if len(req.Query.Groups) > 0 || requiresAdvancedQueryExecution(req.Query) {
		return false
	}
	return req.Query.Sort.Field == "relevance" && req.Query.Sort.Order == "desc"
}

type catalogPageSection struct {
	ID               string
	Scope            string
	LibraryID        *int
	SectionType      string
	Title            string
	ItemLimit        int
	Config           json.RawMessage
	CollectionID     string // library collection ID
	UserCollectionID string // personal user collection ID
}

type catalogSectionFilters struct {
	FilterType       string `json:"filter_type"`
	FilterLibraryID  *int   `json:"filter_library_id"`
	FilterLibraryIDs []int  `json:"filter_library_ids"`
	LibraryIDs       []int
}

func defaultCatalogLibrarySection(libraryID int, sectionID string) (catalogPageSection, bool) {
	sections := []catalogPageSection{
		{
			ID:          "default-continue-watching",
			Scope:       "library",
			LibraryID:   &libraryID,
			SectionType: "continue_watching",
			Title:       "Continue Watching",
			ItemLimit:   20,
			Config:      json.RawMessage(`{}`),
		},
		{
			ID:          "default-recently-added",
			Scope:       "library",
			LibraryID:   &libraryID,
			SectionType: "recently_added",
			Title:       "Recently Added",
			ItemLimit:   20,
			Config:      json.RawMessage(`{}`),
		},
		{
			ID:          "default-recently-released",
			Scope:       "library",
			LibraryID:   &libraryID,
			SectionType: "recently_released",
			Title:       "Recently Released",
			ItemLimit:   20,
			Config:      json.RawMessage(`{}`),
		},
	}

	for _, section := range sections {
		if section.ID == sectionID {
			return section, true
		}
	}

	return catalogPageSection{}, false
}

func (r *CatalogResolver) loadCatalogSection(ctx context.Context, req CatalogRequest) (catalogPageSection, error) {
	var section catalogPageSection
	query := `
		SELECT id, scope, library_id, section_type, title, item_limit, config
		FROM page_sections
		WHERE id = $1 AND scope = $2 AND enabled = true
	`
	args := []any{req.SectionID, req.Scope}
	if req.Scope == "library" {
		query += " AND library_id = $3"
		args = append(args, req.LibraryID)
	} else {
		query += " AND library_id IS NULL"
	}

	err := r.itemRepo.pool.QueryRow(ctx, query, args...).Scan(
		&section.ID,
		&section.Scope,
		&section.LibraryID,
		&section.SectionType,
		&section.Title,
		&section.ItemLimit,
		&section.Config,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if req.Scope == "library" {
				if fallback, ok := defaultCatalogLibrarySection(req.LibraryID, req.SectionID); ok {
					return fallback, nil
				}
			}
			return catalogPageSection{}, ErrCatalogSourceNotFound
		}
		return catalogPageSection{}, err
	}

	var collectionCfg struct {
		LibraryCollectionID string `json:"library_collection_id"`
		UserCollectionID    string `json:"user_collection_id"`
	}
	if len(section.Config) > 0 {
		_ = json.Unmarshal(section.Config, &collectionCfg)
	}
	section.CollectionID = collectionCfg.LibraryCollectionID
	section.UserCollectionID = collectionCfg.UserCollectionID
	return section, nil
}

func parseCatalogSectionFilters(config json.RawMessage) catalogSectionFilters {
	var filters catalogSectionFilters
	if len(config) > 0 {
		_ = json.Unmarshal(config, &filters)
	}
	filters.LibraryIDs = normalizeCatalogSectionLibraryIDs(filters.FilterLibraryID, filters.FilterLibraryIDs)
	if filters.FilterType == "" && len(filters.LibraryIDs) == 0 {
		if def, err := parseCatalogSectionQueryDefinition(config); err == nil {
			filters.FilterType = def.MediaScope
			filters.LibraryIDs = append([]int(nil), def.LibraryIDs...)
		}
	}
	return filters
}

// parseCatalogSectionSort extracts the optional flat sort/order keys from a
// watchlist/favorites section config.
func parseCatalogSectionSort(config json.RawMessage) (string, string) {
	var cfg struct {
		Sort  string `json:"sort"`
		Order string `json:"order"`
	}
	if len(config) > 0 {
		_ = json.Unmarshal(config, &cfg)
	}
	return cfg.Sort, cfg.Order
}

func normalizeCatalogSectionLibraryIDs(single *int, multiple []int) []int {
	seen := map[int]struct{}{}
	result := make([]int, 0, len(multiple)+1)
	for _, id := range multiple {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	if single != nil && *single > 0 {
		if _, ok := seen[*single]; !ok {
			result = append(result, *single)
		}
	}
	return result
}

func parseCatalogSectionQueryDefinition(config json.RawMessage) (QueryDefinition, error) {
	return parseCatalogCollectionQueryDefinition(config)
}

func parseCatalogCollectionQueryDefinition(config json.RawMessage) (QueryDefinition, error) {
	if len(bytesTrimSpace(config)) == 0 || string(bytesTrimSpace(config)) == "{}" || string(bytesTrimSpace(config)) == "null" {
		return QueryDefinition{}.Normalize(), nil
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(config, &probe); err != nil {
		return QueryDefinition{}, err
	}
	if _, ok := probe["filter_type"]; ok {
		return NormalizeLegacySectionFilter(config)
	}
	if _, ok := probe["filter_library_id"]; ok {
		return NormalizeLegacySectionFilter(config)
	}
	if _, ok := probe["filter_library_ids"]; ok {
		return NormalizeLegacySectionFilter(config)
	}

	var def QueryDefinition
	if err := json.Unmarshal(config, &def); err != nil {
		return QueryDefinition{}, err
	}
	def = def.Normalize()
	if err := def.Validate(); err != nil {
		return QueryDefinition{}, err
	}
	return def, nil
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func catalogCollectionUsesLiveQuery(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "{}" && trimmed != "null"
}

func intersectCatalogDefinitionLibraries(existing, required []int) []int {
	if len(required) == 0 {
		return existing
	}
	if len(existing) == 0 {
		return append([]int(nil), required...)
	}
	return intersectInts(existing, required)
}

func stripCatalogUserScope(access AccessFilter) AccessFilter {
	access.UserID = 0
	access.ProfileID = ""
	return access
}

func (r *CatalogResolver) catalogStoreForAccess(ctx context.Context, access AccessFilter) (userstore.UserStore, error) {
	if r.storeProvider == nil || access.UserID <= 0 || strings.TrimSpace(access.ProfileID) == "" {
		return nil, fmt.Errorf("%w: source %q requires active user scope", ErrInvalidCatalogRequest, "personal")
	}
	store, err := r.storeProvider.ForUser(ctx, access.UserID)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, ErrCatalogSourceNotFound
	}
	return store, nil
}

func catalogProfileCanAccessCollection(collection *userstore.Collection, profileID string) bool {
	if collection == nil || strings.TrimSpace(profileID) == "" {
		return false
	}
	for _, allowed := range collection.AllowedProfileIDs {
		if allowed == profileID {
			return true
		}
	}
	return false
}

// PersonalListEntry pairs a list member with the timestamp it was added to
// the list (watchlist/favorites), as stored by the user store.
type PersonalListEntry struct {
	ID      string
	AddedAt string
}

// OrderPersonalListIDs returns the entry IDs, reordered by list added_at when
// that sort is requested; any other (or no) sort keeps the stored list order.
// AddedAt strings from one store share a format, so lexicographic comparison
// preserves chronology; entries with no timestamp sort last.
func OrderPersonalListIDs(entries []PersonalListEntry, qs QuerySort) []string {
	if qs.Field == "added_at" {
		asc := qs.Order == "asc"
		slices.SortStableFunc(entries, func(a, b PersonalListEntry) int {
			if (a.AddedAt == "") != (b.AddedAt == "") {
				if a.AddedAt != "" {
					return -1
				}
				return 1
			}
			cmp := strings.Compare(a.AddedAt, b.AddedAt)
			if !asc {
				cmp = -cmp
			}
			return cmp
		})
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

// watchlistVisibility builds the fully-watched-series display filter, falling
// back to a pool-backed episode repository when none was injected (mirroring
// the history path below).
func (r *CatalogResolver) watchlistVisibility() *WatchlistVisibility {
	episodes := r.episodeRepo
	if episodes == nil && r.itemRepo != nil {
		episodes = NewEpisodeRepository(r.itemRepo.pool)
	}
	return NewWatchlistVisibilityFromRepos(r.itemRepo, episodes)
}

func (r *CatalogResolver) loadPersonalSourceIDs(ctx context.Context, store userstore.UserStore, req CatalogRequest, profileID string) ([]string, error) {
	switch req.Source {
	case CatalogSourceFavorites:
		entries, err := store.ListFavorites(ctx, profileID, 10000, 0)
		if err != nil {
			return nil, err
		}
		listed := make([]PersonalListEntry, 0, len(entries))
		for _, entry := range entries {
			listed = append(listed, PersonalListEntry{ID: entry.MediaItemID, AddedAt: entry.AddedAt})
		}
		return OrderPersonalListIDs(listed, req.Query.Sort), nil
	case CatalogSourceWatchlist:
		entries, err := store.ListWatchlist(ctx, profileID, 10000, 0)
		if err != nil {
			return nil, err
		}
		entryIDs := make([]string, 0, len(entries))
		for _, entry := range entries {
			entryIDs = append(entryIDs, entry.MediaItemID)
		}
		hidden, err := r.watchlistVisibility().HiddenSeriesIDs(ctx, store, profileID, entryIDs)
		if err != nil {
			return nil, fmt.Errorf("filtering watched watchlist series: %w", err)
		}
		listed := make([]PersonalListEntry, 0, len(entries))
		for _, entry := range entries {
			if _, ok := hidden[entry.MediaItemID]; ok {
				continue
			}
			listed = append(listed, PersonalListEntry{ID: entry.MediaItemID, AddedAt: entry.AddedAt})
		}
		return OrderPersonalListIDs(listed, req.Query.Sort), nil
	case CatalogSourceHistory:
		entries, err := store.ListHistory(ctx, profileID, 10000, 0)
		if err != nil {
			return nil, err
		}
		// The episode scope shows history at episode granularity; every other
		// scope collapses episode watch events into their series display item.
		if isEpisodeCatalogScope(req.Query.MediaScope) {
			return HistoryEpisodeScopeIDs(entries), nil
		}
		return ResolveHistoryDisplayIDs(ctx, entries, NewEpisodeRepository(r.itemRepo.pool))
	default:
		return nil, fmt.Errorf("%w: source %q is not a personal source", ErrInvalidCatalogRequest, req.Source)
	}
}

func (r *CatalogResolver) loadCollectionSourceIDs(ctx context.Context, req CatalogRequest, access AccessFilter) ([]string, error) {
	items, err := r.loadCollectionSourceBaseItems(ctx, req, access)
	if err != nil {
		return nil, err
	}
	return contentIDsFromMediaItems(items), nil
}

func (r *CatalogResolver) loadCollectionSourceBaseItems(ctx context.Context, req CatalogRequest, access AccessFilter) ([]*models.MediaItem, error) {
	switch req.Source {
	case CatalogSourceLibraryCollection:
		collectionRepo := NewLibraryCollectionRepository(r.itemRepo.pool)
		collection, err := collectionRepo.GetByID(ctx, req.CollectionID)
		if err != nil || collection.Visibility != "visible" {
			return nil, ErrCatalogSourceNotFound
		}
		if IsLiveQueryType(collection.CollectionType) || catalogCollectionUsesLiveQuery(collection.QueryDefinition) {
			def, err := parseCatalogCollectionQueryDefinition(collection.QueryDefinition)
			if err != nil {
				return nil, fmt.Errorf("%w: parsing library collection query_definition: %v", ErrInvalidCatalogRequest, err)
			}
			if len(collection.LibraryIDs) > 0 {
				def.LibraryIDs = intersectCatalogDefinitionLibraries(def.LibraryIDs, collection.LibraryIDs)
			} else if collection.LibraryID > 0 {
				def.LibraryIDs = intersectCatalogDefinitionLibraries(def.LibraryIDs, []int{collection.LibraryID})
			}
			return r.resolveCollectionQueryBaseItems(ctx, ApplySmartCollectionItemLimit(def), stripCatalogUserScope(access))
		}

		collectionItems, err := collectionRepo.ListItems(ctx, collection.ID)
		if err != nil {
			return nil, err
		}
		contentIDs := make([]string, 0, len(collectionItems))
		for _, item := range collectionItems {
			contentIDs = append(contentIDs, item.MediaItemID)
		}
		return r.fetchAccessibleItemsByID(ctx, contentIDs, catalogBaseCollectionRequest(req), access)
	case CatalogSourceUserCollection:
		store, err := r.catalogStoreForAccess(ctx, access)
		if err != nil {
			return nil, err
		}
		collection, err := store.GetCollection(ctx, req.CollectionID)
		if err != nil || !catalogProfileCanAccessCollection(collection, access.ProfileID) {
			return nil, ErrCatalogSourceNotFound
		}
		var items []*models.MediaItem
		if IsLiveQueryType(collection.CollectionType) {
			def, err := parseCatalogCollectionQueryDefinition([]byte(collection.QueryDefinition))
			if err != nil {
				return nil, fmt.Errorf("%w: parsing user collection query_definition: %v", ErrInvalidCatalogRequest, err)
			}
			items, err = r.resolveCollectionQueryBaseItems(ctx, ApplySmartCollectionItemLimit(def), access)
			if err != nil {
				return nil, err
			}
		} else {
			collectionItems, err := store.ListCollectionItems(ctx, collection.ID)
			if err != nil {
				return nil, err
			}
			contentIDs := make([]string, 0, len(collectionItems))
			for _, item := range collectionItems {
				contentIDs = append(contentIDs, item.MediaItemID)
			}
			items, err = r.fetchAccessibleItemsByID(ctx, contentIDs, catalogBaseCollectionRequest(req), access)
			if err != nil {
				return nil, err
			}
		}
		return FilterCollectionItemsByDisplayQuery(ctx, r.itemRepo.pool, items, collection.DisplayQueryDefinition, access)
	default:
		return nil, fmt.Errorf("%w: source %q is not a collection source", ErrInvalidCatalogRequest, req.Source)
	}
}

func catalogBaseCollectionRequest(req CatalogRequest) CatalogRequest {
	return CatalogRequest{
		Source:         req.Source,
		CollectionID:   req.CollectionID,
		Limit:          req.Limit,
		Offset:         req.Offset,
		SkipTotal:      req.SkipTotal,
		UseSourceOrder: true,
	}
}

func contentIDsFromMediaItems(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.ContentID) == "" {
			continue
		}
		ids = append(ids, item.ContentID)
	}
	return ids
}

func (r *CatalogResolver) fetchAccessibleItemsByID(ctx context.Context, contentIDs []string, req CatalogRequest, access AccessFilter) ([]*models.MediaItem, error) {
	if len(contentIDs) == 0 {
		return []*models.MediaItem{}, nil
	}

	if isEpisodeCatalogScope(req.Query.MediaScope) {
		return r.fetchAccessibleEpisodeItemsByID(ctx, contentIDs, req, access)
	}

	filters, earlyEmpty, err := catalogBrowseFilters(req, access)
	if err != nil {
		return nil, err
	}
	if earlyEmpty {
		return []*models.MediaItem{}, nil
	}
	filters.ContentIDs = contentIDs

	items, err := r.fetchAllBrowseCandidatesByFilters(ctx, filters)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*models.MediaItem, len(items))
	for _, item := range items {
		byID[item.ContentID] = item
	}

	ordered := make([]*models.MediaItem, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		item, ok := byID[contentID]
		if !ok {
			continue
		}
		ordered = append(ordered, item)
	}
	return ordered, nil
}

// fetchAccessibleEpisodeItemsByID is the episode-scope counterpart of
// fetchAccessibleItemsByID. Episode rows are not present in media_items — they
// hydrate through the episode catalog relation — so the browse-repository path
// (which scans media_items and would match nothing) is replaced by the episode
// query executor with the requested ids as the allowed-content set. Query
// filters are applied in the same pass; sort and limit are stripped because
// the caller re-imposes source order or its own sort downstream.
func (r *CatalogResolver) fetchAccessibleEpisodeItemsByID(ctx context.Context, contentIDs []string, req CatalogRequest, access AccessFilter) ([]*models.MediaItem, error) {
	filterDef := req.Query
	filterDef.Sort = QuerySort{}
	filterDef.Limit = nil

	queryAccess := access
	if access.AllowedContentIDs != nil {
		queryAccess.AllowedContentIDs = intersectContentIDs(contentIDs, access.AllowedContentIDs)
	} else {
		queryAccess.AllowedContentIDs = contentIDs
	}

	executor := r.queryExecutorForScope(filterDef.MediaScope, nil)
	items, _, err := executor.Preview(ctx, filterDef, queryAccess, len(contentIDs))
	if err != nil {
		return nil, err
	}

	byID := make(map[string]*models.MediaItem, len(items))
	for _, item := range items {
		if item != nil {
			byID[item.ContentID] = item
		}
	}

	ordered := make([]*models.MediaItem, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		if item, ok := byID[contentID]; ok {
			ordered = append(ordered, item)
		}
	}
	return ordered, nil
}

func intersectContentIDs(ids []string, allowed []string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	intersection := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := allowedSet[id]; ok {
			intersection = append(intersection, id)
		}
	}
	return intersection
}

func (r *CatalogResolver) fetchAllBrowseCandidates(ctx context.Context, req CatalogRequest, access AccessFilter) ([]*models.MediaItem, error) {
	filters, earlyEmpty, err := catalogBrowseFilters(req, access)
	if err != nil {
		return nil, err
	}
	if earlyEmpty {
		return []*models.MediaItem{}, nil
	}

	allItems := make([]*models.MediaItem, 0)
	page := filters
	page.Limit = 100
	page.Offset = 0
	page.Sort = "created_at"
	page.Order = "desc"

	for {
		result, err := r.browseRepo.Browse(ctx, page)
		if err != nil {
			return nil, fmt.Errorf("browsing catalog items: %w", err)
		}
		allItems = append(allItems, result.Items...)
		if len(allItems) >= result.Total || len(result.Items) == 0 {
			break
		}
		page.Offset += page.Limit
	}

	return allItems, nil
}

func (r *CatalogResolver) fetchAllSearchCandidates(ctx context.Context, req CatalogRequest, access AccessFilter) ([]*models.MediaItem, error) {
	searchAccess, itemTypes, earlyEmpty := catalogSearchAccess(req, access)
	if earlyEmpty {
		return []*models.MediaItem{}, nil
	}

	_, total, err := r.itemRepo.Search(ctx, req.SearchQuery, itemTypes, 1, 0, searchAccess)
	if err != nil {
		return nil, fmt.Errorf("counting catalog search results: %w", err)
	}
	if total == 0 {
		return []*models.MediaItem{}, nil
	}

	items := make([]*models.MediaItem, 0, total)
	for offset := 0; offset < total; offset += 100 {
		limit := min(100, total-offset)
		page, _, err := r.itemRepo.Search(ctx, req.SearchQuery, itemTypes, limit, offset, searchAccess)
		if err != nil {
			return nil, fmt.Errorf("searching catalog items: %w", err)
		}
		items = append(items, page...)
	}

	return items, nil
}

func catalogSearchAccess(req CatalogRequest, access AccessFilter) (AccessFilter, []string, bool) {
	allowedLibraryIDs, earlyEmpty := effectiveCatalogLibraryIDs(req.Query.LibraryIDs, access)
	if earlyEmpty {
		return AccessFilter{}, nil, true
	}

	searchAccess := AccessFilter{
		AllowedLibraryIDs:  allowedLibraryIDs,
		DisabledLibraryIDs: effectiveCatalogDisabledLibraryIDs(req.Query.LibraryIDs, access.DisabledLibraryIDs),
		MaxContentRating:   access.MaxContentRating,
	}

	return searchAccess, MediaScopeItemTypes(req.Query.MediaScope), false
}

func catalogBrowseFilters(req CatalogRequest, access AccessFilter) (BrowseFilters, bool, error) {
	allowedLibraryIDs, earlyEmpty := effectiveCatalogLibraryIDs(req.Query.LibraryIDs, access)
	if earlyEmpty {
		return BrowseFilters{}, true, nil
	}

	filters := BrowseFilters{
		// BrowseFilters.Type accepts a comma-separated type list, so group
		// scopes like "video" expand here rather than leaking downstream.
		Type:               strings.Join(MediaScopeItemTypes(req.Query.MediaScope), ","),
		NamePrefix:         req.NamePrefix,
		DisabledLibraryIDs: effectiveCatalogDisabledLibraryIDs(req.Query.LibraryIDs, access.DisabledLibraryIDs),
		MaxContentRating:   access.MaxContentRating,
	}
	applyCatalogBrowseOverlayRules(&filters, req.Query)

	if len(allowedLibraryIDs) == 1 {
		filters.LibraryID = allowedLibraryIDs[0]
	} else if len(allowedLibraryIDs) > 1 {
		filters.LibraryIDs = allowedLibraryIDs
	} else if access.AllowedLibraryIDs != nil && len(req.Query.LibraryIDs) == 0 {
		filters.LibraryIDs = []int{}
	}

	return filters, false, nil
}

func applyCatalogBrowseOverlayRules(filters *BrowseFilters, def QueryDefinition) {
	if filters == nil {
		return
	}

	for _, group := range def.Groups {
		for _, rule := range group.Rules {
			switch rule.Field {
			case "genre":
				if filters.Genre == "" && rule.Op == "contains" {
					if value, ok := catalogStringValue(rule.Value); ok {
						filters.Genre = value
					}
				}
			case "status":
				if filters.Status == "" && rule.Op == "is" {
					if value, ok := catalogStringValue(rule.Value); ok {
						filters.Status = value
					}
				}
			case "content_rating":
				if rule.Op == "is" {
					if value, ok := catalogStringValue(rule.Value); ok && value != "" {
						filters.ContentRating = append(filters.ContentRating, value)
					}
				}
			case "year":
				switch rule.Op {
				case "between":
					if values := catalogIntValues(rule.Value); len(values) >= 2 {
						if filters.YearMin == 0 || values[0] > filters.YearMin {
							filters.YearMin = values[0]
						}
						if filters.YearMax == 0 || values[1] < filters.YearMax {
							filters.YearMax = values[1]
						}
					}
				case "gte", "gt", "is":
					if value, ok := catalogIntValue(rule.Value); ok {
						if rule.Op == "gt" {
							value++
						}
						if filters.YearMin == 0 || value > filters.YearMin {
							filters.YearMin = value
						}
						if rule.Op == "is" && (filters.YearMax == 0 || value < filters.YearMax) {
							filters.YearMax = value
						}
					}
				case "lte", "lt":
					if value, ok := catalogIntValue(rule.Value); ok {
						if rule.Op == "lt" {
							value--
						}
						if filters.YearMax == 0 || value < filters.YearMax {
							filters.YearMax = value
						}
					}
				}
			}
		}
	}

	if len(filters.ContentRating) > 1 {
		filters.ContentRating = slices.Compact(filters.ContentRating)
	}
}

func effectiveCatalogLibraryIDs(requestIDs []int, access AccessFilter) ([]int, bool) {
	if len(requestIDs) == 0 {
		if access.AllowedLibraryIDs != nil {
			ids := append([]int(nil), access.AllowedLibraryIDs...)
			ids = removeCatalogLibraryIDs(ids, access.DisabledLibraryIDs)
			if len(ids) == 0 {
				return nil, true
			}
			return ids, false
		}
		return nil, false
	}

	ids := append([]int(nil), requestIDs...)
	if access.AllowedLibraryIDs != nil {
		ids = intersectInts(ids, access.AllowedLibraryIDs)
	}
	ids = removeCatalogLibraryIDs(ids, access.DisabledLibraryIDs)
	if len(ids) == 0 {
		return nil, true
	}
	return ids, false
}

func effectiveCatalogDisabledLibraryIDs(requestIDs, disabled []int) []int {
	if len(requestIDs) > 0 {
		return nil
	}
	return append([]int(nil), disabled...)
}

func removeCatalogLibraryIDs(ids, remove []int) []int {
	if len(ids) == 0 || len(remove) == 0 {
		return ids
	}
	blocked := make(map[int]struct{}, len(remove))
	for _, id := range remove {
		blocked[id] = struct{}{}
	}
	filtered := ids[:0]
	for _, id := range ids {
		if _, ok := blocked[id]; ok {
			continue
		}
		filtered = append(filtered, id)
	}
	return filtered
}

func filterCatalogItems(items []*models.MediaItem, def QueryDefinition) []*models.MediaItem {
	filtered := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		if !MediaScopeMatchesItemType(def.MediaScope, item.Type) {
			continue
		}
		if catalogDefinitionMatchesItem(item, def) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterCatalogSearchItems(items []*models.MediaItem, raw string) []*models.MediaItem {
	query := strings.TrimSpace(raw)
	if query == "" {
		return items
	}

	parsed := parseSearchQuery(query)
	needle := normalizeTitleForComparison(firstNonEmptySearchValue(parsed.Text, query))
	tokens := strings.Fields(needle)
	if len(tokens) == 0 {
		return items
	}

	filtered := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		haystack := normalizeTitleForComparison(strings.Join([]string{
			item.Title,
			item.SortTitle,
			item.OriginalTitle,
			item.Overview,
		}, " "))
		matched := true
		for _, token := range tokens {
			if !strings.Contains(haystack, token) {
				matched = false
				break
			}
		}
		if matched {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 && len(items) > 0 && eligibleForFuzzy(parsed) {
		// The strict per-token substring filter erases the repo layer's
		// typo-tolerant (trigram) matches: a misspelled token is never a
		// substring of the titles it fuzzy-matched, so a typo query that
		// found results via the fuzzy fallback would be filtered to zero here
		// while the plain search box shows matches. When a fuzzy-eligible
		// query would be emptied entirely, trust the repo's relevance
		// ordering instead.
		return items
	}
	return filtered
}

func filterCatalogNamePrefix(items []*models.MediaItem, raw string) []*models.MediaItem {
	prefix := strings.ToLower(strings.TrimSpace(raw))
	if prefix == "" {
		return items
	}

	filtered := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		title := strings.ToLower(strings.TrimSpace(item.Title))
		sortTitle := strings.ToLower(strings.TrimSpace(item.SortTitle))
		if strings.HasPrefix(title, prefix) || (sortTitle != "" && strings.HasPrefix(sortTitle, prefix)) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func catalogStringValue(value any) (string, bool) {
	str, ok := value.(string)
	if !ok {
		return "", false
	}
	str = strings.TrimSpace(str)
	return str, str != ""
}

func catalogIntValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func catalogIntValues(value any) []int {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]int, 0, len(values))
	for _, entry := range values {
		if normalized, ok := catalogIntValue(entry); ok {
			result = append(result, normalized)
		}
	}
	return result
}

func catalogDefinitionMatchesItem(item *models.MediaItem, def QueryDefinition) bool {
	matchMode := def.Match
	if matchMode == "" {
		matchMode = "all"
	}

	matches := 0
	for _, group := range def.Groups {
		if catalogGroupMatchesItem(item, group) {
			matches++
			if matchMode == "any" {
				return true
			}
		} else if matchMode == "all" {
			return false
		}
	}

	if matchMode == "any" {
		return matches > 0
	}
	return true
}

func catalogGroupMatchesItem(item *models.MediaItem, group QueryGroup) bool {
	matchMode := group.Match
	if matchMode == "" {
		matchMode = "all"
	}

	matches := 0
	for _, rule := range group.Rules {
		if catalogRuleMatchesItem(item, rule) {
			matches++
			if matchMode == "any" {
				return true
			}
		} else if matchMode == "all" {
			return false
		}
	}

	if matchMode == "any" {
		return matches > 0
	}
	return true
}

func catalogRuleMatchesItem(item *models.MediaItem, rule QueryRule) bool {
	switch rule.Field {
	case "type":
		return compareCatalogString(item.Type, rule.Op, rule.Value)
	case "genre":
		return compareCatalogStringSlice(item.Genres, rule.Op, rule.Value)
	case "studio":
		return compareCatalogStringSlice(item.Studios, rule.Op, rule.Value)
	case "network":
		return compareCatalogStringSlice(item.Networks, rule.Op, rule.Value)
	case "country":
		return compareCatalogStringSlice(item.Countries, rule.Op, rule.Value)
	case "original_language":
		return compareCatalogString(item.OriginalLanguage, rule.Op, rule.Value)
	case "content_rating":
		return compareCatalogString(item.ContentRating, rule.Op, rule.Value)
	case "status":
		return compareCatalogString(item.Status, rule.Op, rule.Value)
	case "year":
		return compareCatalogNumeric(float64(item.Year), rule.Op, rule.Value)
	case "rating_imdb":
		if item.RatingIMDB == nil {
			return false
		}
		return compareCatalogNumeric(*item.RatingIMDB, rule.Op, rule.Value)
	case "added_at":
		addedAt := item.CreatedAt
		if item.AddedAt != nil {
			addedAt = *item.AddedAt
		}
		return compareCatalogTime(addedAt, rule.Op, rule.Value)
	case "release_date":
		releaseDate := catalogItemReleaseDate(item)
		if releaseDate == "" {
			return false
		}
		return compareCatalogStringDate(releaseDate, rule.Op, rule.Value)
	default:
		return false
	}
}

func compareCatalogString(actual, op string, value any) bool {
	expected := strings.TrimSpace(strings.ToLower(fmt.Sprint(value)))
	actual = strings.TrimSpace(strings.ToLower(actual))

	switch op {
	case "is":
		return actual == expected
	case "is_not":
		return actual != expected
	default:
		return false
	}
}

func compareCatalogStringSlice(actual []string, op string, value any) bool {
	expected := strings.TrimSpace(strings.ToLower(fmt.Sprint(value)))
	found := false
	for _, entry := range actual {
		if strings.TrimSpace(strings.ToLower(entry)) == expected {
			found = true
			break
		}
	}

	switch op {
	case "contains", "is":
		return found
	case "is_not":
		return !found
	default:
		return false
	}
}

func compareCatalogNumeric(actual float64, op string, value any) bool {
	switch op {
	case "is":
		expected, ok := catalogFloat(value)
		return ok && actual == expected
	case "is_not":
		expected, ok := catalogFloat(value)
		return ok && actual != expected
	case "gt":
		expected, ok := catalogFloat(value)
		return ok && actual > expected
	case "gte":
		expected, ok := catalogFloat(value)
		return ok && actual >= expected
	case "lt":
		expected, ok := catalogFloat(value)
		return ok && actual < expected
	case "lte":
		expected, ok := catalogFloat(value)
		return ok && actual <= expected
	case "between":
		values, ok := catalogFloatRange(value)
		return ok && actual >= values[0] && actual <= values[1]
	default:
		return false
	}
}

func compareCatalogStringDate(actual, op string, value any) bool {
	actualTime, err := time.Parse("2006-01-02", actual)
	if err != nil {
		return false
	}

	switch op {
	case "gt", "gte", "lt", "lte", "between", "is", "is_not", "in_last":
	default:
		return false
	}

	if op == "in_last" {
		duration, ok := catalogStringValue(value)
		if !ok {
			return false
		}
		spec, err := parseDurationSpec(duration)
		if err != nil {
			return false
		}
		cutoff := catalogDateOnly(spec.cutoffTime(time.Now().UTC()))
		return !actualTime.Before(cutoff)
	}

	if op == "between" {
		values, ok := catalogStringRange(value)
		if !ok {
			return false
		}
		start, err := time.Parse("2006-01-02", values[0])
		if err != nil {
			return false
		}
		end, err := time.Parse("2006-01-02", values[1])
		if err != nil {
			return false
		}
		return !actualTime.Before(start) && !actualTime.After(end)
	}

	expected := strings.TrimSpace(fmt.Sprint(value))
	expectedTime, err := time.Parse("2006-01-02", expected)
	if err != nil {
		return false
	}

	switch op {
	case "is":
		return actualTime.Equal(expectedTime)
	case "is_not":
		return !actualTime.Equal(expectedTime)
	case "gt":
		return actualTime.After(expectedTime)
	case "gte":
		return actualTime.After(expectedTime) || actualTime.Equal(expectedTime)
	case "lt":
		return actualTime.Before(expectedTime)
	case "lte":
		return actualTime.Before(expectedTime) || actualTime.Equal(expectedTime)
	default:
		return false
	}
}

func compareCatalogTime(actual time.Time, op string, value any) bool {
	if actual.IsZero() {
		return false
	}

	switch op {
	case "gt", "gte", "lt", "lte", "between", "in_last":
	default:
		return false
	}

	if op == "in_last" {
		duration, ok := catalogStringValue(value)
		if !ok {
			return false
		}
		spec, err := parseDurationSpec(duration)
		if err != nil {
			return false
		}
		return !actual.Before(spec.cutoffTime(time.Now()))
	}

	if op == "between" {
		values, ok := catalogStringRange(value)
		if !ok {
			return false
		}
		start, ok := catalogTimeValue(values[0])
		if !ok {
			return false
		}
		end, ok := catalogTimeValue(values[1])
		if !ok {
			return false
		}
		return !actual.Before(start) && !actual.After(end)
	}

	expected, ok := catalogTimeValue(value)
	if !ok {
		return false
	}

	switch op {
	case "gt":
		return actual.After(expected)
	case "gte":
		return actual.After(expected) || actual.Equal(expected)
	case "lt":
		return actual.Before(expected)
	case "lte":
		return actual.Before(expected) || actual.Equal(expected)
	default:
		return false
	}
}

func catalogTimeValue(value any) (time.Time, bool) {
	switch v := value.(type) {
	case time.Time:
		return v, !v.IsZero()
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return time.Time{}, false
		}
		if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
			return parsed, true
		}
		if parsed, err := time.Parse("2006-01-02", trimmed); err == nil {
			return parsed, true
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

func catalogDateOnly(value time.Time) time.Time {
	utc := value.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

func catalogFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func catalogFloatRange(value any) ([2]float64, bool) {
	switch v := value.(type) {
	case []any:
		if len(v) != 2 {
			return [2]float64{}, false
		}
		start, ok := catalogFloat(v[0])
		if !ok {
			return [2]float64{}, false
		}
		end, ok := catalogFloat(v[1])
		if !ok {
			return [2]float64{}, false
		}
		return [2]float64{start, end}, true
	default:
		return [2]float64{}, false
	}
}

func catalogStringRange(value any) ([2]string, bool) {
	switch v := value.(type) {
	case []any:
		if len(v) != 2 {
			return [2]string{}, false
		}
		return [2]string{fmt.Sprint(v[0]), fmt.Sprint(v[1])}, true
	default:
		return [2]string{}, false
	}
}

func catalogItemReleaseDate(item *models.MediaItem) string {
	if item.ReleaseDate != nil && strings.TrimSpace(*item.ReleaseDate) != "" {
		return strings.TrimSpace(*item.ReleaseDate)
	}
	if item.FirstAirDate != nil {
		return strings.TrimSpace(*item.FirstAirDate)
	}
	return ""
}

func sortCatalogItems(items []*models.MediaItem, sortConfig QuerySort) {
	slices.SortStableFunc(items, func(a, b *models.MediaItem) int {
		if a == nil && b == nil {
			return 0
		}
		if a == nil {
			return 1
		}
		if b == nil {
			return -1
		}

		direction := 1
		if sortConfig.Order == "desc" {
			direction = -1
		}

		switch sortConfig.Field {
		case "title":
			left := strings.ToLower(firstNonEmptySearchValue(a.SortTitle, a.Title))
			right := strings.ToLower(firstNonEmptySearchValue(b.SortTitle, b.Title))
			if left == right {
				left = strings.ToLower(a.Title)
				right = strings.ToLower(b.Title)
			}
			if cmp := strings.Compare(left, right); cmp != 0 {
				return cmp * direction
			}
		case "year":
			if a.Year != b.Year {
				if a.Year < b.Year {
					return -1 * direction
				}
				return 1 * direction
			}
		case "rating_imdb":
			left := -1.0
			right := -1.0
			if a.RatingIMDB != nil {
				left = *a.RatingIMDB
			}
			if b.RatingIMDB != nil {
				right = *b.RatingIMDB
			}
			if left != right {
				if left < right {
					return -1 * direction
				}
				return 1 * direction
			}
		case "release_date":
			left := catalogItemReleaseDate(a)
			right := catalogItemReleaseDate(b)
			if left != right {
				return strings.Compare(left, right) * direction
			}
		case "last_air_date":
			var left, right string
			if a.LastAirDate != nil {
				left = strings.TrimSpace(*a.LastAirDate)
			}
			if b.LastAirDate != nil {
				right = strings.TrimSpace(*b.LastAirDate)
			}
			if left == "" && right != "" {
				return 1
			}
			if left != "" && right == "" {
				return -1
			}
			if left != right {
				return strings.Compare(left, right) * direction
			}
		case "added_at":
			left := a.CreatedAt
			right := b.CreatedAt
			if a.AddedAt != nil {
				left = *a.AddedAt
			}
			if b.AddedAt != nil {
				right = *b.AddedAt
			}
			if !left.Equal(right) {
				if left.Before(right) {
					return -1 * direction
				}
				return 1 * direction
			}
		}

		return strings.Compare(a.ContentID, b.ContentID)
	})
}

func paginateCatalogItems(items []*models.MediaItem, offset, limit int) []*models.MediaItem {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []*models.MediaItem{}
	}
	if limit <= 0 {
		limit = 20
	}

	end := min(len(items), offset+limit)
	return append([]*models.MediaItem(nil), items[offset:end]...)
}
