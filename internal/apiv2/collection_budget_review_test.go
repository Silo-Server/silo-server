package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type facetBudgetHTTPStore struct {
	userstore.UserStore
	collection *userstore.Collection
}

func (s facetBudgetHTTPStore) GetCollection(context.Context, string) (*userstore.Collection, error) {
	return s.collection, nil
}

type facetBudgetHTTPProvider struct {
	userstore.UserStoreProvider
	store userstore.UserStore
	err   error
}

func (p facetBudgetHTTPProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, p.err
}

func TestCatalogFacetStoredBudgetValidation(t *testing.T) {
	rules := make([]catalogsvc.QueryRule, 129)
	for i := range rules {
		rules[i] = catalogsvc.QueryRule{Field: "year", Op: "gte", Value: 2000}
	}
	raw, err := json.Marshal(catalogsvc.QueryDefinition{MediaScope: "movie", Groups: []catalogsvc.QueryGroup{{Rules: rules}}})
	if err != nil {
		t.Fatal(err)
	}
	store := facetBudgetHTTPStore{collection: &userstore.Collection{
		ID: "synthetic", CreatorProfileID: "p-owner", CollectionType: "smart", QueryDefinition: string(raw),
	}}
	for _, failure := range []bool{false, true} {
		provider := facetBudgetHTTPProvider{store: store}
		if failure {
			provider.err = errors.New("synthetic database unavailable")
		}
		browse, items := catalogsvc.NewBrowseRepository(nil), catalogsvc.NewItemRepository(nil)
		itemsH := handlers.NewItemsHandler(browse, items, nil, nil, nil, nil, nil, nil, nil)
		deps, _ := catalogDeps(t)
		deps.CatalogAccess = &allowlistCatalogAccess{}
		deps.CatalogBrowse = handlers.NewCatalogHandler(catalogsvc.NewCatalogResolver(browse, items).WithUserStoreProvider(provider), itemsH)
		h := newTestHandler(t, deps)
		for _, path := range []string{"/api/v2/catalog/filters?", "/api/v2/catalog/filters/search?facet=studio&q=synthetic&"} {
			t.Run(fmt.Sprintf("%s/storage_failure=%t", path, failure), func(t *testing.T) {
				rec := do(t, h, http.MethodGet, path+"source=user_collection&collection_id=synthetic&type=movie", "", viewerHeaders())
				if failure {
					if rec.Code != http.StatusInternalServerError {
						t.Fatalf("ordinary storage failure changed classification: %d %s", rec.Code, rec.Body.String())
					}
				} else {
					requireProblem(t, rec, TypeValidationFailed)
				}
			})
		}
	}
}

func TestAdminCollectionRawBudgetValidation(t *testing.T) {
	deps, _ := libraryDeps(t)
	// Both requests must fail before the service needs a database.
	deps.AdminCollections = &handlers.LibraryCollectionHandler{}
	h := newTestHandler(t, deps)
	ids := make([]string, 257)
	for i := range ids {
		ids[i] = "1"
	}
	raw, err := json.Marshal(map[string]any{"library_ids": ids, "title": "Synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/admin/collections", string(raw), bearer(adminToken)), TypeValidationFailed)
	queryIDs := make([]int, 256)
	for i := range queryIDs {
		queryIDs[i] = 1
	}
	query, err := json.Marshal(map[string]any{"library_ids": queryIDs})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"library_id":"1","title":"Synthetic","collection_type":"smart","query_definition":` + string(query) + `}`
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/admin/collections", body, bearer(adminToken)), TypeValidationFailed)
}

func TestCatalogComposedBudgetValidationDB(t *testing.T) {
	pool := viewerAccessTestPool(t)
	var account int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("composition-%d", time.Now().UnixNano())).Scan(&account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE id=$1`, account)
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM user_collection_revisions WHERE user_id=$1`, account)
	})
	store, err := pgstore.NewPostgresProvider(pool).ForUser(t.Context(), account)
	if err != nil {
		t.Fatal(err)
	}
	rules := make([]catalogsvc.QueryRule, 128)
	for i := range rules {
		rules[i] = catalogsvc.QueryRule{Field: "year", Op: "gte", Value: 999999}
	}
	raw, err := json.Marshal(catalogsvc.QueryDefinition{MediaScope: "movie", Groups: []catalogsvc.QueryGroup{{Rules: rules}}})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "p-owner", Name: "Synthetic", CollectionType: "smart", QueryDefinition: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	provider := collectionGuardHTTPProvider{stores: map[int]userstore.UserStore{1: store}}
	browse, items := catalogsvc.NewBrowseRepository(pool), catalogsvc.NewItemRepository(pool)
	itemsH := handlers.NewItemsHandler(browse, items, nil, nil, nil, nil, nil, nil, nil)
	deps, _ := catalogDeps(t)
	deps.CatalogAccess = &allowlistCatalogAccess{}
	deps.CatalogBrowse = handlers.NewCatalogHandler(catalogsvc.NewCatalogResolver(browse, items).WithUserStoreProvider(provider), itemsH)
	h := newTestHandler(t, deps)
	body := `{"source":"user_collection","collection_id":"` + collection.ID + `","type":"movie","groups":[{"match":"all","rules":[{"field":"year","op":"gte","value":2000}]}]}`
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/query", body, viewerHeaders()), TypeValidationFailed)
	// With no overlay the same admitted source is usable and has a real zero result.
	control := do(t, h, http.MethodGet, "/api/v2/catalog?source=user_collection&collection_id="+collection.ID+"&type=movie", "", viewerHeaders())
	if control.Code != http.StatusOK {
		t.Fatalf("boundary control: %d %s", control.Code, control.Body.String())
	}
}
