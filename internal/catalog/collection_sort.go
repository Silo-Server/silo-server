package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// QueryCollectionItemsBySort loads collection members through the catalog
// query executor, applying an optional personal-collection display filter and
// the resolved sort before the database limit. This keeps rail queries bounded
// without changing the full-match total returned to callers.
func QueryCollectionItemsBySort(
	ctx context.Context,
	pool *pgxpool.Pool,
	contentIDs []string,
	qs QuerySort,
	access AccessFilter,
	limit int,
	displayQueryDefinition string,
) ([]*models.MediaItem, int, error) {
	if len(contentIDs) == 0 {
		return []*models.MediaItem{}, 0, nil
	}

	def := QueryDefinition{Sort: qs}
	if trimmed := strings.TrimSpace(displayQueryDefinition); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &def); err != nil {
			return nil, 0, fmt.Errorf("parsing collection display_query_definition: %w", err)
		}
		// Display fragments are filter-only. Never let stale or malformed stored
		// execution fields expand scope, replace the selected sort, or set a
		// different limit.
		def.MediaScope = ""
		def.LibraryIDs = nil
		def.Sort = qs
		def.Limit = nil
	}

	queryAccess := access
	if access.AllowedContentIDs != nil {
		queryAccess.AllowedContentIDs = intersectContentIDs(contentIDs, access.AllowedContentIDs)
	} else {
		queryAccess.AllowedContentIDs = contentIDs
	}

	if limit <= 0 || limit > len(contentIDs) {
		limit = len(contentIDs)
	}
	executor := &QueryExecutor{Pool: pool}
	items, total, err := executor.Preview(ctx, def.Normalize(), queryAccess, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("querying collection items by %q: %w", qs.Field, err)
	}
	return items, total, nil
}

// EffectiveCollectionSort resolves the order a collection's items are shown in
// when the request itself carries no sort. Precedence, highest first:
//
//  1. the viewing profile's saved override — set by changing the sort while
//     browsing the collection, and sticky from then on;
//  2. the default sort the collection's creator configured (sort_config);
//  3. the collection's own source order, signaled by ok == false.
//
// An override row with an empty sort field is a real choice ("keep source
// order") and short-circuits the creator's default, which is why it returns
// false rather than falling through.
//
// Creator defaults for library collections stay user-agnostic because they are
// shared. A viewer override is profile-scoped, however, so personalized fields
// (progress, date viewed, plays) are valid for either collection kind.
func (r *CatalogResolver) EffectiveCollectionSort(
	ctx context.Context,
	access AccessFilter,
	kind string,
	collectionID string,
	sortConfig []byte,
) (QuerySort, bool) {
	if pref := r.collectionSortOverride(ctx, access, kind, collectionID); pref != nil {
		if strings.TrimSpace(pref.SortField) == "" {
			return QuerySort{}, false
		}
		if qs, ok := NormalizeCollectionSort(pref.SortField, pref.SortOrder, true); ok {
			return qs, true
		}
		// A stored override that no longer validates (a sort field retired from
		// the vocabulary) falls back to the creator's default rather than
		// failing the browse.
	}

	return ParseCollectionDefaultSort(sortConfig, kind == userstore.CollectionKindUser)
}

// OrderCollectionItemsBySort reorders already-loaded collection members by a
// resolved sort, returning them in that order.
//
// Ordering runs through the catalog query executor constrained to the member
// IDs, so a rail orders items exactly as the collection's browse page does —
// same NULLS LAST handling, same sort_title collation — rather than through a
// second, drifting in-memory comparator. An empty sort field returns items
// untouched (source order). Items the executor does not return (no longer
// visible to this access scope) are dropped, matching the browse path.
func OrderCollectionItemsBySort(
	ctx context.Context,
	pool *pgxpool.Pool,
	items []*models.MediaItem,
	qs QuerySort,
	access AccessFilter,
) ([]*models.MediaItem, error) {
	if strings.TrimSpace(qs.Field) == "" || len(items) == 0 {
		return items, nil
	}

	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil && strings.TrimSpace(item.ContentID) != "" {
			ids = append(ids, item.ContentID)
		}
	}
	if len(ids) == 0 {
		return items, nil
	}

	ordered, _, err := QueryCollectionItemsBySort(ctx, pool, ids, qs, access, len(ids), "")
	if err != nil {
		return nil, fmt.Errorf("ordering collection items by %q: %w", qs.Field, err)
	}

	// Preview returns freshly loaded rows; re-project onto the caller's items so
	// any enrichment already applied to them survives the reorder.
	byID := make(map[string]*models.MediaItem, len(items))
	for _, item := range items {
		if item != nil {
			byID[item.ContentID] = item
		}
	}
	result := make([]*models.MediaItem, 0, len(ordered))
	for _, item := range ordered {
		if item == nil {
			continue
		}
		if original, ok := byID[item.ContentID]; ok {
			result = append(result, original)
		}
	}
	return result, nil
}

// collectionSortOverride loads the viewing profile's saved sort for a
// collection, or nil when there is none. A lookup failure is logged and treated
// as "no override": a browse must not fail because a preference row could not
// be read.
func (r *CatalogResolver) collectionSortOverride(
	ctx context.Context,
	access AccessFilter,
	kind string,
	collectionID string,
) *userstore.CollectionSortPreference {
	if r.storeProvider == nil || access.UserID <= 0 || strings.TrimSpace(access.ProfileID) == "" {
		return nil
	}
	store, err := r.storeProvider.ForUser(ctx, access.UserID)
	if err != nil || store == nil {
		if err != nil {
			slog.WarnContext(ctx, "loading user store for collection sort preference",
				"collection_kind", kind, "collection_id", collectionID, "error", err)
		}
		return nil
	}
	pref, err := store.GetCollectionSortPreference(ctx, access.ProfileID, kind, collectionID)
	if err != nil {
		slog.WarnContext(ctx, "loading collection sort preference",
			"collection_kind", kind, "collection_id", collectionID, "error", err)
		return nil
	}
	return pref
}
