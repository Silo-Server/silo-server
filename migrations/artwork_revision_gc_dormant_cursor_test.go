package migrations

import (
	"strings"
	"testing"
)

func TestArtworkRevisionGCDormantCursorMigrationChangesIndexConcurrently(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20261008041147_artwork_revision_gc_dormant_cursor.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(migrationBytes)
	if !strings.HasPrefix(migration, "-- +goose NO TRANSACTION") {
		t.Fatal("concurrent index changes require NO TRANSACTION")
	}
	up, down, found := strings.Cut(migration, "-- +goose Down")
	if !found {
		t.Fatal("migration has no down marker")
	}
	up, down = strings.Join(strings.Fields(up), " "), strings.Join(strings.Fields(down), " ")
	const drop = "DROP INDEX CONCURRENTLY IF EXISTS public.artwork_revision_gc_dormant_idx;"
	if !strings.Contains(up, drop) {
		t.Fatalf("up must drop the old dormant index concurrently: %q", drop)
	}
	create := "CREATE INDEX CONCURRENTLY artwork_revision_gc_dormant_idx ON public.artwork_revision_gc_candidates (updated_at, id) WHERE next_attempt_at IS NULL;"
	dropAt, createAt := strings.Index(down, drop), strings.Index(down, create)
	if dropAt < 0 || createAt < 0 || dropAt > createAt {
		t.Fatalf("down must clean up a failed build concurrently before %q", create)
	}
}
