package migrations

import (
	"strings"
	"testing"
)

func TestUnifyUserCollectionsPreservesExistingCollectionTypes(t *testing.T) {
	upBytes, err := FS.ReadFile("156_unify_user_collections.up.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	downBytes, err := FS.ReadFile("156_unify_user_collections.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	up := normalizeSQL(string(upBytes))
	down := normalizeSQL(string(downBytes))

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
