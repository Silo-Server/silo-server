package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
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

// TestCollectionNameErrorNamesTheField: a taken name or slug is a field
// error, which v2 answers as 422 validation_failed at body.title or
// body.slug and v1 as 400, never a 500.
func TestCollectionNameErrorNamesTheField(t *testing.T) {
	slugConflict := &pgconn.PgError{Code: "23505", ConstraintName: "library_collections_library_slug_unique"}
	for _, tc := range []struct {
		err       error
		wantField string
		wantMsg   string
	}{
		{err: fmt.Errorf("updating: %w", &catalog.LibraryCollectionTitleTakenError{LibraryName: "Movies"}), wantField: "title", wantMsg: "Another collection in Movies already has this name"},
		{err: fmt.Errorf("updating library collection: %w", slugConflict), wantField: "slug"},
		{err: &pgconn.PgError{Code: "23505", ConstraintName: "some_other_unique"}},
		{err: errors.New("connection reset")},
	} {
		got := collectionNameError(tc.err)
		if tc.wantField == "" {
			if got != nil {
				t.Fatalf("%v: got %v, want no field error", tc.err, got)
			}
			continue
		}
		if got == nil || got.Status != 400 || got.Field != tc.wantField || (tc.wantMsg != "" && got.Message != tc.wantMsg) {
			t.Fatalf("%v: got %+v, want a 400 at %s", tc.err, got, tc.wantField)
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

// TestAdminCollectionUpdateNameRuleDB: the editor sends its whole draft on
// Save, so an update checks the title only when it changes it or adds a
// library. A collection that already shares its title (made by an import)
// still saves its other fields; a rename into a taken name, or adding a
// library that has the name, is refused at title.
func TestAdminCollectionUpdateNameRuleDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	title := fmt.Sprintf("Staff Picks %d", f.account)
	create := func(name string, library int, unique bool) string {
		t.Helper()
		var id string
		if unique {
			created, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryIDs: []int{library}, Title: name, CollectionType: "manual"})
			if err != nil {
				t.Fatalf("create %q: %v", name, err)
			}
			id = created.ID
		} else {
			created, err := repo.Create(t.Context(), catalog.CreateLibraryCollectionInput{LibraryIDs: []int{library}, Slug: "staff-picks", Title: name})
			if err != nil {
				t.Fatalf("import %q: %v", name, err)
			}
			id = created.ID
		}
		t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id = $1`, id) })
		return id
	}
	create(title, f.library, true)
	imported := create(title, f.library, false)
	other := create(title+" too", f.library, true)
	create(title, f.hidden, true)

	for _, tc := range []struct {
		name      string
		id        string
		update    AdminCollectionUpdate
		wantTaken bool
	}{
		{name: "full draft of an existing duplicate", id: imported, update: AdminCollectionUpdate{Title: new(title), Description: new("Edited"), LibraryIDs: &[]int{f.library}}},
		{name: "case and spaces only", id: imported, update: AdminCollectionUpdate{Title: new("  " + strings.ToUpper(title) + " ")}},
		{name: "rename into a taken name", id: other, update: AdminCollectionUpdate{Title: new(title)}, wantTaken: true},
		{name: "add a library that has the name", id: imported, update: AdminCollectionUpdate{Title: new(title), LibraryIDs: &[]int{f.library, f.hidden}}, wantTaken: true},
	} {
		_, err := h.UpdateAdminCollection(t.Context(), tc.id, tc.update)
		apiErr, isField := errors.AsType[*APIError](err)
		taken := isField && apiErr.Field == "title"
		if taken != tc.wantTaken || (!tc.wantTaken && err != nil) {
			t.Fatalf("%s: err = %v, want title taken %v", tc.name, err, tc.wantTaken)
		}
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

// A /api/v2 smart collection needs a complete rule: one with none would match
// every title. The refusal names query_definition.groups so the editor can
// point at the rules. Updates that leave the rules alone, and /api/v1, keep
// working for collections saved before the check.
func TestSmartCollectionNeedsACompleteRule(t *testing.T) {
	const blank = `{"groups":[{"match":"all","rules":[{"field":"genre","op":"is","value":""}]}]}`
	const comedy = `{"groups":[{"match":"all","rules":[{"field":"genre","op":"is","value":"Comedy"}]}]}`
	needsRule := func(t *testing.T, err error) {
		t.Helper()
		if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 400 || e.Field != "query_definition.groups" {
			t.Fatalf("expected a query_definition.groups field error, got %v", err)
		}
	}

	h := &LibraryCollectionHandler{}
	for _, definition := range []string{"", `{"groups":[]}`, blank} {
		_, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryID: 1, Title: "Everything", CollectionType: collectionTypeSmart, QueryDefinition: json.RawMessage(definition)})
		needsRule(t, err)
	}

	smart, manual := collectionTypeSmart, collectionManagementModeManual
	for name, tc := range map[string]struct {
		existingType  string
		existingRules string
		req           AdminCollectionUpdate
		refused       bool
	}{
		"smart, rules removed":              {existingType: smart, req: AdminCollectionUpdate{QueryDefinition: json.RawMessage(`{"groups":[]}`)}, refused: true},
		"smart, only a blank rule":          {existingType: smart, req: AdminCollectionUpdate{QueryDefinition: json.RawMessage(blank)}, refused: true},
		"smart, a rule":                     {existingType: smart, req: AdminCollectionUpdate{QueryDefinition: json.RawMessage(comedy)}},
		"saved empty, renamed only":         {existingType: smart, existingRules: `{}`, req: AdminCollectionUpdate{Title: new("Renamed")}},
		"saved empty, list toggle":          {existingType: smart, existingRules: `{}`, req: AdminCollectionUpdate{CollectionType: &smart, Featured: new(true)}},
		"switched to smart, no stored rule": {existingType: manual, existingRules: `{}`, req: AdminCollectionUpdate{CollectionType: &smart}, refused: true},
		"switched to smart, stored rule":    {existingType: manual, existingRules: comedy, req: AdminCollectionUpdate{CollectionType: &smart}},
		"manual sending empty rules":        {existingType: manual, req: AdminCollectionUpdate{QueryDefinition: json.RawMessage(`{"groups":[]}`)}},
		"v1, rules removed":                 {existingType: smart, req: AdminCollectionUpdate{QueryDefinition: json.RawMessage(`{"groups":[]}`), V1Rules: true}},
	} {
		t.Run(name, func(t *testing.T) {
			err := adminSmartRuleCheck(tc.existingType, json.RawMessage(tc.existingRules), tc.req, tc.req.QueryDefinition)
			if tc.refused {
				needsRule(t, err)
			} else if err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}

	store := &lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", CollectionType: smart}}
	personal := NewCollectionHandler(lifecycleProvider{store: store})
	_, err := personal.CreatePersonalCollection(t.Context(), PersonalCollectionCreateCommand{UserID: 1, ProfileID: "owner", Request: PersonalCollectionCreateRequest{Name: "Everything", CollectionType: smart, QueryDefinition: json.RawMessage(`{"groups":[]}`)}})
	needsRule(t, err)
	_, err = personal.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{UserID: 1, ProfileID: "owner", CollectionID: "c", Request: PersonalCollectionUpdateRequest{QueryDefinition: json.RawMessage(blank)}})
	needsRule(t, err)
	if store.mutations != 0 {
		t.Fatalf("refused saves wrote %d times", store.mutations)
	}
}
