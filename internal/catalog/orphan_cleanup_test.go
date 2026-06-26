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
		"public.abs_playback_sessions aps WHERE aps.content_id = mi.content_id",
		"public.abs_rss_feeds arf WHERE arf.library_item_id = mi.content_id",
		"public.episodes e WHERE e.series_id = mi.content_id",
		"public.podcast_feeds pf WHERE pf.media_item_id = mi.content_id",
		"public.seasons s WHERE s.series_id = mi.content_id",
		"public.user_audio_preferences uap WHERE uap.series_id = mi.content_id",
		"public.user_home_item_dismissals uhid WHERE uhid.media_item_id = mi.content_id",
		"public.user_home_item_dismissals uhid_series WHERE uhid_series.series_id = mi.content_id",
		"public.user_personal_collection_items upci WHERE upci.media_item_id = mi.content_id",
		"public.user_series_playback_preferences uspp WHERE uspp.series_id = mi.content_id",
		"public.user_subtitle_preferences usp WHERE usp.series_id = mi.content_id",
		"public.watch_provider_list_items wpli WHERE wpli.media_item_id = mi.content_id",
	} {
		if !strings.Contains(predicate, normalizePredicateSQL(want)) {
			t.Fatalf("cleanup predicate missing durable reference guard %q", want)
		}
	}
	for _, droppedTable := range []string{
		"abs_collection_items",
		"abs_playlist_items",
		"abs_playlists",
		"watch_provider_favorite_items",
	} {
		if strings.Contains(predicate, droppedTable) {
			t.Fatalf("cleanup predicate must not reference dropped legacy table %q", droppedTable)
		}
	}
}

func TestOrphanedProvisionalCleanerDeleteQueryReturnsIDsForSearchIndexCleanup(t *testing.T) {
	query := normalizePredicateSQL(deleteOrphanedProvisionalBatchSQL)
	for _, want := range []string{
		"DELETE FROM public.media_items mi",
		"RETURNING mi.content_id",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("orphaned provisional cleanup delete query missing %q: %s", want, query)
		}
	}
}
