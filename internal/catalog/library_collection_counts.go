package catalog

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A server collection's stored count is every member it has: the stored
// members of a curated or synced collection, or for a smart collection the
// matches the smart-count refresher found with no access filter. A viewer
// whose access narrows the catalog opens fewer members than that, so user
// surfaces show it ViewerCollectionCounts instead (#2828).

// NarrowsCatalog reports whether access can hide a title that an unfiltered
// read lists: a library allowlist or disabled libraries, a content allowlist,
// maturity limits, or excluded media types. Without any of them, a server
// collection's stored count is already what the viewer opens. Viewer request
// filters never set a content allowlist; it is listed so that a filter
// carrying one is never treated as unrestricted.
func (f AccessFilter) NarrowsCatalog() bool {
	limits := f.MaturityLimits
	return f.AllowedLibraryIDs != nil || len(f.DisabledLibraryIDs) > 0 || f.AllowedContentIDs != nil ||
		limits.Active() || len(f.ExcludedMediaTypes) > 0
}

// ViewerCollectionCount is what one server collection shows a viewer.
type ViewerCollectionCount struct {
	// Count is the members the viewer can open.
	Count int
	// HidesMembers reports that the collection has members and the viewer
	// can open none of them.
	HidesMembers bool
}

// ViewerCollectionCounts returns what each server collection shows the viewer
// described by access, keyed by collection ID, counted the way the
// collection's catalog page counts its members. It returns nil when access
// narrows nothing, since the stored counts are then the viewer's, and
// otherwise a non-nil map. Stored members are counted in one statement; smart
// collections run their count queries in concurrent batches, and those the
// viewer can open nothing in are counted again without the viewer's access,
// because their cached count may predate their current members. A collection
// whose count could not be read is absent from the result, and the error says
// why.
func (r *LibraryCollectionRepository) ViewerCollectionCounts(ctx context.Context, collections []*models.LibraryCollection, access AccessFilter) (map[string]ViewerCollectionCount, error) {
	if !access.NarrowsCatalog() || len(collections) == 0 {
		return nil, nil
	}
	access = stripCatalogUserScope(access)
	counts := make(map[string]ViewerCollectionCount, len(collections))
	var stored []*models.LibraryCollection
	var smart []*models.LibraryCollection
	for _, c := range collections {
		if c == nil {
			continue
		}
		if collectionUsesLiveQuery(c) {
			smart = append(smart, c)
		} else {
			stored = append(stored, c)
		}
	}
	var failures []error
	if len(stored) > 0 {
		ids := make([]string, 0, len(stored))
		for _, c := range stored {
			ids = append(ids, c.ID)
		}
		visible, err := r.countVisibleStoredMembers(ctx, ids, access)
		if err != nil {
			failures = append(failures, err)
		} else {
			// The stored count is read live, so it says whether members exist.
			for _, c := range stored {
				n := visible[c.ID]
				counts[c.ID] = ViewerCollectionCount{Count: n, HidesMembers: n == 0 && c.ItemCount > 0}
			}
		}
	}
	visible, err := r.countSmartCollections(ctx, smart, access)
	if err != nil {
		failures = append(failures, err)
	}
	var none []*models.LibraryCollection
	for _, c := range smart {
		n, ok := visible[c.ID]
		switch {
		case !ok:
		case n > 0:
			counts[c.ID] = ViewerCollectionCount{Count: n}
		default:
			none = append(none, c)
		}
	}
	everyone, err := r.countSmartCollections(ctx, none, AccessFilter{})
	if err != nil {
		failures = append(failures, err)
	}
	for _, c := range none {
		if n, ok := everyone[c.ID]; ok {
			counts[c.ID] = ViewerCollectionCount{HidesMembers: n > 0}
		}
	}
	return counts, errors.Join(failures...)
}

// countSmartCollections counts the members each smart collection lists under
// access, as its catalog page resolves them: its rules narrowed to its
// libraries and access's, with its item limit. A collection whose count could
// not be read is absent from the result.
func (r *LibraryCollectionRepository) countSmartCollections(ctx context.Context, collections []*models.LibraryCollection, access AccessFilter) (map[string]int, error) {
	counts := make(map[string]int, len(collections))
	var queries []collectionCountStatement
	var failures []error
	for _, c := range collections {
		membership, err := ResolveLibraryCollectionMembership(c, access.AllowedLibraryIDs)
		if err != nil {
			failures = append(failures, fmt.Errorf("collection %s: %w", c.ID, err))
			continue
		}
		if membership.OutOfScope {
			counts[c.ID] = 0
			continue
		}
		executor := &QueryExecutor{Scope: membership.Query.MediaScope}
		sql, args, err := executor.buildCountQuery(membership.Query, access)
		if err != nil {
			failures = append(failures, fmt.Errorf("collection %s: %w", c.ID, err))
			continue
		}
		queries = append(queries, collectionCountStatement{id: c.ID, sql: sql, args: args})
	}
	batchCounts, batchFailures := runCollectionCountStatements(ctx, r.pool, queries)
	maps.Copy(counts, batchCounts)
	return counts, errors.Join(append(failures, batchFailures...)...)
}

// CollectionsShownToViewer returns copies of the server collections a user
// surface lists for a viewer, carrying that viewer's counts from
// ViewerCollectionCounts. With nil counts every collection is listed as
// stored. Otherwise a collection is left out when its count could not be read,
// or when it has members the viewer can open none of: either way its card
// would name titles the viewer's limits may hide, and a stored count would
// say how many. One that is empty for everyone stays listed.
func CollectionsShownToViewer(collections []*models.LibraryCollection, counts map[string]ViewerCollectionCount) []*models.LibraryCollection {
	if counts == nil {
		return collections
	}
	shown := make([]*models.LibraryCollection, 0, len(collections))
	for _, c := range collections {
		n, ok := counts[c.ID]
		if !ok || n.HidesMembers {
			continue
		}
		viewed := *c
		viewed.ItemCount = n.Count
		shown = append(shown, &viewed)
	}
	return shown
}

// countVisibleStoredMembers counts the stored members of each collection that
// access admits and the collection's catalog page lists. Collections with
// none are absent from the result.
func (r *LibraryCollectionRepository) countVisibleStoredMembers(ctx context.Context, collectionIDs []string, access AccessFilter) (map[string]int, error) {
	counts := make(map[string]int, len(collectionIDs))
	if access.AllowedLibraryIDs != nil && len(access.AllowedLibraryIDs) == 0 {
		return counts, nil
	}
	sql, args := buildCountVisibleStoredMembersSQL(collectionIDs, access)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("counting visible library collection members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scanning library collection member count: %w", err)
		}
		counts[id] = n
	}
	return counts, rows.Err()
}

func buildCountVisibleStoredMembersSQL(collectionIDs []string, access AccessFilter) (string, []any) {
	args := []any{collectionIDs}
	argIdx := 2
	var sql strings.Builder
	sql.WriteString(`SELECT lci.collection_id, COUNT(*)
            FROM library_collection_items lci
            JOIN media_items mi ON mi.content_id = lci.media_item_id
            WHERE lci.collection_id = ANY($1)`)
	for _, c := range itemAccessConditions(access, &args, &argIdx) {
		sql.WriteString("\n            AND " + c)
	}
	// The catalog page a collection card opens never lists manga chapters.
	sql.WriteString("\n            AND " + MangaChapterExclusionWhere("mi"))
	sql.WriteString("\n            GROUP BY lci.collection_id")
	return sql.String(), args
}
