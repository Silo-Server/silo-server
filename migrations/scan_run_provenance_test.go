package migrations

import (
	"strings"
	"testing"
)

func TestScanRunProvenanceIndexRetryUsesConcurrentCleanup(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260807094340_add_scan_run_provenance_to_media_availability.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	up := strings.SplitN(string(migrationBytes), "-- +goose Down", 2)[0]
	up = strings.Join(strings.Fields(up), " ")

	for _, index := range []struct {
		name  string
		table string
	}{
		{name: "idx_media_files_first_seen_scan_run_id", table: "media_files"},
		{name: "idx_episode_libraries_first_seen_scan_run_id", table: "episode_libraries"},
	} {
		drop := "DROP INDEX CONCURRENTLY IF EXISTS public." + index.name + ";"
		create := "CREATE INDEX CONCURRENTLY " + index.name + " ON public." + index.table + " (first_seen_scan_run_id) WHERE first_seen_scan_run_id IS NOT NULL;"
		dropAt, createAt := strings.Index(up, drop), strings.Index(up, create)
		if dropAt < 0 {
			t.Fatalf("migration missing concurrent retry cleanup %q", drop)
		}
		if createAt < 0 {
			t.Fatalf("migration missing concurrent create %q", create)
		}
		if dropAt > createAt {
			t.Fatalf("retry cleanup for %s must precede its create", index.name)
		}
		for _, ordinaryDrop := range []string{
			"DROP INDEX public." + index.name + ";",
			"DROP INDEX IF EXISTS public." + index.name + ";",
		} {
			if strings.Contains(up, ordinaryDrop) {
				t.Fatalf("migration contains blocking ordinary drop %q", ordinaryDrop)
			}
		}
	}
}
