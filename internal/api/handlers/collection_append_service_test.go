package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestPersonalCollectionAppendBoundariesDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	t.Logf("append cleanup account=%d libraries=%d,%d", f.account, f.library, f.hidden)
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(t.Context(), f.account)
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "append boundary", CollectionType: "manual", IsShared: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(t.Context())
		// Delete the owned collection before the fixture deletes its account, so
		// no later cascade recreates the order witness after this cleanup.
		if _, err := f.pool.Exec(cleanup, `DELETE FROM user_personal_collections WHERE user_id=$1 AND id=$2`, f.account, c.ID); err != nil {
			t.Error(err)
		}
		if _, err := f.pool.Exec(cleanup, `DELETE FROM user_collection_order_revisions WHERE user_id=$1`, f.account); err != nil {
			t.Error(err)
		}
	})
	h := NewCollectionHandler(provider)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	ctx := access.SetScope(t.Context(), access.Scope{AllowedLibraryIDs: []int{f.library}})
	for _, tc := range []struct {
		profile, item string
		want          int
	}{
		{"viewer", f.ids[1], 403},
		{"", f.ids[1], 404},
		{"owner", f.ids[0], 404},
		{"owner", "missing-append-item", 404},
	} {
		err := h.AppendPersonalCollectionItem(ctx, f.account, tc.profile, c.ID, tc.item)
		got, ok := errors.AsType[*APIError](err)
		if !ok || got.Status != tc.want {
			t.Fatalf("profile=%s item=%s: %v want %d", tc.profile, tc.item, err, tc.want)
		}
	}
	if err := h.AppendPersonalCollectionItem(ctx, f.account, "owner", c.ID, f.ids[1]); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListCollectionItems(ctx, c.ID)
	if err != nil || len(items) != 1 || items[0].MediaItemID != f.ids[1] || items[0].Position != 0 {
		t.Fatalf("denied append changed membership: %+v, %v", items, err)
	}
	for _, kind := range []string{"smart", "mdblist"} {
		f.exec(t, `UPDATE user_personal_collections SET collection_type=$3 WHERE user_id=$1 AND id=$2`, f.account, c.ID, kind)
		err := h.AppendPersonalCollectionItem(ctx, f.account, "owner", c.ID, f.ids[2])
		got, ok := errors.AsType[*APIError](err)
		if !ok || got.Status != 409 {
			t.Fatalf("%s append: %v", kind, err)
		}
	}
}
