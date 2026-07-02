package migrations

import (
	"strings"
	"testing"
)

func TestEpisodesSeasonIDIndexMigrationWrapsDollarQuotedBlock(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260618164519_add_episodes_season_id_index.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	migration := string(migrationBytes)
	for _, want := range []string{
		"-- +goose StatementBegin\nDO $$",
		"END;\n$$;\n-- +goose StatementEnd",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episodes_season_id_episode_number",
	} {
		if !strings.Contains(migration, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
}
