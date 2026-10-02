package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func apiKeyHTTPRepository(t *testing.T) *auth.APIKeyRepository {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("api_key_http_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(t.Context(), `CREATE SCHEMA `+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+quoted+` CASCADE`) })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(t.Context(), `CREATE TABLE users(id integer PRIMARY KEY,username text); INSERT INTO users VALUES(1,'one'),(2,'two');
 CREATE TABLE api_keys(id bigserial PRIMARY KEY,user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,label text NOT NULL,api_key text NOT NULL UNIQUE,rate_tier text NOT NULL DEFAULT 'standard' CHECK(rate_tier IN ('standard','elevated')),scopes text[],created_at timestamptz NOT NULL DEFAULT now(),last_used_at timestamptz);
 INSERT INTO api_keys(user_id,label,api_key) VALUES(1,'Existing','sa_existing_credential_kept_unchanged')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260905214731_add_api_key_configuration_revisions.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err = pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	return auth.NewAPIKeyRepository(pool)
}

func TestAdminAPIKeyHTTPWithPostgresLegacyWrites(t *testing.T) {
	repo := apiKeyHTTPRepository(t)
	deps := requestDeps(fixtureRequests())
	deps.AdminAPIKeys = handlers.NewAPIKeyHandler(repo)
	h := NewHandler(deps)
	created := do(t, h, http.MethodPost, Prefix+"/admin/api-keys", `{"label":"New"}`, actingRequestAdmin)
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body.String())
	}
	var key struct {
		ID  ID     `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	if len(key.Key) != 67 || key.ID == "" {
		t.Fatal("creation response lost usable credential or typed identity")
	}
	lookup, err := repo.GetByKey(t.Context(), key.Key)
	if err != nil {
		t.Fatal(err)
	}
	path := created.Header().Get("Location")
	if path != Prefix+"/admin/api-keys/"+string(key.ID) {
		t.Fatal("invalid canonical Location", path)
	}
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if read.Code != 200 || read.Header().Get("ETag") == "" {
		t.Fatal(read.Code, read.Body.String())
	}
	assertSafe := func(raw string) {
		t.Helper()
		if strings.Contains(raw, key.Key) || strings.Contains(raw, `"key":`) || strings.Contains(raw, `"api_key":`) {
			t.Fatal("credential exposed in metadata response")
		}
	}
	assertSafe(read.Body.String())
	tag := read.Header().Get("ETag")
	if err = repo.UpdateLastUsed(t.Context(), lookup.ID); err != nil {
		t.Fatal(err)
	}
	cached := do(t, h, http.MethodGet, path, "", with(actingRequestAdmin, "If-None-Match", tag))
	if cached.Code != 304 {
		t.Fatal("usage invalidated canonical editor", cached.Code, cached.Body.String())
	}
	if err = repo.UpdateTier(t.Context(), lookup.ID, "elevated"); err != nil {
		t.Fatal(err)
	}
	stale := do(t, h, http.MethodPut, path+"/tier", `{"rate_tier":"standard"}`, with(actingRequestAdmin, "If-Match", tag))
	if stale.Code != 412 || stale.Header().Get("ETag") == "" || stale.Header().Get("ETag") == tag {
		t.Fatal("legacy write did not fence HTTP editor", stale.Code, stale.Body.String())
	}
	assertSafe(stale.Body.String())
	currentTag := stale.Header().Get("ETag")
	noop := do(t, h, http.MethodPut, path+"/tier", `{"rate_tier":"elevated"}`, with(actingRequestAdmin, "If-Match", currentTag))
	if noop.Code != 200 || noop.Header().Get("ETag") != currentTag {
		t.Fatal("no-op changed tag", noop.Code, noop.Body.String())
	}
	assertSafe(noop.Body.String())
	page := do(t, h, http.MethodGet, Prefix+"/admin/api-keys?limit=1", "", actingRequestAdmin)
	if page.Code != 200 {
		t.Fatal(page.Code, page.Body.String())
	}
	assertSafe(page.Body.String())
	removed := do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", currentTag))
	if removed.Code != 204 || removed.Body.Len() != 0 || removed.Header().Get("ETag") != "" {
		t.Fatal("delete response", removed.Code, removed.Body.String())
	}
	if _, err = repo.GetByKey(t.Context(), key.Key); err == nil {
		t.Fatal("deleted key still authenticates")
	}
}
