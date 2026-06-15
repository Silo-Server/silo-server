package catalog

import (
	"strings"
	"testing"
	"time"
)

func TestHistorySourceCanUseOptimizedPageQuery(t *testing.T) {
	req := CatalogRequest{
		Source:         CatalogSourceHistory,
		UseSourceOrder: true,
		Query:          QueryDefinition{},
	}

	if !historySourceCanUseOptimizedPageQuery(req) {
		t.Fatal("expected bare history source-order request to use optimized page query")
	}

	req.SearchQuery = "house"
	if historySourceCanUseOptimizedPageQuery(req) {
		t.Fatal("expected search history request to fall back to generic resolver path")
	}
}

func TestBuildHistoryDisplayBaseQueryIncludesSnapshotAndLibraryAccess(t *testing.T) {
	snapshot := time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC)
	query, args := buildHistoryDisplayBaseQuery(AccessFilter{
		UserID:             7,
		ProfileID:          "profile-1",
		AllowedLibraryIDs:  []int{11, 12},
		DisabledLibraryIDs: []int{99},
		MaxContentRating:   "PG-13",
	}, &snapshot)

	expectedFragments := []string{
		"h.user_id = $1",
		"h.profile_id = $2",
		"h.watched_at <= $3",
		"media_item_libraries mil",
		"media_folder_id = ANY($4)",
		"media_item_libraries mil_disabled",
		"media_folder_id = ANY($5)",
		"mi.content_rating = ANY($",
		// Anchored episode ids resolve their show by string transform; the
		// episodes probe is null-poisoned (skipped) for them and kept only for
		// non-anchored (legacy/local/malformed) ids. The predicate requires the
		// full five-part episode form, not just the 'episode-' prefix.
		"LEFT JOIN episodes e\n\t\t\t\tON e.content_id = CASE WHEN h.media_item_id LIKE 'episode-%' AND split_part(h.media_item_id, '-', 2) <> '' AND split_part(h.media_item_id, '-', 3) <> '' AND split_part(h.media_item_id, '-', 4) <> '' AND split_part(h.media_item_id, '-', 5) <> '' THEN NULL ELSE h.media_item_id END",
		"split_part(h.media_item_id, '-', 2)",
		"JOIN media_items mi ON mi.content_id = COALESCE(",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected query to contain %q, got:\n%s", fragment, query)
		}
	}

	if len(args) < 6 {
		t.Fatalf("expected snapshot, library, disabled-library, and rating args, got %d", len(args))
	}
	if got, ok := args[2].(time.Time); !ok || !got.Equal(snapshot) {
		t.Fatalf("expected snapshot arg at index 2, got %#v", args[2])
	}
}
