package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCatalogCompositionBudgetIsValidation(t *testing.T) {
	err := catalogResolveError(t.Context(), fmt.Errorf("composed query: %w", catalog.ErrQueryComplexity), false)
	if got, ok := errors.AsType[*APIError](err); !ok || got.Status != http.StatusBadRequest {
		t.Fatalf("composed budget must be a validation error: %v", err)
	}
	err = catalogResolveError(t.Context(), errors.New("ordinary database failure"), false)
	if got, ok := errors.AsType[*APIError](err); !ok || got.Status != http.StatusInternalServerError {
		t.Fatalf("ordinary failure changed classification: %v", err)
	}
}

func TestAdminCollectionRawCompositionAdmission(t *testing.T) {
	ids := make([]int, 256)
	for i := range ids {
		ids[i] = 1 // Raw duplicate IDs still consume construction budget.
	}
	raw, err := json.Marshal(catalog.QueryDefinition{LibraryIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	h := &LibraryCollectionHandler{}
	_, err = h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryID: 1, Title: "Synthetic", CollectionType: "smart", QueryDefinition: raw})
	if got, ok := errors.AsType[*APIError](err); !ok || got.Status != http.StatusBadRequest || got.Field != "query_definition" {
		t.Fatalf("raw composed budget must be refused before normalization/database access: %v", err)
	}
}

func TestLegacyLiveCollectionRawCompositionAdmission(t *testing.T) {
	ids := make([]int, 256)
	for i := range ids {
		ids[i] = 1
	}
	raw, err := json.Marshal(catalog.QueryDefinition{LibraryIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	h := &LibraryCollectionHandler{Executor: &catalog.QueryExecutor{}}
	_, _, err = h.loadLiveCollectionItems(t.Context(), &models.LibraryCollection{LibraryID: 1, QueryDefinition: raw}, catalog.AccessFilter{}, CollectionItemPage{})
	queryErr, ok := errors.AsType[smartCollectionQueryError](err)
	if !ok || !errors.Is(queryErr.err, catalog.ErrQueryComplexity) {
		t.Fatalf("legacy binding and raw query escaped admission: %v", err)
	}
}

func TestLibraryCollectionComposedUpdateRecoveryDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	definition := func(n int) json.RawMessage {
		t.Helper()
		ids := make([]int, n)
		for i := range ids {
			ids[i] = f.library
		}
		raw, err := json.Marshal(catalog.QueryDefinition{LibraryIDs: ids})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	created, err := repo.Create(t.Context(), catalog.CreateLibraryCollectionInput{LibraryID: f.library, Title: "Synthetic composition", CollectionType: "smart", QueryDefinition: definition(255)})
	if err != nil {
		t.Fatalf("exact raw boundary: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM library_collections WHERE id=$1`, created.ID)
	})
	if err := repo.Update(t.Context(), catalog.UpdateLibraryCollectionInput{ID: created.ID, QueryDefinition: definition(256)}); !errors.Is(err, catalog.ErrQueryComplexity) {
		t.Fatalf("repository update did not combine stored binding: %v", err)
	}
	_, err = h.UpdateAdminCollection(t.Context(), created.ID, AdminCollectionUpdate{QueryDefinition: definition(256)})
	if got, ok := errors.AsType[*APIError](err); !ok || got.Status != http.StatusBadRequest || got.Field != "query_definition" {
		t.Fatalf("service normalized away raw composition or returned 500: %v", err)
	}
	// Seed legacy state directly: metadata edits and definition repair must stay usable.
	f.exec(t, `UPDATE library_collections SET query_definition=$2 WHERE id=$1`, created.ID, definition(256))
	if err := repo.Update(t.Context(), catalog.UpdateLibraryCollectionInput{ID: created.ID, Title: new("Synthetic renamed")}); err != nil {
		t.Fatalf("legacy metadata edit: %v", err)
	}
	if err := repo.Update(t.Context(), catalog.UpdateLibraryCollectionInput{ID: created.ID, QueryDefinition: definition(255)}); err != nil {
		t.Fatalf("legacy query repair: %v", err)
	}
	tooManyBindings := make([]int, 256)
	for i := range tooManyBindings {
		tooManyBindings[i] = f.library
	}
	if err := repo.Update(t.Context(), catalog.UpdateLibraryCollectionInput{ID: created.ID, LibraryIDs: &tooManyBindings}); !errors.Is(err, catalog.ErrQueryComplexity) {
		t.Fatalf("binding update did not combine stored query before deduplication: %v", err)
	}
	if err := repo.Update(t.Context(), catalog.UpdateLibraryCollectionInput{ID: created.ID, QueryDefinition: definition(1)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(t.Context(), catalog.UpdateLibraryCollectionInput{ID: created.ID, LibraryIDs: new([]int{f.library})}); err != nil {
		t.Fatalf("small composition: %v", err)
	}
}
