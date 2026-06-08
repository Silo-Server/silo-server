package migrations

import (
	"strings"
	"testing"
)

func TestUnifyUserCollectionsPreservesExistingCollectionTypes(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/156_unify_user_collections.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	parts := strings.SplitN(string(migrationBytes), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("migration missing goose Down section")
	}
	up := normalizeSQL(parts[0])
	down := normalizeSQL(parts[1])

	for _, collectionType := range []string{"'manual'", "'smart'", "'mdblist'", "'tmdb'", "'trakt'", "'synced'", "'playlist'"} {
		if !strings.Contains(up, collectionType) {
			t.Fatalf("up migration collection_type constraint missing %s", collectionType)
		}
	}
	for _, collectionType := range []string{"'manual'", "'smart'", "'mdblist'", "'tmdb'", "'trakt'", "'synced'"} {
		if !strings.Contains(down, collectionType) {
			t.Fatalf("down migration collection_type constraint missing %s", collectionType)
		}
	}
	if strings.Contains(down, "collection_type IN ('playlist', 'smart')") {
		t.Fatal("down migration must not delete all smart personal collections")
	}
}
