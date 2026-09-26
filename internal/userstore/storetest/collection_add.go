package storetest

import (
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunCollectionAddIfAbsent covers an existing member outside the editor's first
// 200 rows. A picker may legitimately submit it again without loading all IDs;
// only explicit reordering is allowed to change its stored position.
func RunCollectionAddIfAbsent(t *testing.T, store userstore.UserStore) {
	t.Helper()
	ctx := t.Context()
	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Add if absent", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	pager, ok := store.(userstore.CollectionItemsPager)
	if !ok {
		t.Fatal("store does not implement collection paging")
	}
	for i := range 205 {
		if err := store.AddCollectionItem(ctx, collection.ID, fmt.Sprintf("member-%03d", i), i); err != nil {
			t.Fatal(err)
		}
	}
	first, err := pager.ListCollectionItemsPage(ctx, collection.ID, userstore.CollectionItemsPageOptions{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 200 || !first.HasMore {
		t.Fatalf("first editor page: %d rows, more=%v", len(first.Items), first.HasMore)
	}
	last := first.Items[199]
	opts := userstore.CollectionItemsPageOptions{Limit: 20, Revision: first.Revision, After: &userstore.CollectionItemPosition{Position: last.Position, MediaItemID: last.MediaItemID}}
	before, err := pager.ListCollectionItemsPage(ctx, collection.ID, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 5 || before.Items[4].MediaItemID != "member-204" {
		t.Fatalf("off-page fixture: %+v", before)
	}
	// Position zero matches the API picker request that previously moved the
	// existing off-page membership to the beginning of the collection.
	if err := store.AddCollectionItem(ctx, collection.ID, "member-204", 0); err != nil {
		t.Fatal(err)
	}
	after, err := pager.ListCollectionItemsPage(ctx, collection.ID, opts)
	if err != nil {
		t.Fatalf("duplicate add invalidated existing continuation: %v", err)
	}
	if len(after.Items) != 5 || after.HasMore || after.Items[4] != before.Items[4] || after.Revision != before.Revision {
		t.Fatalf("duplicate add moved/duplicated/retimestamped member: before=%+v after=%+v", before, after)
	}
	if err := store.AddCollectionItem(ctx, collection.ID, "new-member", 250); err != nil {
		t.Fatal(err)
	}
	opts.Revision, err = pager.CollectionRevision(ctx, collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := pager.ListCollectionItemsPage(ctx, collection.ID, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted.Items) != 6 || inserted.HasMore || inserted.Items[5].MediaItemID != "new-member" || inserted.Items[5].Position != 250 {
		t.Fatalf("fresh add ignored supplied position or duplicated membership: %+v", inserted)
	}
}
