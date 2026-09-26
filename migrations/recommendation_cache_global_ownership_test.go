package migrations

import (
	"fmt"
	"strings"
	"testing"
)

// The user-integrity foreign key (20260912231023) deletes and then rejects the
// sentinel-owned global recommendation cache rows (issue #1261). This migration
// moves global ownership to user_id NULL while keeping personalized rows under
// the account foreign key. Assert its contract without a database.
func TestRecommendationCacheGlobalOwnershipMigrationContract(t *testing.T) {
	raw, err := FS.ReadFile("sql/20260922130000_recommendation_cache_global_ownership.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(raw)

	up, down, ok := strings.Cut(migration, "-- +goose Down")
	if !ok {
		t.Fatal("migration missing a -- +goose Down section")
	}

	for _, want := range []string{
		"ALTER TABLE public.recommendation_cache DROP CONSTRAINT recommendation_cache_pkey;",
		"ALTER TABLE public.recommendation_cache ALTER COLUMN user_id DROP NOT NULL;",
		"UPDATE public.recommendation_cache SET user_id = NULL WHERE user_id = 0;",
		"NULLS NOT DISTINCT",
		"CREATE UNIQUE INDEX recommendation_cache_identity_key",
	} {
		if !strings.Contains(up, want) {
			t.Fatalf("Up section missing %q", want)
		}
	}

	// The account foreign key must survive so personalized rows keep their
	// integrity and ON DELETE CASCADE cleanup; only global ownership changes.
	for _, ddl := range []string{
		"DROP CONSTRAINT recommendation_cache_user_id_fkey",
		"ADD CONSTRAINT recommendation_cache_user_id_fkey",
	} {
		if strings.Contains(migration, ddl) {
			t.Fatalf("migration must not touch the account foreign key: %q", ddl)
		}
	}

	if strings.Contains(down, "SET user_id = 0") {
		t.Fatal("Down must not restore the sentinel: user_id = 0 violates the retained account foreign key")
	}
	for _, want := range []string{
		"DELETE FROM public.recommendation_cache WHERE user_id IS NULL;",
		"DROP INDEX IF EXISTS recommendation_cache_identity_key;",
		"ALTER TABLE public.recommendation_cache ALTER COLUMN user_id SET NOT NULL;",
		"ADD CONSTRAINT recommendation_cache_pkey PRIMARY KEY (user_id, profile_id, rec_type, source_item_id);",
	} {
		if !strings.Contains(down, want) {
			t.Fatalf("Down section missing %q", want)
		}
	}
}

func TestRecommendationCacheGlobalOwnershipPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE users (id integer PRIMARY KEY);
CREATE TABLE recommendation_cache (
    user_id integer NOT NULL, profile_id text NOT NULL, rec_type text NOT NULL,
    source_item_id text DEFAULT '' NOT NULL, items jsonb DEFAULT '[]' NOT NULL,
    expires_at timestamptz NOT NULL, created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT recommendation_cache_pkey PRIMARY KEY (user_id, profile_id, rec_type, source_item_id),
    CONSTRAINT recommendation_cache_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE);
INSERT INTO users VALUES (1);
INSERT INTO recommendation_cache (user_id, profile_id, rec_type, expires_at) VALUES (1, 'p', 'for_you_main', now() + interval '1 day');`)
	const migration = "20260922130000_recommendation_cache_global_ownership"
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))

	upsert := func(userID int) string {
		return fmt.Sprintf(`INSERT INTO recommendation_cache (user_id, profile_id, rec_type, source_item_id, expires_at)
VALUES (NULLIF(%d, 0), '', 'popular', '', now() + interval '1 day')
ON CONFLICT (user_id, profile_id, rec_type, source_item_id) DO UPDATE SET expires_at = EXCLUDED.expires_at`, userID)
	}
	count := func(where string) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM recommendation_cache WHERE "+where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// A global write succeeds and a repeat updates the same NULL-owned row.
	migrationExec(t, tx, upsert(0))
	migrationExec(t, tx, upsert(0))
	if got := count("user_id IS NULL"); got != 1 {
		t.Fatalf("global rows = %d, want 1", got)
	}
	// Personalized rows keep the account foreign key and its cascade.
	requireMigrationSQLState(t, tx, upsert(2), "23503")
	migrationExec(t, tx, "DELETE FROM users WHERE id = 1")
	if got, global := count("user_id = 1"), count("user_id IS NULL"); got != 0 || global != 1 {
		t.Fatalf("after account delete: personalized = %d, global = %d; want 0, 1", got, global)
	}

	// Rollback succeeds with global rows present and restores the primary key.
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	if got := count("true"); got != 0 {
		t.Fatalf("rows after rollback = %d, want 0", got)
	}
	var pkey bool
	if err := tx.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_constraint
WHERE conname = 'recommendation_cache_pkey' AND contype = 'p' AND connamespace = $1::regnamespace)`, schema).Scan(&pkey); err != nil {
		t.Fatal(err)
	}
	if !pkey {
		t.Fatal("rollback did not restore recommendation_cache_pkey")
	}
}
