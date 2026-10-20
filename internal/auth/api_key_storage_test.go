package auth

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Exercise the real repository against a task-owned schema. Do not print the
// generated bearer, including on failure.
func TestAPIKeyStorageDoesNotRetainBearer(t *testing.T) {
	repo := apiKeyMetadataRepository(t)
	ctx := t.Context()
	created, err := repo.Create(ctx, 2, "Synthetic storage regression", []string{ScopeAdminUsers})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Key, "sa_") || len(created.Key) != 67 {
		t.Fatal("creation did not disclose the expected bearer format")
	}
	var storedBearer bool
	if err := repo.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM api_keys ak, jsonb_each_text(to_jsonb(ak)) field WHERE ak.id=$1 AND field.value=$2)`, created.ID, created.Key).Scan(&storedBearer); err != nil {
		t.Fatal(err)
	}
	if storedBearer {
		t.Fatal("database row retains the reusable bearer")
	}
	for _, read := range []func() (any, error){
		func() (any, error) { return repo.GetByKey(ctx, created.Key) },
		func() (any, error) { return repo.GetMetadataByID(ctx, created.ID) },
		func() (any, error) { return repo.ListByUser(ctx, 2) },
		func() (any, error) { return repo.ListAll(ctx) },
	} {
		result, err := read()
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), created.Key) {
			t.Fatal("repository read hydrated the reusable bearer")
		}
	}
	altered := created.Key[:len(created.Key)-1] + "x"
	if _, err := repo.GetByKey(ctx, altered); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatal("altered bearer was not rejected")
	}
	if err := repo.Delete(ctx, created.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetByKey(ctx, created.Key); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatal("revoked bearer was not rejected")
	}
	t.Log("storage, repository reads, valid/altered/revoked controls passed; no bearer values recorded")
}

func TestAPIKeyVerifierUpgrade(t *testing.T) {
	repo := apiKeyLegacyRepository(t)
	ctx := t.Context()
	generated, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	// Malformed but previously usable credentials must not require rotation.
	keys := []string{generated, "sa_x", "sa_legacy_Δ", "sa_existing_credential_kept_unchanged"}
	for _, key := range keys[:3] {
		if _, err := repo.pool.Exec(ctx, `INSERT INTO api_keys(user_id,label,api_key,rate_tier,scopes,last_used_at) VALUES(2,'Upgrade',$1,'elevated',ARRAY['admin:users'],'2026-01-01')`, key); err != nil {
			t.Fatal("seed legacy credential failed")
		}
	}
	var before, after string
	if err := repo.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(ak)-'api_key' ORDER BY id)::text FROM api_keys ak`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	up, down := apiKeyVerifierMigration(t)
	if _, err := repo.pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	if err := repo.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(ak)-'key_hash'-'key_prefix' ORDER BY id)::text FROM api_keys ak`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("migration changed key identity, configuration revision, scopes, tier, or usage")
	}
	for i, key := range keys {
		authenticated, err := repo.GetByKey(ctx, key)
		if err != nil || authenticated.Key != "" {
			t.Fatalf("legacy credential %d authentication or hydration changed", i)
		}
		metadata, err := repo.GetMetadataByID(ctx, authenticated.ID)
		if err != nil {
			t.Fatal(err)
		}
		prefix := ""
		if i == 0 {
			prefix = key[:11]
		}
		if metadata.KeyPrefix != prefix {
			t.Fatal("legacy prefix changed")
		}
		var safe bool
		if err := repo.pool.QueryRow(ctx, `SELECT key_hash=$2 AND NOT EXISTS(SELECT 1 FROM jsonb_each_text(to_jsonb(ak)) field WHERE field.value=$3) FROM api_keys ak WHERE id=$1`, authenticated.ID, HashLinkToken(key), key).Scan(&safe); err != nil || !safe {
			t.Fatal("migrated logical row retained a bearer or incorrect verifier")
		}
		if _, err := repo.GetByKey(ctx, HashLinkToken(key)); !errors.Is(err, ErrAPIKeyNotFound) {
			t.Fatal("stored verifier was accepted as a bearer")
		}
	}
	// The existing unique B-tree must still support a bounded lookup.
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	var plan string
	if err := tx.QueryRow(ctx, `EXPLAIN (FORMAT JSON) SELECT `+apiKeyColumns+` FROM api_keys WHERE key_hash=$1`, HashLinkToken(generated)).Scan(&plan); err != nil || !strings.Contains(plan, "Index") {
		t.Fatal("verifier lookup has no usable index")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, down); err == nil {
		t.Fatal("nonempty verifier table allowed an irreversible downgrade")
	}
	if _, err := repo.GetByKey(ctx, generated); err != nil {
		t.Fatal("refused downgrade damaged an existing credential")
	}
	for _, statement := range []string{
		`INSERT INTO api_keys(user_id,label,key_hash) VALUES(1,'Raw',$1)`,
		`INSERT INTO api_keys(user_id,label,key_hash,key_prefix) VALUES(1,'Bad prefix',repeat('a',64),$1)`,
	} {
		_, err := repo.pool.Exec(ctx, statement, generated)
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23514" {
			t.Fatal("schema did not reject reusable credential storage")
		}
	}
	t.Log("historical credentials preserved; logical rows contain verifiers only; index, metadata, revision, and downgrade controls passed")
}

func TestAPIKeyVerifierEmptyMigrationRoundTrip(t *testing.T) {
	repo := apiKeyLegacyRepository(t)
	ctx := t.Context()
	if _, err := repo.pool.Exec(ctx, `DELETE FROM api_keys`); err != nil {
		t.Fatal(err)
	}
	up, down := apiKeyVerifierMigration(t)
	for _, migration := range []string{up, down, up} {
		if _, err := repo.pool.Exec(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	created, err := repo.Create(ctx, 1, "Fresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetByKey(ctx, created.Key); err != nil {
		t.Fatal("fresh migration cannot authenticate a new key")
	}
}

func TestAPIKeyVerifierLegacyDigestCollision(t *testing.T) {
	repo := apiKeyLegacyRepository(t)
	ctx := t.Context()
	key, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	// Both values fit the old schema. Conversion must not collide with a
	// legacy row that happens to equal another row's final digest.
	keys := []string{key, HashLinkToken(key)}
	for _, value := range keys {
		if _, err := repo.pool.Exec(ctx, `INSERT INTO api_keys(user_id,label,api_key) VALUES(2,'Legacy collision',$1)`, value); err != nil {
			t.Fatal("legacy collision fixture failed")
		}
	}
	up, _ := apiKeyVerifierMigration(t)
	if _, err := repo.pool.Exec(ctx, up); err != nil {
		t.Fatal("conversion failed for distinct legacy credentials")
	}
	var previousID int64
	for _, value := range keys {
		found, err := repo.GetByKey(ctx, value)
		if err != nil || found.ID == previousID || found.Key != "" {
			t.Fatal("conversion did not preserve distinct legacy credential identities")
		}
		previousID = found.ID
	}
}

func TestAPIKeyVerifierMigrationRollback(t *testing.T) {
	repo := apiKeyLegacyRepository(t)
	ctx := t.Context()
	up, _ := apiKeyVerifierMigration(t)
	// Fail after the rewrite and before the column rename. PostgreSQL must
	// restore the old credential and re-enable its identity trigger together.
	broken := strings.Replace(up, "ALTER TABLE api_keys RENAME", "SELECT 1/0;\nALTER TABLE api_keys RENAME", 1)
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, broken); err == nil {
		t.Fatal("injected migration failure was not reached")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var restored bool
	if err := repo.pool.QueryRow(ctx, `SELECT api_key=$1 AND NOT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='api_keys'::regclass AND attname='key_prefix' AND NOT attisdropped) FROM api_keys WHERE id=1`, "sa_existing_credential_kept_unchanged").Scan(&restored); err != nil || !restored {
		t.Fatal("failed migration did not restore the historical row and schema")
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE api_keys SET api_key='replacement' WHERE id=1`); err == nil {
		t.Fatal("failed migration left credential identity mutable")
	}
	if _, err := repo.pool.Exec(ctx, up); err != nil {
		t.Fatal("migration could not retry after rollback")
	}
	if _, err := repo.GetByKey(ctx, "sa_existing_credential_kept_unchanged"); err != nil {
		t.Fatal("retry lost the existing credential")
	}
}

