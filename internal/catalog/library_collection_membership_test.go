package catalog

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestResolveLibraryCollectionMembershipReadsStoredItemsForNonSmartTypes(t *testing.T) {
	for _, collectionType := range []string{"manual", "mdblist", "tmdb", "trakt"} {
		t.Run(collectionType, func(t *testing.T) {
			// A query_definition on a non-smart collection does not make it
			// live: its members are the stored items its writers maintain.
			c := &models.LibraryCollection{
				CollectionType:  collectionType,
				LibraryIDs:      []int{1},
				QueryDefinition: json.RawMessage(`{"media_scope":"movie","sort":{"field":"year","order":"desc"}}`),
				SortConfig:      json.RawMessage(`{"field":"title","order":"asc"}`),
			}
			m, err := ResolveLibraryCollectionMembership(c, nil)
			if err != nil {
				t.Fatalf("ResolveLibraryCollectionMembership: %v", err)
			}
			if m.Live {
				t.Fatalf("membership = %+v, want stored items", m)
			}
			if m.Sort != (QuerySort{Field: "title", Order: "asc"}) {
				t.Fatalf("sort = %+v, want the sort_config default", m.Sort)
			}
		})
	}
}

func TestResolveLibraryCollectionMembershipKeepsCuratedOrderWithoutDefaultSort(t *testing.T) {
	m, err := ResolveLibraryCollectionMembership(&models.LibraryCollection{CollectionType: "manual", SortConfig: json.RawMessage(`{}`)}, nil)
	if err != nil {
		t.Fatalf("ResolveLibraryCollectionMembership: %v", err)
	}
	if m.Live || m.Sort != (QuerySort{}) {
		t.Fatalf("membership = %+v, want stored items in curated order", m)
	}
}

func TestResolveLibraryCollectionMembershipPreparesSmartQuery(t *testing.T) {
	c := &models.LibraryCollection{
		CollectionType:  "smart",
		LibraryIDs:      []int{1, 2},
		QueryDefinition: json.RawMessage(`{"media_scope":"movie","library_ids":[2,3],"sort":{"field":"year","order":"desc"}}`),
	}
	m, err := ResolveLibraryCollectionMembership(c, nil)
	if err != nil {
		t.Fatalf("ResolveLibraryCollectionMembership: %v", err)
	}
	if !m.Live || m.OutOfScope {
		t.Fatalf("membership = %+v, want a live query", m)
	}
	if !slices.Equal(m.Query.LibraryIDs, []int{2}) {
		t.Fatalf("library_ids = %v, want the query's libraries the collection is bound to", m.Query.LibraryIDs)
	}
	if m.Query.Limit == nil || *m.Query.Limit != DefaultSmartCollectionItemLimit {
		t.Fatalf("limit = %v, want the uncapped smart default", m.Query.Limit)
	}
	if m.Query.MediaScope != "movie" {
		t.Fatalf("media_scope = %q, want movie", m.Query.MediaScope)
	}
	want := QuerySort{Field: "year", Order: "desc"}
	if m.Query.Sort != want || m.Sort != want {
		t.Fatalf("query sort %+v, list sort %+v; want both %+v without a sort_config default", m.Query.Sort, m.Sort, want)
	}
}

func TestResolveLibraryCollectionMembershipListsSmartMembersInDefaultSort(t *testing.T) {
	c := &models.LibraryCollection{
		CollectionType:  "smart",
		LibraryID:       4,
		QueryDefinition: json.RawMessage(`{"sort":{"field":"year","order":"desc"},"limit":25}`),
		SortConfig:      json.RawMessage(`{"field":"title","order":"asc"}`),
	}
	m, err := ResolveLibraryCollectionMembership(c, nil)
	if err != nil {
		t.Fatalf("ResolveLibraryCollectionMembership: %v", err)
	}
	// The query's own sort and limit still choose the members; the default
	// sort only orders them.
	if m.Query.Sort != (QuerySort{Field: "year", Order: "desc"}) || m.Query.Limit == nil || *m.Query.Limit != 25 {
		t.Fatalf("query = %+v, want its own sort and limit kept", m.Query)
	}
	if m.Sort != (QuerySort{Field: "title", Order: "asc"}) {
		t.Fatalf("sort = %+v, want the sort_config default", m.Sort)
	}
	if !slices.Equal(m.Query.LibraryIDs, []int{4}) {
		t.Fatalf("library_ids = %v, want the legacy single library", m.Query.LibraryIDs)
	}
}

