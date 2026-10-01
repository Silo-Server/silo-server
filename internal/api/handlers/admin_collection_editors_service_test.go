package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type adminEditorFixture struct {
	pagingIntegrationFixture
	repo         *catalog.LibraryCollectionRepository
	groups       *catalog.LibraryCollectionGroupRepository
	handler      *LibraryCollectionHandler
	groupHandler *LibraryCollectionGroupHandler
}

func newAdminEditorFixture(t *testing.T) adminEditorFixture {
	t.Helper()
	f := newPagingIntegrationFixture(t)
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	groups := catalog.NewLibraryCollectionGroupRepository(f.pool)
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	h.GroupRepo = groups
	return adminEditorFixture{f, repo, groups, h, NewLibraryCollectionGroupHandler(groups, repo, f.pool)}
}
func (f adminEditorFixture) collection(t *testing.T, input catalog.CreateLibraryCollectionInput) *models.LibraryCollection {
	t.Helper()
	input.LibraryID = f.library
	if input.Slug == "" {
		input.Slug = input.Title
	}
	c, err := f.repo.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.repo.Delete(context.Background(), c.ID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, c.ID)
	})
	return c
}
func (f adminEditorFixture) group(t *testing.T, libraryID int, name string) *models.LibraryCollectionGroup {
	t.Helper()
	g, err := f.groups.Create(t.Context(), catalog.CreateLibraryCollectionGroupInput{LibraryID: libraryID, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestAdminCollectionEditorSavedOrderDB(t *testing.T) {
	f := newAdminEditorFixture(t)
	ctx := t.Context()
	normal := f.collection(t, catalog.CreateLibraryCollectionInput{Title: "Normal"})
	featured := f.collection(t, catalog.CreateLibraryCollectionInput{Title: "Featured", Featured: true})
	hidden := f.collection(t, catalog.CreateLibraryCollectionInput{Title: "Hidden", Visibility: "hidden"})
	group := f.group(t, f.library, "Grouped")
	grouped := f.collection(t, catalog.CreateLibraryCollectionInput{Title: "Grouped hidden", Visibility: "hidden", GroupID: &group.ID})
	want := []string{hidden.ID, normal.ID, featured.ID}
	if err := f.repo.ReorderCollections(ctx, f.library, nil, want); err != nil {
		t.Fatal(err)
	}
	got, err := f.handler.AdminCollectionOrder(ctx, f.library, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.OrderedIDs, want) || got.HasMore || got.Revision == 0 {
		t.Fatalf("saved order promoted featured or excluded hidden: %+v", got)
	}
	groupedView, err := f.handler.AdminCollectionOrder(ctx, f.library, &group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(groupedView.OrderedIDs, []string{grouped.ID}) {
		t.Fatalf("hidden group membership=%v", groupedView.OrderedIDs)
	}
	// Position ties use IDs, rather than titles or featured promotion.
	f.exec(t, `UPDATE library_collection_libraries SET sort_order=0 WHERE library_id=$1 AND collection_id=ANY($2::text[])`, f.library, []string{normal.ID, hidden.ID})
	ties := []string{normal.ID, hidden.ID}
	slices.Sort(ties)
	got, err = f.handler.AdminCollectionOrder(ctx, f.library, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.OrderedIDs, append(ties, featured.ID)) {
		t.Fatalf("position tie order=%v", got.OrderedIDs)
	}
}

func TestAdminCollectionEditorScopesDB(t *testing.T) {
	f := newAdminEditorFixture(t)
	ctx := t.Context()
	foreign := f.group(t, f.hidden, "Foreign")
	var missingLibrary int
	if err := f.pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies','removed-editor-library',true) RETURNING id`).Scan(&missingLibrary); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `DELETE FROM media_folders WHERE id=$1`, missingLibrary)
	tests := []struct {
		name string
		run  func() error
		want error
	}{
		{"unknown-library", func() error { _, err := f.handler.AdminCollectionOrder(ctx, missingLibrary, nil); return err }, catalog.ErrLibraryCollectionNotFound},
		{"unknown-group", func() error {
			_, err := f.handler.AdminCollectionOrder(ctx, f.library, new("missing-editor-group"))
			return err
		}, catalog.ErrLibraryCollectionGroupNotFound},
		{"cross-library-group", func() error { _, err := f.handler.AdminCollectionOrder(ctx, f.library, &foreign.ID); return err }, catalog.ErrLibraryCollectionGroupNotFound},
		{"group-order-unknown-library", func() error { _, err := f.groupHandler.AdminCollectionGroupOrder(ctx, missingLibrary); return err }, catalog.ErrLibraryCollectionNotFound},
		{"group-editor-unknown-group", func() error {
			_, _, err := f.groupHandler.GetAdminCollectionGroup(ctx, "missing-editor-group")
			return err
		}, catalog.ErrLibraryCollectionGroupNotFound},
		{"membership-editor-unknown-group", func() error {
			_, err := f.groupHandler.AdminGroupCollectionOrder(ctx, "missing-editor-group", f.library)
			return err
		}, catalog.ErrLibraryCollectionGroupNotFound},
		{"ungrouped-unknown-library", func() error {
			_, err := f.groupHandler.AdminGroupCollectionOrder(ctx, "ungrouped", missingLibrary)
			return err
		}, catalog.ErrLibraryCollectionNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, tc.want) {
				t.Fatalf("expected not-found domain error %v, got %v", tc.want, err)
			}
		})
	}
}

func TestAdminCollectionGroupEditorCanonicalRevisionDB(t *testing.T) {
	f := newAdminEditorFixture(t)
	ctx := t.Context()
	first := f.group(t, f.library, "First")
	second := f.group(t, f.library, "Second")
	want := []string{first.ID, "ungrouped", second.ID}
	if err := f.groups.Reorder(ctx, f.library, want); err != nil {
		t.Fatal(err)
	}
	order, err := f.groupHandler.AdminCollectionGroupOrder(ctx, f.library)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order.OrderedIDs, want) || order.HasMore {
		t.Fatalf("synthetic group order=%+v", order)
	}
	firstView, revision, err := f.groupHandler.GetAdminCollectionGroup(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, againRevision, err := f.groupHandler.GetAdminCollectionGroup(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(value any) []byte {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if revision != order.Revision || againRevision != revision || !bytes.Equal(encode(firstView), encode(again)) {
		t.Fatal("unchanged canonical group bytes/witness differ")
	}
	// A sibling group changes the shared ordering witness used by the API's
	// strong tag, even when this group's own canonical fields remain identical.
	if _, err := f.groups.Update(ctx, second.ID, catalog.UpdateLibraryCollectionGroupInput{Name: new("Renamed sibling")}); err != nil {
		t.Fatal(err)
	}
	current, currentRevision, err := f.groupHandler.GetAdminCollectionGroup(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if currentRevision == revision || !bytes.Equal(encode(firstView), encode(current)) {
		t.Fatal("sibling update failed to advance shared canonical witness")
	}
	desired := []string{second.ID, "ungrouped", first.ID}
	if err := f.groupHandler.ReorderAdminCollectionGroups(WithAdminCollectionExpectedRevision(ctx, revision), f.library, desired); !errors.Is(err, catalog.ErrLibraryCollectionRevisionMismatch) {
		t.Fatalf("stale canonical reorder: %v", err)
	}
	after, err := f.groupHandler.AdminCollectionGroupOrder(ctx, f.library)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after.OrderedIDs, want) || after.Revision != currentRevision {
		t.Fatal("stale reorder mutated canonical order")
	}
	if err := f.groupHandler.ReorderAdminCollectionGroups(WithAdminCollectionExpectedRevision(ctx, currentRevision), f.library, desired); err != nil {
		t.Fatal(err)
	}
	after, err = f.groupHandler.AdminCollectionGroupOrder(ctx, f.library)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after.OrderedIDs, desired) || after.Revision == currentRevision {
		t.Fatal("fresh reorder did not update canonical order and witness")
	}
}

func TestAdminCollectionManualEditorContinuationDB(t *testing.T) {
	f := newAdminEditorFixture(t)
	ctx := t.Context()
	c := f.collection(t, catalog.CreateLibraryCollectionInput{Title: "Manual", CollectionType: "manual"})
	for _, id := range f.ids[1:] {
		if err := f.handler.AddAdminCollectionItem(ctx, c.ID, id, 0); err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.handler.AdminCollectionItemsPage(ctx, c.ID, userstore.CollectionItemsPageOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range first.Items {
		if first.Titles[item.MediaItemID] != "Same title" {
			t.Fatalf("missing catalog title: %+v", first)
		}
	}
	if len(first.Items) != 2 || !first.HasMore || first.Revision == 0 {
		t.Fatalf("first manual page=%+v", first)
	}
	last := first.Items[len(first.Items)-1]
	opts := userstore.CollectionItemsPageOptions{Limit: 2, Revision: first.Revision, After: &userstore.CollectionItemPosition{Position: last.Position, MediaItemID: last.MediaItemID}}
	next, err := f.handler.AdminCollectionItemsPage(ctx, c.ID, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 2 || next.HasMore || next.Revision != first.Revision {
		t.Fatalf("manual continuation=%+v", next)
	}
	var ids []string
	for _, page := range []AdminCollectionItemsPageView{first, next} {
		for _, item := range page.Items {
			ids = append(ids, item.MediaItemID)
		}
	}
	if !slices.Equal(ids, f.ids[1:]) {
		t.Fatalf("tie continuation=%v", ids)
	}
	if err := f.handler.AddAdminCollectionItem(ctx, c.ID, f.ids[4], 99); err != nil {
		t.Fatal(err)
	}
	stable, err := f.handler.AdminCollectionItemsPage(ctx, c.ID, opts)
	if err != nil || stable.Revision != first.Revision {
		t.Fatalf("duplicate add invalidated stable page: %v", err)
	}
	if err := f.repo.Update(ctx, catalog.UpdateLibraryCollectionInput{ID: c.ID, Title: new("Definition changed")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.handler.AdminCollectionItemsPage(ctx, c.ID, opts); !errors.Is(err, userstore.ErrCollectionChanged) {
		t.Fatalf("changed definition accepted old continuation: %v", err)
	}
	for _, kind := range []string{"smart", "mdblist"} {
		t.Run(kind, func(t *testing.T) {
			other := f.collection(t, catalog.CreateLibraryCollectionInput{Title: fmt.Sprintf("%s collection", kind), CollectionType: kind})
			_, err := f.handler.AdminCollectionItemsPage(t.Context(), other.ID, userstore.CollectionItemsPageOptions{Limit: 2})
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Status != 409 || apiErr.Code != "collection_not_manual" {
				t.Fatalf("nonmanual editor: %v", err)
			}
		})
	}
	if _, err := f.handler.AdminCollectionItemsPage(ctx, "missing-editor-collection", userstore.CollectionItemsPageOptions{Limit: 2}); !errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
		t.Fatalf("missing manual editor: %v", err)
	}
}
