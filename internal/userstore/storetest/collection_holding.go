package storetest

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunManualCollectionsHolding covers the Add to collection ticks: of the
// account's collections, only the creator profile's own manual collections
// that hold the title are reported. Another profile's collection, shared or
// not, and a synced list holding the same title are left out.
func RunManualCollectionsHolding(t *testing.T, store userstore.UserStore) {
	t.Helper()
	const owner = "owner"
	ctx := t.Context()
	reader, ok := store.(userstore.CollectionMembershipReader)
	if !ok {
		t.Fatal("store does not implement collection membership reads")
	}
	create := func(creator, name, kind string, shared bool, items ...string) string {
		t.Helper()
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
			CreatorProfileID: creator, Name: name, CollectionType: kind, IsShared: shared,
		})
		if err != nil {
			t.Fatal(err)
		}
		for i, item := range items {
			if err := store.AddCollectionItem(ctx, c.ID, item, i); err != nil {
				t.Fatal(err)
			}
		}
		return c.ID
	}
	holds := create(owner, "Holds it", "manual", false, "other-title", "title")
	sharedHolds := create(owner, "Shared, holds it", "manual", true, "title")
	create(owner, "Lacks it", "manual", false, "other-title")
	create(owner, "Empty", "manual", false)
	create(owner, "Synced list", "mdblist", false, "title")
	create("viewer", "Another profile's", "manual", true, "title")

	got, err := reader.ManualCollectionsHolding(ctx, owner, "title")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{holds, sharedHolds}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("owner's collections holding title = %v, want %v", got, want)
	}

	for _, tc := range []struct{ profile, item string }{
		{owner, "missing-title"},
		{"nobody", "title"},
	} {
		got, err := reader.ManualCollectionsHolding(ctx, tc.profile, tc.item)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("%s's collections holding %s = %v, want none", tc.profile, tc.item, got)
		}
	}

	if err := store.RemoveCollectionItem(ctx, holds, "title"); err != nil {
		t.Fatal(err)
	}
	got, err = reader.ManualCollectionsHolding(ctx, owner, "title")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{sharedHolds}) {
		t.Fatalf("after removal = %v, want [%s]", got, sharedHolds)
	}
}