func TestResolveLibraryCollectionMembershipNarrowsToScope(t *testing.T) {
	c := &models.LibraryCollection{
		CollectionType:  "smart",
		LibraryIDs:      []int{1, 2},
		QueryDefinition: json.RawMessage(`{}`),
	}
	for _, tc := range []struct {
		name       string
		scope      []int
		want       []int
		outOfScope bool
	}{
		{name: "no scope", scope: nil, want: []int{1, 2}},
		{name: "overlapping scope", scope: []int{2, 5}, want: []int{2}},
		{name: "disjoint scope", scope: []int{5}, outOfScope: true},
		{name: "empty scope", scope: []int{}, outOfScope: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ResolveLibraryCollectionMembership(c, tc.scope)
			if err != nil {
				t.Fatalf("ResolveLibraryCollectionMembership: %v", err)
			}
			if !m.Live || m.OutOfScope != tc.outOfScope {
				t.Fatalf("membership = %+v, want live with out_of_scope=%v", m, tc.outOfScope)
			}
			if !tc.outOfScope && !slices.Equal(m.Query.LibraryIDs, tc.want) {
				t.Fatalf("library_ids = %v, want %v", m.Query.LibraryIDs, tc.want)
			}
		})
	}
}

func TestResolveLibraryCollectionMembershipHasNoMembersOutsideItsLibraries(t *testing.T) {
	// The query names only libraries the collection is not bound to. An empty
	// library list would otherwise run the query over every library.
	c := &models.LibraryCollection{
		CollectionType:  "smart",
		LibraryIDs:      []int{1},
		QueryDefinition: json.RawMessage(`{"library_ids":[2]}`),
	}
	m, err := ResolveLibraryCollectionMembership(c, []int{1, 2})
	if err != nil {
		t.Fatalf("ResolveLibraryCollectionMembership: %v", err)
	}
	if !m.Live || !m.OutOfScope {
		t.Fatalf("membership = %+v, want a live collection with no members", m)
	}
	// No query runs, so no database is needed to answer it.
	items, total, err := PreviewLiveLibraryCollection(t.Context(), nil, m, AccessFilter{}, 20)
	if err != nil || len(items) != 0 || total != 0 {
		t.Fatalf("preview = %v (total %d, err %v), want nothing", items, total, err)
	}
}

func TestResolveLibraryCollectionMembershipRunsUnboundSmartQueryEverywhere(t *testing.T) {
	// The repository reads a collection without library bindings as an empty
	// list, which binds it to nothing rather than to no library.
	m, err := ResolveLibraryCollectionMembership(&models.LibraryCollection{CollectionType: "smart", LibraryIDs: []int{}}, nil)
	if err != nil {
		t.Fatalf("ResolveLibraryCollectionMembership: %v", err)
	}
	if !m.Live || m.OutOfScope || len(m.Query.LibraryIDs) != 0 {
		t.Fatalf("membership = %+v, want a live query over every library", m)
	}
}

func TestResolveLibraryCollectionMembershipRejectsUnusableQueries(t *testing.T) {
	for name, raw := range map[string]string{
		"malformed":         `{`,
		"personalized rule": `{"groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`,
		"personalized sort": `{"sort":{"field":"progress","order":"desc"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			// Library collections are shared across profiles, so their query
			// must not depend on who is watching.
			c := &models.LibraryCollection{CollectionType: "smart", QueryDefinition: json.RawMessage(raw)}
			if m, err := ResolveLibraryCollectionMembership(c, nil); err == nil {
				t.Fatalf("membership = %+v, want an error", m)
			}
		})
	}
}
