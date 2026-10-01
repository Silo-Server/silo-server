package subtitles

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func providerConfigRevisionDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_PROVIDER_CONFIG_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_PROVIDER_CONFIG_TEST_DATABASE_URL must name an isolated test database")
	}
	control, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "provider_config_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = control.Exec(t.Context(), "CREATE SCHEMA "+identifier); err != nil {
		control.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := control.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
		control.Close()
	})
	_, err = pool.Exec(t.Context(), `CREATE TABLE subtitle_provider_config (
 provider_name TEXT PRIMARY KEY, enabled BOOLEAN NOT NULL DEFAULT false,
 api_key TEXT NOT NULL DEFAULT '',username TEXT NOT NULL DEFAULT '',password TEXT NOT NULL DEFAULT '',extra_config JSONB,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
func TestProviderConfigRevisionRequiresEncryption(t *testing.T) {
	repo := NewPgRepository(nil, nil)
	if _, err := repo.GetProviderConfigWithRevision(t.Context(), "subdl"); !errors.Is(err, ErrProviderConfigEncryptionUnavailable) {
		t.Fatal(err)
	}
	if _, err := repo.SaveProviderConfigWithRevision(t.Context(), "subdl", ProviderConfigChange{APIKey: "fixture"}, new(int64(0))); !errors.Is(err, ErrProviderConfigEncryptionUnavailable) {
		t.Fatal(err)
	}
}
func TestProviderConfigRevisionDB(t *testing.T) {
	pool := providerConfigRevisionDatabase(t)
	ctx := t.Context()
	cipher, err := secret.New([]byte(strings.Repeat("f", secret.MinMasterKeyLen)))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPgRepository(pool, cipher)
	if err = repo.UpsertProviderConfig(ctx, &ProviderConfig{ProviderName: "subdl", Enabled: true, APIKey: "fixture-api", Username: "fixture-user", Password: "fixture-password"}); err != nil {
		t.Fatal(err)
	}
	snapshot := func(name string, withoutRevision bool) string {
		t.Helper()
		var value string
		query := `SELECT to_jsonb(c)::text FROM subtitle_provider_config c WHERE provider_name=$1`
		if withoutRevision {
			query = `SELECT (to_jsonb(c)-'revision')::text FROM subtitle_provider_config c WHERE provider_name=$1`
		}
		if err := pool.QueryRow(ctx, query, name).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if _, err = pool.Exec(ctx, `UPDATE subtitle_provider_config SET extra_config='{"retained":true}'::jsonb WHERE provider_name='subdl'`); err != nil {
		t.Fatal(err)
	}
	original := snapshot("subdl", false)
	migration, err := os.ReadFile("../../migrations/sql/20260906152948_subtitle_provider_config_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(migration), "-- +goose Down")
	if !ok {
		t.Fatal("missing down migration")
	}
	if _, err = pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	if snapshot("subdl", true) != original {
		t.Fatal("migration changed existing config or ciphertext")
	}
	current, err := repo.GetProviderConfigWithRevision(ctx, "subdl")
	if err != nil || current.Revision <= 0 || current.Config.APIKey != "fixture-api" || current.Config.Password != "fixture-password" || !current.Config.HasAPIKey || !current.Config.HasCredentials {
		t.Fatal("canonical encrypted read failed", err)
	}
	var storedAPI string
	if err = pool.QueryRow(ctx, `SELECT api_key FROM subtitle_provider_config WHERE provider_name='subdl'`).Scan(&storedAPI); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(storedAPI, "enc:v1:") {
		t.Fatal("secret not encrypted")
	}
	if _, err = cipher.DecryptIfEncrypted(storedAPI, repo.providerSecretAAD("api_key", "other-provider")); err == nil {
		t.Fatal("ciphertext lost provider-name AAD binding")
	}
	oldRevision := current.Revision
	if err = repo.UpsertProviderConfig(ctx, &ProviderConfig{ProviderName: "subdl", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	current, err = repo.GetProviderConfigWithRevision(ctx, "subdl")
	if err != nil || current.Revision <= oldRevision || current.Config.APIKey != "fixture-api" {
		t.Fatal("legacy upsert failed to invalidate", err)
	}
	before := snapshot("subdl", false)
	_, err = repo.SaveProviderConfigWithRevision(ctx, "subdl", ProviderConfigChange{Enabled: true, APIKey: "stale-secret"}, new(oldRevision))
	conflict, ok := errors.AsType[*ProviderConfigRevisionConflict](err)
	if !ok || conflict.CurrentRevision != current.Revision || snapshot("subdl", false) != before {
		t.Fatal("stale write changed full persisted row", err)
	}
	revision, err := repo.SaveProviderConfigWithRevision(ctx, "subdl", ProviderConfigChange{Enabled: true}, new(current.Revision))
	if err != nil || revision <= current.Revision {
		t.Fatal(err)
	}
	current, err = repo.GetProviderConfigWithRevision(ctx, "subdl")
	if err != nil || current.Config.APIKey != "fixture-api" || current.Config.Password != "fixture-password" || !current.Config.Enabled {
		t.Fatal("blank did not preserve credentials", err)
	}
	if err = repo.ClearProviderCredentials(ctx, "subdl"); err != nil {
		t.Fatal(err)
	}
	current, err = repo.GetProviderConfigWithRevision(ctx, "subdl")
	if err != nil || current.Revision <= revision || current.Config.Enabled || current.Config.HasAPIKey || current.Config.HasCredentials {
		t.Fatal("legacy clear failed to invalidate", err)
	}
	revision, err = repo.SaveProviderConfigWithRevision(ctx, "subdl", ProviderConfigChange{Enabled: true, APIKey: "replacement", Username: "user", Password: "password"}, new(current.Revision))
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := repo.SaveProviderConfigWithRevision(ctx, "subdl", ProviderConfigChange{Enabled: true, APIKey: "must-ignore", ClearCredentials: true}, new(revision))
	if err != nil || cleared <= revision {
		t.Fatal(err)
	}
	current, err = repo.GetProviderConfigWithRevision(ctx, "subdl")
	if err != nil || current.Config.Enabled || current.Config.APIKey != "" || current.Config.Username != "" || current.Config.Password != "" {
		t.Fatal("explicit clear not atomic", err)
	}
	// Observe a guarded provider UPDATE waiting behind a legacy UPDATE before its CAS.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `UPDATE subtitle_provider_config SET username='changed' WHERE provider_name='subdl'`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := repo.SaveProviderConfigWithRevision(ctx, "subdl", ProviderConfigChange{Enabled: true}, new(cleared))
		done <- err
	}()
	waitForProviderConfigWriteLock(t, pool)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok = errors.AsType[*ProviderConfigRevisionConflict](<-done); !ok {
		t.Fatal("waiting CAS accepted a changed row")
	}
	current, err = repo.GetProviderConfigWithRevision(ctx, "subdl")
	if err != nil || current.Config.Enabled || current.Config.Username != "changed" {
		t.Fatal("guarded race changed credentials", err)
	}
	oldRevision = current.Revision
	if _, err = pool.Exec(ctx, `DELETE FROM subtitle_provider_config WHERE provider_name='subdl'`); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.SaveProviderConfigWithRevision(ctx, "subdl", ProviderConfigChange{}, nil); err == nil {
		t.Fatal("existence update created missing provider")
	}
	revision, err = repo.SaveProviderConfigWithRevision(ctx, "subdl", ProviderConfigChange{}, new(int64(0)))
	if err != nil || revision <= oldRevision {
		t.Fatal("recreated row reused old revision", err)
	}
	before = snapshot("subdl", false)
	if _, err = repo.SaveProviderConfigWithRevision(ctx, "subdl", ProviderConfigChange{Enabled: true}, new(oldRevision)); err == nil || snapshot("subdl", false) != before {
		t.Fatal("old validator changed recreated row", err)
	}
	// Two initial editors race to create the same absent provider; exactly one wins.
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			_, err := repo.SaveProviderConfigWithRevision(ctx, "subsource", ProviderConfigChange{APIKey: "new-fixture"}, new(int64(0)))
			results <- err
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if _, ok := errors.AsType[*ProviderConfigRevisionConflict](err); !ok {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("create-if-absent did not choose exactly one winner")
	}
	if _, err = pool.Exec(ctx, `UPDATE subtitle_provider_config SET revision=1 WHERE provider_name='subdl'`); err != nil {
		t.Fatal(err)
	}
	current, err = repo.GetProviderConfigWithRevision(ctx, "subdl")
	if err != nil || current.Revision <= revision {
		t.Fatal("direct writer supplied stale revision", err)
	}
	before = snapshot("subdl", false)
	if _, err = pool.Exec(ctx, `SELECT setval('subtitle_provider_config_revision_seq',9223372036854775807,true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.SaveProviderConfigWithRevision(ctx, "subdl", ProviderConfigChange{Enabled: true}, nil); err == nil || snapshot("subdl", false) != before {
		t.Fatal("revision exhaustion changed persisted state")
	}
	// Down removes only the guard, preserving configuration and legacy writers.
	before = snapshot("subdl", true)
	if _, err = pool.Exec(ctx, down); err != nil {
		t.Fatal(err)
	}
	if snapshot("subdl", false) != before {
		t.Fatal("down migration changed provider data")
	}
	if err = repo.UpsertProviderConfig(ctx, &ProviderConfig{ProviderName: "subdl", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	t.Log("migration preserves existing ciphertext; legacy and direct writers invalidate; guarded stale snapshots, locked-update CAS, create race, clear/preserve, AAD, delete/recreate and down compatibility PASS")
}
func waitForProviderConfigWriteLock(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'UPDATE subtitle_provider_config SET enabled%')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		runtime.Gosched()
	}
}
