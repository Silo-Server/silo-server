package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
)

var ErrCatalogCursorChanged = errors.New("catalog source changed during pagination")

// CollectionCursor fences only the selected collection's definition and
// membership. It does not invalidate a live tuple when unrelated catalog data
// or viewer progress changes.
type CollectionCursor struct {
	ID       string                            `json:"id"`
	Revision int64                             `json:"revision"`
	Position *userstore.CollectionItemPosition `json:"position,omitempty"`
}

type collectionPageReader func(context.Context, string, userstore.CollectionItemsPageOptions) (userstore.CollectionItemsPage, error)
type collectionRevisionReader func(context.Context, string) (int64, error)

func collectionFence(ctx context.Context, req CatalogRequest, revision collectionRevisionReader) (int64, error) {
	current, err := revision(ctx, req.CollectionID)
	if err != nil {
		if errors.Is(err, ErrLibraryCollectionNotFound) || errors.Is(err, userstore.ErrCollectionNotFound) {
			if req.After != nil {
				return 0, ErrCatalogCursorChanged
			}
			return 0, ErrCatalogSourceNotFound
		}
		return 0, err
	}
	if req.After != nil {
		c := req.After.Collection
		if c == nil || c.ID != req.CollectionID || c.Revision != current {
			return 0, ErrCatalogCursorChanged
		}
	}
	return current, nil
}

// The two revision reads deliberately bracket all parent/access, query and
// hydration reads. Source mutations advance a monotonic revision in their own
// transaction. Thus a mutation visible anywhere inside this interval must be
// visible to the final primary-database read, and its page is discarded. A
// mutation committed after this check is detected by the next continuation.
// This is optimistic validation, not a claim of a retained snapshot. Both reads
// must use the authoritative primary (never a lagging read replica).
func finishCollectionCursor(ctx context.Context, req CatalogRequest, revision int64, read collectionRevisionReader, result *CatalogResult, err error) (*CatalogResult, error) {
	if err != nil {
		if errors.Is(err, userstore.ErrCollectionChanged) {
			return nil, ErrCatalogCursorChanged
		}
		return nil, err
	}
	current, err := read(ctx, req.CollectionID)
	if err != nil {
		if errors.Is(err, ErrLibraryCollectionNotFound) || errors.Is(err, userstore.ErrCollectionNotFound) {
			return nil, ErrCatalogCursorChanged
		}
		return nil, err
	}
	if current != revision {
		return nil, ErrCatalogCursorChanged
	}
	result.CursorScope = &QueryCursor{Collection: &CollectionCursor{ID: req.CollectionID, Revision: revision}}
	if result.Next != nil {
		if result.Next.Collection == nil {
			result.Next.Collection = &CollectionCursor{}
		}
		result.Next.Collection.ID = req.CollectionID
		result.Next.Collection.Revision = revision
	}
	return result, nil
}

func (r *CatalogResolver) resolveLibraryCollectionCursor(ctx context.Context, req CatalogRequest, access AccessFilter, repo *LibraryCollectionRepository) (*CatalogResult, error) {
	revision, err := collectionFence(ctx, req, repo.CollectionRevision)
	if err != nil {
		return nil, err
	}
	collection, err := repo.GetByID(ctx, req.CollectionID)
	if err != nil || !CanAccessLibraryCollection(collection, access) {
		return nil, ErrCatalogSourceNotFound
	}
	result, err := r.resolveCollectionWithEffectiveSort(ctx, req, access, userstore.CollectionKindLibrary, collection.ID, collection.SortConfig, func(effective CatalogRequest) (*CatalogResult, error) {
		if IsLiveQueryType(collection.CollectionType) || catalogCollectionUsesLiveQuery(collection.QueryDefinition) {
			def, err := parseCatalogCollectionQueryDefinition(collection.QueryDefinition)
			if err != nil {
				return nil, err
			}
			if len(collection.LibraryIDs) > 0 {
				def.LibraryIDs = intersectCatalogDefinitionLibraries(def.LibraryIDs, collection.LibraryIDs)
			} else if collection.LibraryID > 0 {
				def.LibraryIDs = intersectCatalogDefinitionLibraries(def.LibraryIDs, []int{collection.LibraryID})
			}
			return r.resolveSmartCollectionCursor(ctx, effective, access, def)
		}
		return r.resolveLibraryMembershipQueryCursor(ctx, effective, access)
	})
	return finishCollectionCursor(ctx, req, revision, repo.CollectionRevision, result, err)
}

