package migrations

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPlaybackProtocolV3MigrationAcceptsEveryRouteEvent(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20261017230200_playback_connections.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	// Check the latest replacement constraint, excluding the rollback.
	migration := strings.Split(string(migrationBytes), "-- +goose Down")[0]
	for _, event := range playback.RouteEventNamesV3() {
		if !strings.Contains(migration, "'"+event+"'") {
			t.Errorf("route event CHECK constraint is missing %q", event)
		}
	}
}
