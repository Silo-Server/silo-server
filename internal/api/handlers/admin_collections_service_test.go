package handlers

import (
	"context"
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

func TestAdminCollectionServiceRejectsUnknownVisibility(t *testing.T) {
	h := &LibraryCollectionHandler{}
	_, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryID: 1, Title: "repro", Visibility: "public"})
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 400 || e.Field != "visibility" {
		t.Fatalf("create: expected visibility field error, got %v", err)
	}
	_, err = h.UpdateAdminCollection(t.Context(), "collection", AdminCollectionUpdate{Visibility: new("public")})
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 400 || e.Field != "visibility" {
		t.Fatalf("update: expected visibility field error, got %v", err)
	}
	for _, v := range []string{"", "visible", "hidden"} {
		if err := validateCollectionVisibility(v); err != nil {
			t.Fatalf("%q rejected: %v", v, err)
		}
	}
	if got, err := normalizeCollectionVisibilityUpdate(nil); err != nil || got != nil {
		t.Fatalf("nil update: got %v, %v", got, err)
	}
	for in, want := range map[string]string{"": adminCollectionVisible, adminCollectionHidden: adminCollectionHidden, adminCollectionVisible: adminCollectionVisible} {
		got, err := normalizeCollectionVisibilityUpdate(new(in))
		if err != nil || got == nil || *got != want {
			t.Fatalf("%q: got %v, %v, want %q", in, got, err, want)
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

// The web editor changes a franchise list's TMDB collection ID by sending a
// rebuilt tmdb_collection source; only Trakt sources are frozen.
func TestAdminCollectionSourceUpdateAcceptsFranchiseID(t *testing.T) {
	existing := &models.LibraryCollection{
		CollectionType: "tmdb",
		LibraryIDs:     []int{1},
		SourceURL:      "tmdb://collection/10",
		SourceConfig:   json.RawMessage(`{"mode":"tmdb_collection","collection_id":10}`),
	}
	err := validateAdminCollectionSourceUpdate(existing, AdminCollectionUpdate{
		SourceURL:    new("tmdb://collection/119"),
		SourceConfig: json.RawMessage(`{"mode":"tmdb_collection","collection_id":119,"limit":20}`),
	})
	if err != nil {
		t.Fatalf("validateAdminCollectionSourceUpdate: %v", err)
	}
}

// A variant that fails to delete must not leave an admin collection pointing
// at partly deleted artwork: the reference is cleared first and the failed
// cleanup only leaves orphans (issue #2419).
func TestDeleteAdminCollectionArtworkClearsReferenceWhenVariantDeleteFailsDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	created, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryID: f.library, Title: "Broken artwork", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id = $1`, created.ID)
	})
	for _, kind := range []string{collectionImagePoster, adminCollectionBackdrop} {
		t.Run(kind, func(t *testing.T) {
			dir := collectionImageDir(adminCollectionImagePrefix, created.ID, kind)
			path := dir + "/original.0000000000000001.webp"
			input := catalog.UpdateLibraryCollectionInput{ID: created.ID}
			if kind == collectionImagePoster {
				input.PosterURL = new(path)
			} else {
				input.BackdropURL = new(path)
			}
			if err := repo.Update(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			artwork := &partialDeleteStore{okKeys: 1, stubListDeleteStore: stubListDeleteStore{keys: []string{path, dir + "/w300.0000000000000001.webp"}}}
			h.ArtworkStore = artwork

			if err := h.DeleteAdminCollectionArtwork(t.Context(), created.ID, kind); err != nil {
				t.Fatalf("DeleteAdminCollectionArtwork: %v", err)
			}
			got, err := h.adminCollectionImagePath(t.Context(), created.ID, kind)
			if err != nil {
				t.Fatal(err)
			}
			if got != "" {
				t.Fatalf("%s reference = %q, want cleared", kind, got)
			}
			if len(artwork.deleted) != 1 {
				t.Fatalf("cleanup did not run after the clear: deleted %#v", artwork.deleted)
			}
		})
	}
}
