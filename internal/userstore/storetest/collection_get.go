package storetest

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunGetMissingCollection checks that reading a collection that never existed,
// or was deleted, reports userstore.ErrCollectionNotFound. A Home row that
// still names a deleted collection relies on it to tell an expected empty row
// apart from a failing store.
func RunGetMissingCollection(t *testing.T, store userstore.UserStore) {
	t.Helper()
	ctx := t.Context()
	if _, err := store.GetCollection(ctx, "never-created"); !errors.Is(err, userstore.ErrCollectionNotFound) {
		t.Fatalf("GetCollection(never created) error = %v, want ErrCollectionNotFound", err)
	}
	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Deleted", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCollection(ctx, collection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCollection(ctx, collection.ID); !errors.Is(err, userstore.ErrCollectionNotFound) {
		t.Fatalf("GetCollection(deleted) error = %v, want ErrCollectionNotFound", err)
	}
}
