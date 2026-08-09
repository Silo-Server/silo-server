package database

import (
	"os"
	"strings"
	"testing"
)

func TestTranscodeDirDefaultsToSiloPath(t *testing.T) {
	initialSchema, err := os.ReadFile("../../migrations/sql/001_schema.sql")
	if err != nil {
		t.Fatalf("read initial schema: %v", err)
	}
	schema := string(initialSchema)
	if !strings.Contains(schema, "('playback.transcode_dir', '/tmp/silo-transcode')") {
		t.Fatal("initial schema does not seed the Silo transcode directory")
	}
	if strings.Contains(schema, "('playback.transcode_dir', '/tmp/streamapp-transcode')") {
		t.Fatal("initial schema still seeds the legacy StreamApp transcode directory")
	}

	migration, err := os.ReadFile("../../migrations/sql/20260809133921_rename_default_transcode_dir.sql")
	if err != nil {
		t.Fatalf("read transcode-dir migration: %v", err)
	}
	normalized := strings.Join(strings.Fields(string(migration)), " ")
	for _, fragment := range []string{
		"SET value = '/tmp/silo-transcode'",
		"WHERE key = 'playback.transcode_dir' AND value = '/tmp/streamapp-transcode'",
	} {
		if !strings.Contains(normalized, fragment) {
			t.Fatalf("transcode-dir migration missing %q:\n%s", fragment, migration)
		}
	}
}
