package handlers

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestAdminCollectionServiceRejectsArtworkInDefinition(t *testing.T) {
	h := &LibraryCollectionHandler{}
	_, err := h.UpdateAdminCollection(t.Context(), "collection", AdminCollectionUpdate{PosterSourceURL: new("https://example.invalid/poster.png")})
	e, ok := errors.AsType[*APIError](err)
	if !ok || e.Status != 400 {
		t.Fatalf("expected validation before any DB or download, got %v", err)
	}
	for _, kind := range []string{"invalid", "poster"} {
		err := h.UploadAdminCollectionArtwork(t.Context(), "collection", kind, nil)
		if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 400 {
			t.Fatalf("invalid artwork: %v", err)
		}
	}
}

func TestAdminCollectionServiceCanonicalGuardAndMembershipDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	created, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryID: f.library, Title: "Admin editor", CollectionType: "manual", PosterURL: "poster/path", BackdropURL: "backdrop/path"})
	if err != nil {
		t.Fatal(err)
	}
	before, revision, err := h.GetAdminCollection(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.PosterURL != "" || before.BackdropURL != "" {
		t.Fatal("canonical editor contains artwork URLs")
	}
	_, err = h.UpdateAdminCollection(WithAdminCollectionExpectedRevision(t.Context(), revision), created.ID, AdminCollectionUpdate{Title: new("Updated")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.UpdateAdminCollection(WithAdminCollectionExpectedRevision(t.Context(), revision), created.ID, AdminCollectionUpdate{Title: new("Stale")})
	if !errors.Is(err, catalog.ErrLibraryCollectionRevisionMismatch) {
		t.Fatalf("stale update lost conflict: %v", err)
	}
	if err = h.AddAdminCollectionItem(t.Context(), created.ID, f.ids[0], 7); err == nil {
		t.Fatal("added member outside collection libraries")
	}
	if err = h.AddAdminCollectionItem(t.Context(), created.ID, f.ids[1], 217); err != nil {
		t.Fatal(err)
	}
	if err = h.AddAdminCollectionItem(t.Context(), created.ID, f.ids[1], 0); err != nil {
		t.Fatal(err)
	}
	items, err := repo.ListItems(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Position != 217 {
		t.Fatalf("duplicate add changed membership: %+v", items)
	}
	if err = h.DeleteAdminCollection(WithAdminCollectionExpectedRevision(t.Context(), revision), created.ID); !errors.Is(err, catalog.ErrLibraryCollectionRevisionMismatch) {
		t.Fatalf("stale delete lost conflict: %v", err)
	}
	current, revision, err := h.GetAdminCollection(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Title != "Updated" {
		t.Fatalf("stale mutation changed title: %s", current.Title)
	}
	if err = h.DeleteAdminCollection(WithAdminCollectionExpectedRevision(t.Context(), revision), created.ID); err != nil {
		t.Fatal(err)
	}
}
