package migrations

import (
	"strings"
	"testing"
)

func TestCatalogSearchEventRetentionMigrationUsesCrashSafePartialIndexSwap(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260826231720_catalog_search_event_retention.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := strings.Join(strings.Fields(string(migrationBytes)), " ")
	for _, fragment := range []string{
		"-- +goose NO TRANSACTION",
		"c.relname = 'idx_catalog_search_index_events_ready' AND NOT i.indisvalid",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_catalog_search_index_events_ready",
		"ON public.catalog_search_index_events (provider, available_at, id) WHERE processed_at IS NULL",
		"DROP INDEX CONCURRENTLY IF EXISTS public.idx_catalog_search_index_events_pending",
		"c.relname = 'idx_catalog_search_index_events_pending' AND NOT i.indisvalid",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_catalog_search_index_events_pending",
		"DROP INDEX CONCURRENTLY IF EXISTS public.idx_catalog_search_index_events_ready",
	} {
		if !strings.Contains(migration, fragment) {
			t.Fatalf("catalog search event retention migration missing %q", fragment)
		}
	}
}
