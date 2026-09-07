package catalog

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildHistoryDisplayBaseQueryIncludesSnapshotAndLibraryAccess(t *testing.T) {
	snapshot := time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC)
	query, args := buildHistoryDisplayBaseQuery(AccessFilter{
		UserID:             7,
		ProfileID:          "profile-1",
		AllowedLibraryIDs:  []int{11, 12},
		DisabledLibraryIDs: []int{99},
		MaxContentRating:   "PG-13",
	}, &snapshot, false)

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

func TestHistoryEpisodeIDsPreserveParentAccessChecks(t *testing.T) {
	query, _ := buildHistoryDisplayBaseQuery(AccessFilter{UserID: 7, ProfileID: "profile-1"}, nil, true)
	if !strings.Contains(query, "SELECT h.media_item_id AS display_id") || !strings.Contains(query, "JOIN media_items mi ON mi.content_id = COALESCE(") {
		t.Fatal("episode IDs must retain access checks on the parent media item")
	}
}

func TestHistoryPreviewPagesAreBoundedAndBindEveryArgument(t *testing.T) {
	r := &CatalogResolver{itemRepo: NewItemRepository(nil)}
	for _, scope := range []string{"", "episode"} {
		for _, sort := range []string{"date_viewed", "rating_imdb", "title"} {
			t.Run(scope+"/"+sort, func(t *testing.T) {
				req := CatalogRequest{Source: CatalogSourceHistory, Limit: 2, Offset: 1, SnapshotAt: new(time.Now().UTC()), SearchQuery: "HES story", NamePrefix: "he", Query: QueryDefinition{MediaScope: scope, Sort: QuerySort{Field: sort, Order: "asc"}, Limit: new(3)}}
				plan, err := r.buildHistoryPreviewPagePlan(req, AccessFilter{UserID: 7, ProfileID: "profile-1", AllowedLibraryIDs: []int{11}})
				if err != nil {
					t.Fatal(err)
				}
				pageSQL, pageArgs := plan.pagedSQL(false)
				countSQL, countArgs := plan.countSQL()
				for _, query := range []struct {
					sql  string
					args []any
				}{{pageSQL, pageArgs}, {countSQL, countArgs}} {
					if !strings.Contains(query.sql, "JOIN history_display") || !strings.Contains(query.sql, "LIMIT $") {
						t.Fatalf("history must filter and bound rows in SQL: %s", query.sql)
					}
					seen := make(map[int]bool)
					for _, match := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(query.sql, -1) {
						n, _ := strconv.Atoi(match[1])
						if n < 1 || n > len(query.args) {
							t.Fatalf("unbound $%d of %d", n, len(query.args))
						}
						seen[n] = true
					}
					if len(seen) != len(query.args) {
						t.Fatalf("SQL uses %d of %d arguments", len(seen), len(query.args))
					}
				}
				if !strings.Contains(pageSQL, "OFFSET $") || plan.limit != 2 || plan.maxResults != 3 {
					t.Fatal("query cap and page bounds must survive history composition")
				}
				if sort == "date_viewed" && !strings.Contains(pageSQL, "ORDER BY history_source.watched_at ASC, mi.content_id ASC") {
					t.Fatal("date viewed must use grouped watch-event order")
				}
				if strings.Contains(pageSQL, "mi.created_at <=") {
					t.Fatal("history snapshot must fence watch events, not imported item creation")
				}
			})
		}
	}
}
