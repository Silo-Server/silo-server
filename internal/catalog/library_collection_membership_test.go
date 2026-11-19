package catalog

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestResolveLibraryCollectionMembershipNarrowsToScope(t *testing.T) {
	// Shown in 5, 9 and the legacy single library 7, so every reader below
	// with a scope can reach it unless a case says otherwise.
	named := &models.LibraryCollection{
		CollectionType:  "smart",
		LibraryIDs:      []int{5, 9},
		QueryDefinition: json.RawMessage(`{"library_ids":[1,2]}`),
	}
	every := &models.LibraryCollection{
		CollectionType:  "smart",
		LibraryIDs:      []int{5, 9},
		QueryDefinition: json.RawMessage(`{}`),
	}
	legacy := &models.LibraryCollection{
		CollectionType:  "smart",
		LibraryID:       7,
		QueryDefinition: json.RawMessage(`{}`),
	}
	for _, tc := range []struct {
		name       string
		collection *models.LibraryCollection
		scope      []int
		disabled   []int
		want       []int
		outOfScope bool
	}{
		{name: "no scope", collection: named, scope: nil, want: []int{1, 2}},
		{name: "overlapping scope", collection: named, scope: []int{2, 5}, want: []int{2}},
		{name: "disjoint scope", collection: named, scope: []int{5}, outOfScope: true},
		{name: "empty scope", collection: named, scope: []int{}, outOfScope: true},
		{name: "every library, no scope", collection: every, scope: nil},
		{name: "every library, scoped reader", collection: every, scope: []int{2, 5}, want: []int{2, 5}},
		{name: "every library, empty scope", collection: every, scope: []int{}, outOfScope: true},
		// A reader who can't reach a library it shows in lists none of it, as
		// its page refuses them, though the rules match titles they may see.
		{name: "shown only outside the scope", collection: named, scope: []int{1, 2}, outOfScope: true},
		{name: "every library, shown only outside the scope", collection: every, scope: []int{2}, outOfScope: true},
		{name: "legacy single library in scope", collection: legacy, scope: []int{2, 7}, want: []int{2, 7}},
		{name: "legacy single library outside the scope", collection: legacy, scope: []int{2}, outOfScope: true},
		// A reader who hid every library it shows in can't reach it either.
		{name: "every library it shows in hidden", collection: every, disabled: []int{5, 9}, outOfScope: true},
		{name: "one library it shows in hidden", collection: every, disabled: []int{9}},
		{name: "scoped reader hid the library it shares", collection: named, scope: []int{1, 2, 5}, disabled: []int{5}, outOfScope: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ResolveLibraryCollectionMembership(tc.collection, AccessFilter{AllowedLibraryIDs: tc.scope, DisabledLibraryIDs: tc.disabled})
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

func TestResolveLibraryCollectionMembershipHasNoMembersOutsideScope(t *testing.T) {
	// The query names only a library the reader may not list, though the
	// collection shows in one they may. An empty library list would otherwise
	// run the query over every library.
	c := &models.LibraryCollection{
		CollectionType:  "smart",
		LibraryIDs:      []int{1},
		QueryDefinition: json.RawMessage(`{"library_ids":[2]}`),
	}
	m, err := ResolveLibraryCollectionMembership(c, AccessFilter{AllowedLibraryIDs: []int{1}})
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
