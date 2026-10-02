package migrations

import (
	"strings"
	"testing"
)

func TestPlaybackNodeRoutingPolicyMigrationPreservesLegacyFallback(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260830022104_playback_node_routing_policy.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	parts := strings.SplitN(string(migrationBytes), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("migration missing goose Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	down := strings.Join(strings.Fields(parts[1]), " ")

	for _, key := range []string{
		"playback.routing.remux_execution",
		"playback.routing.video_transcode_execution",
		"download.local_transcode_fallback",
	} {
		if !strings.Contains(up, "SELECT '"+key+"', 'worker_only'") &&
			!strings.Contains(up, "SELECT '"+key+"', 'false'") {
			t.Fatalf("up migration does not preserve false fallback under %s", key)
		}
	}
	legacyPredicate := "key = 'playback.local_transcode_fallback' AND lower(trim(value)) = 'false'"
	if got := strings.Count(up, legacyPredicate); got != 3 {
		t.Fatalf("legacy false predicate appears %d times, want 3", got)
	}
	deleteLegacy := "DELETE FROM server_settings WHERE key = 'playback.local_transcode_fallback'"
	if deleteAt := strings.Index(up, deleteLegacy); deleteAt < 0 ||
		deleteAt < strings.LastIndex(up, "ON CONFLICT (key) DO NOTHING") {
		t.Fatal("up migration must copy every legacy value before deleting the old setting")
	}

	for _, key := range []string{
		"playback.routing.direct_play_egress",
		"playback.routing.remux_execution",
		"playback.routing.remux_egress",
		"playback.routing.video_transcode_execution",
		"playback.routing.video_transcode_egress",
		"download.local_transcode_fallback",
	} {
		if !strings.Contains(down, "'"+key+"'") {
			t.Fatalf("down migration does not remove %s", key)
		}
	}
	if !strings.Contains(down, "SELECT 'playback.local_transcode_fallback', 'false'") {
		t.Fatal("down migration does not restore the legacy hard fallback setting")
	}
	downloadPredicate := "key = 'download.local_transcode_fallback' AND lower(trim(value)) = 'false'"
	if !strings.Contains(down, downloadPredicate) {
		t.Fatal("down migration does not restore the legacy fallback from prepared-download policy")
	}
}
