package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
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

func TestAdminCollectionLookupAPIErrorPreservesFailureClass(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "missing", err: fmt.Errorf("lookup: %w", catalog.ErrLibraryCollectionNotFound), wantStatus: 404, wantCode: "not_found"},
		{name: "backend failure", err: errors.New("database unavailable"), wantStatus: 500, wantCode: "internal_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiErr, ok := errors.AsType[*APIError](adminCollectionLookupAPIError(tc.err))
			if !ok || apiErr.Status != tc.wantStatus || apiErr.Code != tc.wantCode {
				t.Fatalf("error = %#v, want status=%d code=%s", apiErr, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

func TestLegacyTraktAdminCollectionLibraryScopeIsImmutable(t *testing.T) {
	for _, tc := range []struct {
		name       string
		existing   *models.LibraryCollection
		libraryIDs []int
		wantError  bool
	}{
		{
			name: "collection type changed scope",
			existing: &models.LibraryCollection{
				CollectionType: "trakt", LibraryIDs: []int{1, 2},
			},
			libraryIDs: []int{2, 3}, wantError: true,
		},
		{
			name: "source config changed scope",
			existing: &models.LibraryCollection{
				CollectionType: "manual", LibraryIDs: []int{1, 2},
				SourceConfig: json.RawMessage(`{"provider":"trakt","mode":"trakt_list"}`),
			},
			libraryIDs: []int{2, 3}, wantError: true,
		},
		{
			name: "reordered duplicate scope is unchanged",
			existing: &models.LibraryCollection{
				CollectionType: "trakt", LibraryIDs: []int{1, 2},
			},
			libraryIDs: []int{2, 1, 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAdminCollectionSourceUpdate(tc.existing, AdminCollectionUpdate{LibraryIDs: &tc.libraryIDs})
			if tc.wantError {
				apiErr, ok := errors.AsType[*APIError](err)
				if !ok || apiErr.Code != "legacy_source_immutable" {
					t.Fatalf("error = %#v, want legacy_source_immutable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateAdminCollectionSourceUpdate: %v", err)
			}
		})
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
