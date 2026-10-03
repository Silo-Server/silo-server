package migrations

import (
	"strings"
	"testing"
	"time"
)

// The backfill turns each person's last lookup into an outcome. Only a person
// written after the attempt with nothing left to fill is trusted for 90 days:
// other writes (credit enrichment, photo caching) also move updated_at, so it
// alone doesn't show an answer. Any other lookup gets one more retry a week
// after it. People never looked up are left for the sweep to take first
// (#1606).
func TestPersonRefreshOutcomesBackfillPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE people (
	id bigint PRIMARY KEY,
	tmdb_id text NOT NULL DEFAULT '',
	imdb_id text NOT NULL DEFAULT '',
	tvdb_id text NOT NULL DEFAULT '',
	bio text,
	photo_path text,
	birth_date date,
	updated_at timestamptz NOT NULL,
	metadata_refresh_attempted_at timestamptz
);
CREATE INDEX idx_people_metadata_refresh_due ON people (
	GREATEST(updated_at, COALESCE(metadata_refresh_attempted_at, updated_at)), id
) WHERE tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '';
INSERT INTO people (id, tmdb_id, bio, photo_path, birth_date, updated_at, metadata_refresh_attempted_at) VALUES
	(1, '1', 'Bio', '/p/1.jpg', '1970-01-01', '2026-06-02T00:00:00Z', '2026-06-01T00:00:00Z'),
	(2, '2', 'Bio', '/p/2.jpg', '1970-01-01', '2026-05-01T00:00:00Z', '2026-06-01T00:00:00Z'),
	(3, '3', NULL, NULL, NULL, '2026-06-01T00:00:00Z', NULL),
	-- Written after the attempt, by a photo cache say, with the bio still
	-- missing: not evidence of an answer.
	(4, '4', '', '/p/4.jpg', '1970-01-01', '2026-06-02T00:00:00Z', '2026-06-01T00:00:00Z');`)

	// CREATE INDEX CONCURRENTLY can't run in the fixture's transaction.
	up := strings.ReplaceAll(adminMigrationSQL(t, "20260929230000_person_refresh_outcomes", schema, false), " CONCURRENTLY", "")
	migrationExec(t, tx, up)

	want := map[int64]struct {
		outcome  *string
		failures int
		dueAt    *time.Time
	}{
		1: {outcome: ptr("answered"), failures: 0, dueAt: ptr(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC))},
		2: {outcome: ptr("failed"), failures: 1, dueAt: ptr(time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC))},
		3: {},
		4: {outcome: ptr("failed"), failures: 1, dueAt: ptr(time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC))},
	}
	rows, err := tx.Query(t.Context(), `SELECT id, metadata_refresh_outcome, metadata_refresh_failures, metadata_refresh_due_at FROM people ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var outcome *string
		var failures int
		var dueAt *time.Time
		if err := rows.Scan(&id, &outcome, &failures, &dueAt); err != nil {
			t.Fatal(err)
		}
		w := want[id]
		if !equalPtr(outcome, w.outcome) || failures != w.failures || !equalTimePtr(dueAt, w.dueAt) {
			t.Errorf("person %d: outcome=%v failures=%d due=%v, want outcome=%v failures=%d due=%v",
				id, deref(outcome), failures, dueAt, deref(w.outcome), w.failures, w.dueAt)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	var oldIndex, newIndexes int
	if err := tx.QueryRow(t.Context(), `
		SELECT count(*) FILTER (WHERE indexname = 'idx_people_metadata_refresh_due'),
		       count(*) FILTER (WHERE indexname IN ('idx_people_refresh_never_attempted', 'idx_people_refresh_due_at', 'idx_people_refresh_unrecorded'))
		FROM pg_indexes WHERE schemaname = $1`, schema).Scan(&oldIndex, &newIndexes); err != nil {
		t.Fatal(err)
	}
	if oldIndex != 0 || newIndexes != 3 {
		t.Fatalf("after up: old index %d, new indexes %d; want 0 and 3", oldIndex, newIndexes)
	}

	down := strings.ReplaceAll(adminMigrationSQL(t, "20260929230000_person_refresh_outcomes", schema, true), " CONCURRENTLY", "")
	migrationExec(t, tx, down)
	var columns int
	if err := tx.QueryRow(t.Context(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'people'
		  AND column_name IN ('metadata_refresh_outcome', 'metadata_refresh_failures', 'metadata_refresh_due_at')`,
		schema).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_indexes WHERE schemaname = $1 AND indexname = 'idx_people_metadata_refresh_due'`,
		schema).Scan(&oldIndex); err != nil {
		t.Fatal(err)
	}
	if columns != 0 || oldIndex != 1 {
		t.Fatalf("after down: %d outcome columns and %d old index, want 0 and 1", columns, oldIndex)
	}
}

func ptr[T any](v T) *T { return &v }

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func equalPtr(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func equalTimePtr(a, b *time.Time) bool {
	return (a == nil) == (b == nil) && (a == nil || a.Equal(*b))
}
