package migrations

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestDownloadArtifactToneMapConstraintAcceptsEverySourceKind(t *testing.T) {
	entries, err := fs.ReadDir(FS, "sql")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	// Entries are sorted by timestamped name, so the last match is the
	// definition that applies after every migration has run.
	var name, latest string
	for _, entry := range entries {
		migrationBytes, err := FS.ReadFile("sql/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		up, _, _ := strings.Cut(string(migrationBytes), "-- +goose Down")
		if strings.Contains(up, "ADD CONSTRAINT download_artifacts_tone_map_recipe_check") {
			name, latest = entry.Name(), up
		}
	}
	if latest == "" {
		t.Fatal("no migration defines download_artifacts_tone_map_recipe_check")
	}
	for _, kind := range tonemap.AllSourceKinds() {
		if !strings.Contains(latest, "'"+string(kind)+"'") {
			t.Errorf("%s: download_artifacts_tone_map_recipe_check is missing source kind %q", name, kind)
		}
	}
}