func TestAPIKeyVerifierDownSeesConcurrentInsert(t *testing.T) {
	repo := apiKeyLegacyRepository(t)
	ctx := t.Context()
	if _, err := repo.pool.Exec(ctx, `DELETE FROM api_keys`); err != nil {
		t.Fatal(err)
	}
	up, down := apiKeyVerifierMigration(t)
	if _, err := repo.pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	writer, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(ctx) }()
	if _, err := writer.Exec(ctx, `INSERT INTO api_keys(user_id,label,key_hash) VALUES(1,'In flight',$1)`, HashLinkToken("sa_in_flight")); err != nil {
		t.Fatal(err)
	}
	migrator, err := repo.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer migrator.Release()
	var pid uint32
	if err := migrator.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := migrator.Exec(ctx, down)
		done <- err
	}()
	// Commit only once Down waits on the writer's table lock.
	deadline := time.Now().Add(10 * time.Second)
	for waiting := false; !waiting; {
		select {
		case err := <-done:
			t.Fatalf("Down finished before the in-flight insert committed: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Down never waited for the in-flight insert")
		}
		if err := repo.pool.QueryRow(ctx, `SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("Down reverted a table that held a committed key")
	}
	if _, err := repo.GetByKey(ctx, "sa_in_flight"); err != nil {
		t.Fatal("refused downgrade damaged the in-flight credential")
	}
}