func (r *CatalogResolver) resolveUserCollectionCursor(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	store, err := r.catalogStoreForAccess(ctx, access)
	if err != nil {
		return nil, err
	}
	pager, ok := store.(userstore.CollectionItemsPager)
	if !ok {
		return nil, userstore.ErrCollectionPagingUnsupported
	}
	revision, err := collectionFence(ctx, req, pager.CollectionRevision)
	if err != nil {
		return nil, err
	}
	collection, err := store.GetCollection(ctx, req.CollectionID)
	if err != nil || !ProfileCanAccessCollection(collection, access.ProfileID) {
		return nil, ErrCatalogSourceNotFound
	}
	result, err := r.resolveCollectionWithEffectiveSort(ctx, req, access, userstore.CollectionKindUser, collection.ID, []byte(collection.SortConfig), func(effective CatalogRequest) (*CatalogResult, error) {
		sqlState := userstore.HasCatalogSQLState(store)
		if !sqlState {
			if effective.GroupByWork {
				return nil, ErrCatalogStorageUnsupported
			}
			var display QueryDefinition
			if collection.DisplayQueryDefinition != "" {
				if err := json.Unmarshal([]byte(collection.DisplayQueryDefinition), &display); err != nil {
					return nil, err
				}
			}
			if effective.Query.IsPersonalized() || display.IsPersonalized() {
				return nil, fmt.Errorf("%w: selected user store does not support personalized catalog SQL", ErrCatalogStorageUnsupported)
			}
		}
		if IsLiveQueryType(collection.CollectionType) {
			def, err := parseCatalogCollectionQueryDefinition([]byte(collection.QueryDefinition))
			if err != nil {
				return nil, err
			}
			if !sqlState && def.IsPersonalized() {
				return nil, fmt.Errorf("%w: selected user store does not support personalized catalog SQL", ErrCatalogStorageUnsupported)
			}
			return r.resolveSmartCollectionCursorWithDisplay(ctx, effective, access, def, collection.DisplayQueryDefinition)
		}
		if sqlState {
			return r.resolvePersonalMembershipQueryCursor(ctx, effective, access, collection.DisplayQueryDefinition)
		}
		return r.resolveManualCollectionCursor(ctx, effective, access, revision, pager.ListCollectionItemsPage, collection.DisplayQueryDefinition)
	})
	return finishCollectionCursor(ctx, req, revision, pager.CollectionRevision, result, err)
}

func (r *CatalogResolver) resolveSmartCollectionCursor(ctx context.Context, req CatalogRequest, access AccessFilter, def QueryDefinition) (*CatalogResult, error) {
	return r.resolveSmartCollectionCursorWithDisplay(ctx, req, access, def, "")
}
func (r *CatalogResolver) resolveSmartCollectionCursorWithDisplay(ctx context.Context, req CatalogRequest, access AccessFilter, def QueryDefinition, display string) (*CatalogResult, error) {
	baseAccess := access
	if req.Source == CatalogSourceLibraryCollection {
		baseAccess = stripCatalogUserScope(access)
		if err := def.ValidateWithOptions(false, false); err != nil {
			return nil, fmt.Errorf("%w: library collection definition: %w", ErrInvalidCatalogRequest, err)
		}
	} else if err := r.requireQueryStore(ctx, def, baseAccess); err != nil {
		return nil, err
	}

	def = ApplySmartCollectionItemLimit(def)
	if !catalogRequestHasOverlay(req) && strings.TrimSpace(display) == "" {
		after := req.After
		if after != nil && len(after.Keys) == 0 {
			after = nil
		}
		return r.resolveQuerySource(ctx, CatalogRequest{Source: CatalogSourceQuery, Query: def, Limit: req.Limit, CursorPaging: true, GroupByWork: req.GroupByWork, After: after, Seek: req.Seek, SkipTotal: req.SkipTotal, SnapshotAt: req.SnapshotAt}, baseAccess)
	}
	base := r.queryExecutorForScope(def.MediaScope, req.SnapshotAt)
	predicate, args, err := collectionDefinitionPredicate(base, def, baseAccess)
	if err != nil {
		return nil, err
	}
	overlay := req.Query
	if overlay.Sort.Field == "" {
		overlay.Sort = def.Sort
	}
	if err := r.requireQueryStore(ctx, overlay, access); err != nil {
		return nil, err
	}
	executor := r.queryExecutorForScope(overlay.MediaScope, req.SnapshotAt)
	if overlay.MediaScope == "" {
		executor.Scope = def.MediaScope
	}
	executor.SourceWhere = predicate
	executor.SourceArgs = args
	if err := r.applyCollectionDisplayPredicate(ctx, executor, display, access); err != nil {
		return nil, err
	}

	return resolveCollectionExecutorCursor(ctx, executor, req, overlay, access)
}

