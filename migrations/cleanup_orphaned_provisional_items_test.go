package migrations

import (
	"strings"
	"testing"
)

func TestCleanupOrphanedProvisionalItemsPreservesDurableReferences(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260607220000_cleanup_orphaned_provisional_items.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := normalizeSQL(string(migrationBytes))
	for _, want := range []string{
		"public.abs_bookmarks ab WHERE ab.library_item_id = mi.content_id",
		"public.abs_collection_items aci WHERE aci.library_item_id = mi.content_id",
		"public.abs_playback_sessions aps WHERE aps.content_id = mi.content_id",
		"public.abs_playlist_items api WHERE api.library_item_id = mi.content_id",
		"public.abs_playlists ap WHERE ap.cover_item = mi.content_id",
		"public.abs_rss_feeds arf WHERE arf.library_item_id = mi.content_id",
		"public.podcast_feeds pf WHERE pf.media_item_id = mi.content_id",
	} {
		if !strings.Contains(migration, normalizeSQL(want)) {
			t.Fatalf("cleanup migration missing durable reference guard %q", want)
		}
	}
}
