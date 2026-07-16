package migrations

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPlaybackProtocolV3MigrationAcceptsEveryRouteEvent(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260712103000_add_playback_protocol_v3.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(migrationBytes)
	for _, event := range playback.RouteEventNamesV3() {
		if !strings.Contains(migration, "'"+event+"'") {
			t.Errorf("route event CHECK constraint is missing %q", event)
		}
	}
}
