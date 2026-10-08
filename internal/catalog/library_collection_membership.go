package catalog

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// LibraryCollectionMembership says where a library collection's members come
// from: its stored items, or for a live collection the titles Query matches
// when it is read.
type LibraryCollectionMembership struct {
	// Live is true when Query selects the members and false when they are the
	// collection's stored items.
	Live bool
	// Query selects a live collection's members: its rules narrowed to the
	// collection's libraries and the requested scope, with the smart item
	// limit applied. Its sort and limit decide which titles are members.
	Query QueryDefinition
	// OutOfScope reports that a live collection has no members because none of
	// its libraries is in scope. Query must not run then: its empty LibraryIDs
	// would mean every library.
	OutOfScope bool
	// Sort is the order the members are listed in when the viewer picks none:
	// the creator's default from sort_config, otherwise Query's own sort for a
	// live collection. It is empty for a stored collection without a default,
	// which keeps its curated order.
	Sort QuerySort
}

// ResolveLibraryCollectionMembership decides how a library collection's
// members are read, for every reader that lists them (rails, the collection's
// browse page, collages), so they all agree.
//
// collection_type alone decides, matching how members are written: only a
// smart collection is live, and it never stores items (sync refuses it). Every
// other type keeps its members in library_collection_items, written by manual
// edits and syncs. A query_definition on a non-smart collection is ignored:
// the admin API accepts one on any type, and a PATCH without collection_type
// normalizes it as smart, but neither makes the collection live.
//
// scope lists the libraries the reader may list, nil for no limit. Library
// collections are shared across profiles, so a query with per-profile rules or
// sorts is an error.
func ResolveLibraryCollectionMembership(c *models.LibraryCollection, scope []int) (LibraryCollectionMembership, error) {
	defaultSort, hasDefaultSort := ParseCollectionDefaultSort(c.SortConfig, false)
	if !IsLiveQueryType(c.CollectionType) {
		return LibraryCollectionMembership{Sort: defaultSort}, nil
	}

	def, err := parseCatalogCollectionQueryDefinition(c.QueryDefinition)
	if err != nil {
		return LibraryCollectionMembership{}, err
	}
	if err := def.ValidateWithOptions(false, false); err != nil {
		return LibraryCollectionMembership{}, err
	}
	m := LibraryCollectionMembership{Live: true, Sort: def.Sort}
	if hasDefaultSort {
		m.Sort = defaultSort
	}

	var collectionLibraries []int
	switch {
	case len(c.LibraryIDs) > 0:
		collectionLibraries = c.LibraryIDs
	case c.LibraryID > 0:
		collectionLibraries = []int{c.LibraryID}
	}
	for _, allowed := range [][]int{collectionLibraries, scope} {
		if allowed == nil {
			continue
		}
		if len(def.LibraryIDs) == 0 {
			def.LibraryIDs = slices.Clone(allowed)
		} else {
			def.LibraryIDs = intersectInts(def.LibraryIDs, allowed)
		}
		if len(def.LibraryIDs) == 0 {
			m.OutOfScope = true
			break
		}
	}
	m.Query = ApplySmartCollectionItemLimit(def)
	return m, nil
}

// PreviewLiveLibraryCollection lists up to limit members of a live collection
// in m.Sort order, with the number of members access admits. It orders them
// the way the collection's browse page does: a sort other than the query's own
// reorders the members without choosing different ones, so a capped query
// keeps the titles its own sort ranks first. The members never depend on the
// viewer's profile, because library collections are shared.
func PreviewLiveLibraryCollection(ctx context.Context, pool *pgxpool.Pool, m LibraryCollectionMembership, access AccessFilter, limit int) ([]*models.MediaItem, int, error) {
	if !m.Live || m.OutOfScope {
		return []*models.MediaItem{}, 0, nil
	}
	access = stripCatalogUserScope(access)
	executor := &QueryExecutor{Pool: pool, Scope: m.Query.MediaScope}
	def := m.Query
	if m.Sort != def.Sort {
		predicate, args, err := collectionDefinitionPredicate(executor, def, access)
		if err != nil {
			return nil, 0, err
		}
		executor.SourceWhere, executor.SourceArgs = predicate, args
		def = QueryDefinition{Sort: m.Sort}
	}
	return executor.Preview(ctx, def, access, limit)
}
