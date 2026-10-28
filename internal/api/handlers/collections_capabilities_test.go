package handlers

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Every advertised sort-preference kind must be accepted by the sort
// preference writer, or a client that trusts the capability gets a 400.
func TestCollectionCapabilitiesAdvertiseOnlyAcceptedSortKinds(t *testing.T) {
	got := NewCollectionHandler(nil).Capabilities()
	for _, kind := range got.SortPreferenceKinds {
		if _, _, ok := normalizeCollectionRef(kind, "collection-1"); !ok {
			t.Fatalf("advertised kind %q is rejected by normalizeCollectionRef", kind)
		}
	}
	for _, want := range []string{
		userstore.CollectionKindLibrary,
		userstore.CollectionKindUser,
		userstore.CollectionKindWatchlist,
		userstore.CollectionKindFavorites,
	} {
		if !slices.Contains(got.SortPreferenceKinds, want) {
			t.Fatalf("sort_preference_kinds = %v, missing %q", got.SortPreferenceKinds, want)
		}
	}
}
