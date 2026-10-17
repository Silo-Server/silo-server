package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestAdminCollectionServiceRejectsUnknownCollectionType(t *testing.T) {
	h := &LibraryCollectionHandler{}
	// Trakt collections were retired, so trakt is an unknown type too.
	for _, v := range []string{"bogus", "trakt"} {
		_, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryID: 1, Title: "repro", CollectionType: v})
		if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 400 || e.Field != "collection_type" {
			t.Fatalf("create %q: expected collection_type field error, got %v", v, err)
		}
	}
	var err error
	for _, v := range []string{"bogus", "trakt", ""} {
		_, err = h.UpdateAdminCollection(t.Context(), "collection", AdminCollectionUpdate{CollectionType: new(v)})
		if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 400 || e.Field != "collection_type" {
			t.Fatalf("update %q: expected collection_type field error, got %v", v, err)
		}
	}
	for _, v := range []string{"manual", "smart", "mdblist", "tmdb"} {
		if err := validateCollectionType(v); err != nil {
			t.Fatalf("%q rejected: %v", v, err)
		}
	}
	// An omitted type defaults to manual on create, so it must get past the
	// type check; the missing title is what stops this request.
	_, err = h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryID: 1})
	if e, ok := errors.AsType[*APIError](err); !ok || e.Field == "collection_type" || e.Code != "bad_request" {
		t.Fatalf("create without a type: expected the title error, got %v", err)
	}
}

// The v1 bridge routes share the service validation and answer it as a 400.
func TestAdminCollectionV1RoutesRejectUnknownCollectionType(t *testing.T) {
	h := &LibraryCollectionHandler{}
	for _, tc := range []struct {
		method string
		serve  func(http.ResponseWriter, *http.Request)
	}{
		{http.MethodPost, h.HandleCreateAdminCollection},
		{http.MethodPut, h.HandleUpdateAdminCollection},
	} {
		req := accessGroupRequestWithID(tc.method, "/api/v1/admin/collections/c1", strings.NewReader(`{"title":"repro","library_id":1,"collection_type":"bogus"}`), "c1")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		tc.serve(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "collection_type") {
			t.Fatalf("%s: got %d %s", tc.method, rec.Code, rec.Body)
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

// A v1 update that switches a collection to Smart activates its stored
// rules, so it is refused when they use the v2 vocabulary; v2 may switch it.
func TestAdminCollectionV1SmartSwitchKeepsTheV1RuleVocabularyDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	created, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{
		LibraryID: f.library, Title: "The films", CollectionType: "manual",
		QueryDefinition: json.RawMessage(`{"match":"all","groups":[{"match":"all","rules":[{"field":"title","op":"begins_with","value":"the "}]}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id = $1`, created.ID)
	})
	smart := "smart"
	if _, err := h.updateAdminCollection(t.Context(), created.ID, AdminCollectionUpdate{CollectionType: &smart, V1Rules: true}, func(string, string, string) error { return nil }); err == nil {
		t.Fatal("v1 switched to Smart with a title rule stored")
	}
	if _, err := h.UpdateAdminCollection(t.Context(), created.ID, AdminCollectionUpdate{CollectionType: &smart}); err != nil {
		t.Fatalf("v2 switch to Smart: %v", err)
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