// The inner SELECT is evaluated by PostgreSQL; it never transfers or retains
// the full source membership in the API process. Keeping its ORDER/LIMIT inside
// the membership predicate preserves capped smart-source semantics for overlays.
func collectionDefinitionPredicate(executor *QueryExecutor, def QueryDefinition, access AccessFilter) (string, []any, error) {
	plan, err := executor.buildPreviewPagePlan(def, access, 1, 0)
	if err != nil {
		return "", nil, err
	}
	args := append([]any{}, plan.cteArgs...)
	args = append(args, plan.args...)
	args = append(args, plan.sortArgs...)
	prefix := ""
	if len(plan.ctes) > 0 {
		prefix = "WITH " + strings.Join(plan.ctes, ",\n") + "\n"
	}
	query := prefix + "SELECT mi.content_id " + plan.fromClausePaged + " " + plan.whereClause + " " + plan.orderBy
	if def.Limit != nil {
		args = append(args, *def.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return "mi.content_id IN (" + query + ")", args, nil
}

func (r *CatalogResolver) resolveManualCollectionCursor(ctx context.Context, req CatalogRequest, access AccessFilter, revision int64, read collectionPageReader, display string) (*CatalogResult, error) {
	if req.Limit < 1 || req.Limit > 200 {
		return nil, fmt.Errorf("%w: collection page limit must be between 1 and 200", ErrInvalidCatalogRequest)
	}
	if req.Seek != nil && *req.Seek != 0 {
		return nil, fmt.Errorf("%w: manual collection jumps are not supported by the selected user store", ErrCatalogStorageUnsupported)
	}
	if req.Query.Sort.Field != "" {
		return nil, fmt.Errorf("%w: personal collection sorting requires selected-store membership SQL", ErrCatalogStorageUnsupported)
	}
	if req.Query.Limit != nil {
		if consumedLimit := *req.Query.Limit; consumedLimit < 1 {
			return nil, fmt.Errorf("%w: collection limit must be positive", ErrInvalidCatalogRequest)
		}
	}
	var position *userstore.CollectionItemPosition
	consumed := 0
	if req.After != nil {
		position = req.After.Collection.Position
		consumed = req.After.Consumed
	}
	if req.Query.Limit != nil {
		if consumed >= *req.Query.Limit {
			return &CatalogResult{Items: []*models.MediaItem{}}, nil
		}
		req.Limit = min(req.Limit, *req.Query.Limit-consumed)
	}
	result := &CatalogResult{Items: []*models.MediaItem{}}
	// Bounded work even if most collection members are inaccessible. A partial
	// page can continue from the last inspected tuple without exposing hidden IDs
	// outside the signed cursor envelope.
	const maxBatches = 10
	for range maxBatches {
		page, err := read(ctx, req.CollectionID, userstore.CollectionItemsPageOptions{Limit: 200, After: position, Revision: revision})
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(page.Items))
		for i, item := range page.Items {
			ids[i] = item.MediaItemID
		}
		if access.AllowedContentIDs != nil {
			ids = intersectContentIDs(ids, access.AllowedContentIDs)
		}
		visible, err := r.collectionVisibleBatch(ctx, ids, req, access)
		if err != nil {
			return nil, err
		}
		visible, err = FilterCollectionItemsByDisplayQuery(ctx, r.itemRepo.pool, visible, display, access)
		if err != nil {
			return nil, err
		}
		byID := make(map[string]*models.MediaItem, len(visible))
		for _, item := range visible {
			byID[item.ContentID] = item
		}
		for _, membership := range page.Items {
			item := byID[membership.MediaItemID]
			if item != nil && len(result.Items) == req.Limit {
				if req.Query.Limit != nil && consumed+len(result.Items) >= *req.Query.Limit {
					return result, nil
				}
				result.HasMore = true
				result.Next = &QueryCursor{Consumed: consumed + len(result.Items), Collection: &CollectionCursor{Position: position}}
				return result, nil
			}
			position = &userstore.CollectionItemPosition{Position: membership.Position, MediaItemID: membership.MediaItemID}
			if item != nil {
				result.Items = append(result.Items, item)
			}
		}
		if !page.HasMore {
			return result, nil
		}
	}
	result.HasMore = true
	result.Next = &QueryCursor{Consumed: consumed + len(result.Items), Collection: &CollectionCursor{Position: position}}
	return result, nil
}

// The membership predicate remains in PostgreSQL, so arbitrary structured
// filters and sorting do not materialize a collection's IDs in the API process.
func (r *CatalogResolver) resolveLibraryMembershipQueryCursor(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	if err := r.requireQueryStore(ctx, req.Query, access); err != nil {
		return nil, err
	}
	executor := r.queryExecutorForScope(req.Query.MediaScope, req.SnapshotAt)
	executor.SourceWhere = "EXISTS (SELECT 1 FROM library_collection_items cursor_membership WHERE cursor_membership.collection_id=$1 AND cursor_membership.media_item_id=mi.content_id)"
	executor.SourceArgs = []any{req.CollectionID}
	if req.Query.Sort.Field == "" {
		executor.SourceOrder = []queryCursorTerm{
			{expression: "COALESCE((SELECT position FROM library_collection_items cursor_position WHERE cursor_position.collection_id=$1 AND cursor_position.media_item_id=mi.content_id),0)", kind: cursorKindNumber, nullsLast: true},
			{expression: cursorContentIDExpression, kind: cursorKindText, nullsLast: true},
		}
	}
	return resolveCollectionExecutorCursor(ctx, executor, req, req.Query, access)
}

func resolveCollectionExecutorCursor(ctx context.Context, executor *QueryExecutor, req CatalogRequest, def QueryDefinition, access AccessFilter) (*CatalogResult, error) {
	executor.GroupByWork = req.GroupByWork
	applyCollectionSearchPredicate(executor, req.SearchQuery)
	access.NamePrefix = req.NamePrefix
	after := req.After
	if after != nil && len(after.Keys) == 0 {
		after = nil
	}
	if req.Seek != nil {
		var err error
		after, err = executor.SeekCursor(ctx, def, access, *req.Seek)
		if errors.Is(err, pgx.ErrNoRows) {
			empty := &CatalogResult{Items: []*models.MediaItem{}}
			if executor.SnapshotAt != nil {
				empty.SnapshotAt = *executor.SnapshotAt
			} else if req.SnapshotAt != nil {
				empty.SnapshotAt = *req.SnapshotAt
			}
			return empty, nil
		}
		if err != nil {
			return nil, err
		}
	}
	page, err := executor.PreviewCursorPage(ctx, def, access, req.Limit, after, !req.SkipTotal)
	if err != nil {
		return nil, err
	}
	return &CatalogResult{Items: page.Items, Total: page.Total, TotalExact: page.TotalExact, HasMore: page.HasMore, Next: page.Next}, nil
}

func (r *CatalogResolver) collectionVisibleBatch(ctx context.Context, ids []string, req CatalogRequest, access AccessFilter) ([]*models.MediaItem, error) {
	if len(ids) == 0 {
		return []*models.MediaItem{}, nil
	}
	if !catalogRequestHasOverlay(req) {
		return r.fetchAccessibleItemsByID(ctx, ids, catalogBaseCollectionRequest(req), access)
	}
	def := req.Query
	def.Limit = nil
	if err := r.requireQueryStore(ctx, def, access); err != nil {
		return nil, err
	}
	access.AllowedContentIDs = ids
	access.NamePrefix = req.NamePrefix
	executor := r.queryExecutorForScope(def.MediaScope, req.SnapshotAt)
	applyCollectionSearchPredicate(executor, req.SearchQuery)
	page, err := executor.PreviewCursorPage(ctx, def, access, 200, nil, false)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// Collection search keeps its token-substring semantics in SQL. It does not
// invoke a catalog-wide relevance provider or mistake unmatched batches for
// fuzzy-provider results.
func applyCollectionSearchPredicate(executor *QueryExecutor, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	parsed := parseSearchQuery(raw)
	tokens := strings.Fields(normalizeTitleForComparison(firstNonEmptySearchValue(parsed.Text, raw)))
	for _, token := range tokens {
		executor.SourceArgs = append(executor.SourceArgs, "%"+token+"%")
		condition := fmt.Sprintf("public.normalize_search_text(concat_ws(' ',mi.title,mi.sort_title,mi.original_title,mi.overview)) LIKE $%d", len(executor.SourceArgs))
		if executor.SourceWhere == "" {
			executor.SourceWhere = condition
		} else {
			executor.SourceWhere = "(" + executor.SourceWhere + ") AND " + condition
		}
	}
}

func (r *CatalogResolver) applyCollectionDisplayPredicate(ctx context.Context, executor *QueryExecutor, display string, access AccessFilter) error {
	if strings.TrimSpace(display) != "" {
		canonical, err := NormalizeDisplayQueryFragment([]byte(display))
		if err != nil {
			return err
		}
		if canonical != "" {
			var fragment QueryDefinition
			if err := json.Unmarshal([]byte(canonical), &fragment); err != nil {
				return err
			}
			if err := r.requireQueryStore(ctx, fragment, access); err != nil {
				return err
			}
			fragment.MediaScope = executor.Scope
			predicate, displayArgs, err := collectionDefinitionPredicate(r.queryExecutorForScope(fragment.MediaScope, nil), fragment, access)
			if err != nil {
				return err
			}
			executor.SourceWhere = "(" + executor.SourceWhere + ") AND (" + rebindSQLPlaceholders(predicate, len(executor.SourceArgs)) + ")"
			executor.SourceArgs = append(executor.SourceArgs, displayArgs...)
		}
	}
	return nil
}

func (r *CatalogResolver) resolvePersonalMembershipQueryCursor(ctx context.Context, req CatalogRequest, access AccessFilter, display string) (*CatalogResult, error) {
	executor := r.queryExecutorForScope(req.Query.MediaScope, req.SnapshotAt)
	executor.SourceWhere = "EXISTS (SELECT 1 FROM user_personal_collection_items cursor_membership WHERE cursor_membership.user_id=$1 AND cursor_membership.collection_id=$2 AND cursor_membership.sub_item_id='' AND cursor_membership.media_item_id=mi.content_id)"
	executor.SourceArgs = []any{access.UserID, req.CollectionID}
	if req.Query.Sort.Field == "" {
		executor.SourceOrder = []queryCursorTerm{
			{expression: "COALESCE((SELECT position FROM user_personal_collection_items cursor_position WHERE cursor_position.user_id=$1 AND cursor_position.collection_id=$2 AND cursor_position.sub_item_id='' AND cursor_position.media_item_id=mi.content_id),0)", kind: cursorKindNumber, nullsLast: true},
			{expression: cursorContentIDExpression, kind: cursorKindText, nullsLast: true},
		}
	}
	if err := r.applyCollectionDisplayPredicate(ctx, executor, display, access); err != nil {
		return nil, err
	}
	return resolveCollectionExecutorCursor(ctx, executor, req, req.Query, access)
}
