package database

import (
	"os"
	"strings"
	"testing"
)

func TestJellycompatServerNameDefaultsToSilo(t *testing.T) {
	initialSchema, err := os.ReadFile("../../migrations/sql/001_schema.sql")
	if err != nil {
		t.Fatalf("read initial schema: %v", err)
	}
	if !strings.Contains(string(initialSchema), "('jellyfin_compat.server_name', 'Silo')") {
		t.Fatal("initial schema does not seed the Jellyfin compat server name to Silo")
	}

	migration, err := os.ReadFile("../../migrations/sql/20260806224241_rename_default_jellycompat_server_to_silo.sql")
	if err != nil {
		t.Fatalf("read server-name migration: %v", err)
	}
	normalized := strings.Join(strings.Fields(string(migration)), " ")
	for _, fragment := range []string{
		"SET value = 'Silo'",
		"WHERE key = 'jellyfin_compat.server_name' AND value = 'StreamApp'",
	} {
		if !strings.Contains(normalized, fragment) {
			t.Fatalf("server-name migration missing %q:\n%s", fragment, migration)
		}
	}
}
