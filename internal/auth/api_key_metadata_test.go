package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func apiKeyMetadataRepository(t *testing.T) *APIKeyRepository {
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
	schema := fmt.Sprintf("api_key_metadata_%d", time.Now().UnixNano())
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
	return NewAPIKeyRepository(pool)
}

func TestAPIKeyMetadataNeverReturnsCredential(t *testing.T) {
	repo := apiKeyMetadataRepository(t)
	ctx := t.Context()
	original, err := repo.GetByKey(ctx, "sa_existing_credential_kept_unchanged")
	if err != nil || original.Key != "sa_existing_credential_kept_unchanged" {
		t.Fatalf("migration changed authentication: %v", err)
	}
	created, err := repo.Create(ctx, 2, "Created", []string{ScopeAdminUsers})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := repo.GetMetadataByID(ctx, created.ID)
	if err != nil || metadata.KeyPrefix != created.Key[:11] {
		t.Fatalf("generated key prefix: %v", err)
	}
	authenticated, err := repo.GetByKey(ctx, created.Key)
	if err != nil || authenticated.ID != created.ID || authenticated.UserID != 2 {
		t.Fatalf("created key cannot authenticate: %v", err)
	}
	for _, read := range []func() (any, error){
		func() (any, error) { return repo.ListByUser(ctx, 2) }, func() (any, error) { return repo.ListByUserAdmin(ctx, 2) }, func() (any, error) { return repo.ListAll(ctx) }, func() (any, error) { return repo.GetMetadataByID(ctx, created.ID) },
	} {
		result, err := read()
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), created.Key) || strings.Contains(string(data), original.Key) || strings.Contains(string(data), `"Key":`) {
			t.Fatal("metadata read exposed full credential")
		}
	}
	if _, err = repo.pool.Exec(ctx, `INSERT INTO api_keys(user_id,label,api_key) VALUES(1,'Short','sa_x'),(1,'Near short','sa_1234567890')`); err != nil {
		t.Fatal(err)
	}
	list, err := repo.ListByUser(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if (item.Label == "Short" || item.Label == "Near short") && item.KeyPrefix != "" {
			t.Fatal("short legacy credential was exposed as its own prefix")
		}
	}
	if err = repo.Delete(ctx, created.ID, 1); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("foreign owner deletion: %v", err)
	}
	if err = repo.Delete(ctx, created.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.GetByKey(ctx, created.Key); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatal("deleted key still authenticates")
	}
}

