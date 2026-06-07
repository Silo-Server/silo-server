package catalog

import (
	"strings"
	"testing"
)

func normalizePredicateSQL(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestOrphanedProvisionalPredicatePreservesDurableMediaItemReferences(t *testing.T) {
	predicate := normalizePredicateSQL(orphanedProvisionalMediaItemPredicate)
	for _, want := range []string{
		"public.abs_bookmarks ab WHERE ab.library_item_id = mi.content_id",
		"public.abs_collection_items aci WHERE aci.library_item_id = mi.content_id",
		"public.abs_playback_sessions aps WHERE aps.content_id = mi.content_id",
		"public.abs_playlist_items api WHERE api.library_item_id = mi.content_id",
		"public.abs_playlists ap WHERE ap.cover_item = mi.content_id",
		"public.abs_rss_feeds arf WHERE arf.library_item_id = mi.content_id",
		"public.podcast_feeds pf WHERE pf.media_item_id = mi.content_id",
	} {
		if !strings.Contains(predicate, normalizePredicateSQL(want)) {
			t.Fatalf("cleanup predicate missing durable reference guard %q", want)
		}
	}
}
