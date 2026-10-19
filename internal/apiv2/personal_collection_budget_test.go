package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestCollectionCountUnavailableRecoveryDB uses the real HTTP adapter, service,
// and store with a legacy definition seeded before admission limits existed.
// Count failure must never hide the editor needed to repair or delete it.
func TestCollectionCountUnavailableRecoveryDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires a disposable migrated PostgreSQL database")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var account int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("count-budget-%d", time.Now().UnixNano())).Scan(&account); err != nil {
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
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "p-owner", Name: "Synthetic owner"}); err != nil {
		t.Fatal(err)
	}
	definition := func(n int) json.RawMessage {
		rules := make([]catalogsvc.QueryRule, n)
		for i := range rules {
			rules[i] = catalogsvc.QueryRule{Field: "year", Op: "gte", Value: 2000 + i}
		}
		raw, err := json.Marshal(catalogsvc.QueryDefinition{MediaScope: "movie", Groups: []catalogsvc.QueryGroup{{Rules: rules}}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	legacy, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "p-owner", Name: "Synthetic legacy", CollectionType: "smart", QueryDefinition: string(definition(256))})
	if err != nil {
		t.Fatal(err)
	}
	provider := collectionGuardHTTPProvider{stores: map[int]userstore.UserStore{1: store}}
	svc := handlers.NewCollectionHandler(provider)
	svc.Executor = &catalogsvc.QueryExecutor{Pool: pool}
	deps := pilotDeps(nil, nil)
	deps.PersonalCollections = svc
	h := newTestHandler(t, deps)
	path := "/api/v2/collections/" + legacy.ID
	assertStatus := func(raw []byte, want string) {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if got := body["item_count_status"]; got != want {
			t.Fatalf("item_count_status=%v, want %s; body=%s", got, want, raw)
		}
	}
	// The list and editor must both keep the legacy collection accessible.
	listed := do(t, h, http.MethodGet, "/api/v2/collections", "", viewerHeaders())
	if listed.Code != 200 {
		t.Fatalf("list: %d %s", listed.Code, listed.Body.String())
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("legacy collection missing: %s (%v)", listed.Body.String(), err)
	}
	assertStatus(list.Items[0], "unavailable")
	get := guardHTTPGet(t, h, path, viewerHeaders())
	assertStatus(get.Body.Bytes(), "unavailable")
	updated := do(t, h, http.MethodPatch, path, `{"name":"Synthetic renamed"}`, with(viewerHeaders(), "If-Match", get.Header().Get("ETag")))
	if updated.Code != 200 {
		t.Fatalf("metadata recovery: %d %s", updated.Code, updated.Body.String())
	}
	assertStatus(updated.Body.Bytes(), "unavailable")
	get = guardHTTPGet(t, h, path, viewerHeaders())
	repaired := do(t, h, http.MethodPatch, path, `{"query_definition":`+string(definition(128))+`}`, with(viewerHeaders(), "If-Match", get.Header().Get("ETag")))
	if repaired.Code != 200 {
		t.Fatalf("definition recovery: %d %s", repaired.Code, repaired.Body.String())
	}
	assertStatus(repaired.Body.Bytes(), "available")
	get = guardHTTPGet(t, h, path, viewerHeaders())
	deleted := do(t, h, http.MethodDelete, path, "", with(viewerHeaders(), "If-Match", get.Header().Get("ETag")))
	if deleted.Code != 204 {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	// New definitions cannot recreate the legacy over-budget state.
	refused := do(t, h, http.MethodPost, "/api/v2/collections", `{"name":"Synthetic over budget","collection_type":"smart","query_definition":`+string(definition(256))+`}`, viewerHeaders())
	if refused.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over-budget create: %d %s", refused.Code, refused.Body.String())
	}
	t.Log("legacy list/editor retain collection with unavailable count; metadata edit, definition repair, delete and over-budget creation refusal verified")
}
