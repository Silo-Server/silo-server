package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func apiKeyPrivacyRepository(t *testing.T) *auth.APIKeyRepository {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{fmt.Sprintf("apikey_privacy_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(t.Context(), `CREATE TABLE users(id integer PRIMARY KEY,username text NOT NULL);INSERT INTO users VALUES(7,'Owner');
 CREATE TABLE api_keys(id bigserial PRIMARY KEY,user_id integer NOT NULL REFERENCES users(id),label text NOT NULL,api_key text NOT NULL UNIQUE,rate_tier text NOT NULL DEFAULT 'standard',scopes text[] NOT NULL DEFAULT '{}',created_at timestamptz NOT NULL DEFAULT now(),last_used_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../../migrations/sql/20260905214731_add_api_key_configuration_revisions.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err = pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	migration, err = os.ReadFile("../../../migrations/sql/20261019181724_hash_api_key_verifiers.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ = strings.Cut(string(migration), "-- +goose Down")
	if _, err = pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	return auth.NewAPIKeyRepository(pool)
}

func assertAPIKeyMetadataJSON(t *testing.T, raw []byte, secret string) {
	t.Helper()
	if strings.Contains(string(raw), secret) {
		t.Fatal("complete reusable credential exposed")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	var inspect func(any)
	inspect = func(v any) {
		switch node := v.(type) {
		case []any:
			for _, child := range node {
				inspect(child)
			}
		case map[string]any:
			for name, child := range node {
				if name == "key" || name == "api_key" {
					t.Fatalf("secret field %q present", name)
				}
				inspect(child)
			}
		}
	}
	inspect(value)
	if !strings.Contains(string(raw), `"key_prefix"`) {
		t.Fatal("missing safe identifier prefix")
	}
}

// TestAPIKeyEditorNeverReturnsStoredSecret: every read and edit the key
// services answer, including a stale edit's current state, carries the safe
// prefix and never the bearer, and editing leaves the key usable.
func TestAPIKeyEditorNeverReturnsStoredSecret(t *testing.T) {
	repo := apiKeyPrivacyRepository(t)
	key, err := repo.Create(t.Context(), 7, "Automation", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewAPIKeyHandler(repo)
	requireSafe := func(name string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) { assertAPIKeyMetadataJSON(t, raw, key.Key) })
	}

	personal, _, err := handler.ListPersonalAPIKeysPage(t.Context(), 7, nil, 10)
	if err != nil || len(personal) != 1 {
		t.Fatalf("personal page = %d: %v", len(personal), err)
	}
	requireSafe("personal list", personal)
	metadata, err := handler.GetAdminAPIKey(t.Context(), key.ID)
	if err != nil {
		t.Fatal(err)
	}
	requireSafe("admin editor", metadata)
	if raw, _ := json.Marshal(metadata); strings.Contains(string(raw), "last_used_at") || strings.Contains(string(raw), "username") {
		t.Fatal("volatile usage/display field in canonical editor")
	}
	rows, more, err := handler.ListAdminAPIKeysPage(t.Context(), nil, 1)
	if err != nil || more || len(rows) != 1 {
		t.Fatalf("page=%d more=%v %v", len(rows), more, err)
	}
	requireSafe("admin list", rows)
	updated, err := handler.UpdateAdminAPIKeyTier(t.Context(), key.ID, "elevated", auth.APIKeyPrecondition{Revision: metadata.Revision})
	if err != nil {
		t.Fatal(err)
	}
	requireSafe("tier update", updated)
	_, err = handler.UpdateAdminAPIKeyTier(t.Context(), key.ID, "standard", auth.APIKeyPrecondition{Revision: metadata.Revision})
	conflict, ok := errors.AsType[*auth.APIKeyRevisionConflict](err)
	if !ok || conflict.Current.Revision != updated.Revision {
		t.Fatalf("stale editor did not return current metadata: %v", err)
	}
	requireSafe("stale edit", apiKeyConfigurationOf(conflict.Current))
	valid, err := repo.GetByKey(t.Context(), key.Key)
	if err != nil || valid.ID != key.ID || valid.Key != "" || valid.RateTier != "elevated" {
		t.Fatal("existing key authentication changed", err)
	}
}