func TestAPIKeyConfigurationRevisionAndLegacyWriters(t *testing.T) {
	repo := apiKeyMetadataRepository(t)
	ctx := t.Context()
	current, err := repo.GetMetadataByID(ctx, 1)
	if err != nil || current.Revision < 1 {
		t.Fatalf("initial revision: %v", err)
	}
	before, _ := json.Marshal(current)
	if err = repo.UpdateLastUsed(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE users SET username='Renamed' WHERE id=1; UPDATE api_keys SET revision=99999,scopes='{}' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	unchanged, err := repo.GetMetadataByID(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(unchanged)
	if string(before) != string(after) {
		t.Fatal("usage, username, or equivalent scopes changed canonical configuration")
	}
	if _, err = repo.UpdateTierConditional(ctx, 1, "standard", APIKeyPrecondition{Revision: current.Revision}); err != nil {
		t.Fatal(err)
	}
	if err = repo.UpdateTier(ctx, 1, "elevated"); err != nil {
		t.Fatal(err)
	}
	_, err = repo.UpdateTierConditional(ctx, 1, "standard", APIKeyPrecondition{Revision: current.Revision})
	conflict, ok := errors.AsType[*APIKeyRevisionConflict](err)
	if !ok || conflict.Current.Revision == current.Revision || conflict.Current.RateTier != "elevated" {
		t.Fatalf("legacy update did not fence editor: %v", err)
	}
	if err = repo.DeleteByAdminConditional(ctx, 1, APIKeyPrecondition{Revision: current.Revision}); !errors.Is(err, ErrAPIKeyRevisionConflict) {
		t.Fatalf("stale deletion: %v", err)
	}
	for _, statement := range []string{`UPDATE api_keys SET label='Changed' WHERE id=1`, `UPDATE api_keys SET scopes=ARRAY['admin:users'] WHERE id=1`, `UPDATE api_keys SET created_at=created_at+interval '1 second' WHERE id=1`} {
		old, err := repo.GetMetadataByID(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
		next, err := repo.GetMetadataByID(ctx, 1)
		if err != nil || next.Revision <= old.Revision {
			t.Fatalf("metadata write did not advance revision: %v", err)
		}
	}
	for _, statement := range []string{`UPDATE api_keys SET id=99 WHERE id=1`, `UPDATE api_keys SET user_id=2 WHERE id=1`, `UPDATE api_keys SET api_key='replacement' WHERE id=1`} {
		if _, err = repo.pool.Exec(ctx, statement); err == nil {
			t.Fatal("credential identity changed")
		}
	}
	latest, err := repo.GetMetadataByID(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteByAdminConditional(ctx, 1, APIKeyPrecondition{Revision: latest.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `INSERT INTO api_keys(id,user_id,label,api_key) VALUES(1,1,'Recreated','sa_recreated_credential_with_new_generation')`); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpdateTierConditional(ctx, 1, "elevated", APIKeyPrecondition{Revision: latest.Revision}); !errors.Is(err, ErrAPIKeyRevisionConflict) {
		t.Fatal("old tag survived deletion and recreation")
	}
}

func TestAPIKeyConfigurationConcurrentGuards(t *testing.T) {
	repo := apiKeyMetadataRepository(t)
	ctx := t.Context()
	current, err := repo.GetMetadataByID(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 12 {
		wg.Go(func() {
			<-start
			_, err := repo.UpdateTierConditional(ctx, 1, "elevated", APIKeyPrecondition{Revision: current.Revision})
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrAPIKeyRevisionConflict) {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("exact successes=%d", won.Load())
	}
	won.Store(0)
	for i := range 12 {
		wg.Go(func() {
			tier := "standard"
			if i%2 == 1 {
				tier = "elevated"
			}
			if _, err := repo.UpdateTierConditional(ctx, 1, tier, APIKeyPrecondition{Any: true}); err != nil {
				t.Error(err)
			} else {
				won.Add(1)
			}
		})
	}
	wg.Wait()
	if won.Load() != 12 {
		t.Fatalf("wildcard successes=%d", won.Load())
	}
	for _, guard := range []APIKeyPrecondition{{}, {Revision: -1}, {Any: true, Revision: 1}, {Any: true, Revision: -1}} {
		if _, err = repo.UpdateTierConditional(ctx, 1, "standard", guard); !errors.Is(err, ErrAPIKeyPreconditionInvalid) {
			t.Fatalf("invalid update guard: %v", err)
		}
		if err = repo.DeleteByAdminConditional(ctx, 1, guard); !errors.Is(err, ErrAPIKeyPreconditionInvalid) {
			t.Fatalf("invalid delete guard: %v", err)
		}
	}
	if _, err = repo.UpdateTierConditional(ctx, 1, "unlimited", APIKeyPrecondition{Any: true}); !errors.Is(err, ErrInvalidAPIKeyTier) {
		t.Fatalf("invalid tier: %v", err)
	}
	if err = repo.DeleteByAdminConditional(ctx, 1, APIKeyPrecondition{Any: true}); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteByAdminConditional(ctx, 1, APIKeyPrecondition{Any: true}); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("missing wildcard: %v", err)
	}
}

func TestAPIKeyMetadataPagingIsBoundedAndStable(t *testing.T) {
	repo := apiKeyMetadataRepository(t)
	ctx := t.Context()
	if _, err := repo.pool.Exec(ctx, `INSERT INTO api_keys(user_id,label,api_key,created_at) SELECT 1,'Paged','sa_paged_credential_'||i,'2026-01-01'::timestamptz FROM generate_series(1,205) i`); err != nil {
		t.Fatal(err)
	}
	first, more, err := repo.ListAllPage(ctx, nil, 10000)
	if err != nil || !more || len(first) != 200 {
		t.Fatalf("bounded page %d %v %v", len(first), more, err)
	}
	last := first[len(first)-1]
	cursor := &APIKeyPageKey{CreatedAt: last.CreatedAt, ID: last.ID}
	if err = repo.DeleteByAdmin(ctx, first[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Create(ctx, 2, "New head", nil); err != nil {
		t.Fatal(err)
	}
	second, more, err := repo.ListAllPage(ctx, cursor, 200)
	if err != nil || more || len(second) != 6 {
		t.Fatalf("next page %d %v %v", len(second), more, err)
	}
	seen := map[int64]bool{}
	for _, row := range first {
		seen[row.ID] = true
	}
	for _, row := range second {
		if seen[row.ID] {
			t.Fatal("duplicate page entry")
		}
	}
}

func TestAdminUserAPIKeyPageIsolation(t *testing.T) {
	repo := apiKeyMetadataRepository(t)
	ctx := t.Context()
	first, err := repo.Create(ctx, 2, "first", []string{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.Create(ctx, 2, "second", []string{})
	if err != nil {
		t.Fatal(err)
	}
	rows, more, err := repo.ListByUserAdminPage(ctx, 2, nil, 1)
	if err != nil || len(rows) != 1 || !more || rows[0].ID != second.ID {
		t.Fatalf("%+v %v %v", rows, more, err)
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), second.Key) {
		t.Fatal("metadata disclosed credential")
	}
	rows, more, err = repo.ListByUserAdminPage(ctx, 2, &APIKeyPageKey{ID: rows[0].ID, CreatedAt: rows[0].CreatedAt}, 1)
	if err != nil || len(rows) != 1 || more || rows[0].ID != first.ID || rows[0].UserID != 2 {
		t.Fatalf("%+v %v %v", rows, more, err)
	}
}
