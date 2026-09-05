package catalog

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHistoryFacetSourceStaysInSQL(t *testing.T) {
	for _, scope := range []string{"movie", "episode"} {
		t.Run(scope, func(t *testing.T) {
			snapshot := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
			filters := BrowseFilters{Type: scope, NamePrefix: "HES", LibraryID: 9}
			scopeHistoryFacetFilters(&filters, CatalogRequest{
				Source: CatalogSourceHistory, SnapshotAt: &snapshot,
				Query: QueryDefinition{MediaScope: scope},
			}, AccessFilter{UserID: 7, ProfileID: "profile-1"})
			if filters.ContentIDs != nil {
				t.Fatal("history facets must not materialize an ID allowlist")
			}
			from, where, args, empty := filterWhereClauseForSource(filters, catalogBaseRelationForScope(scope), scope)
			if empty {
				t.Fatal("history presence must be decided by SQL, not an empty Go allowlist")
			}
			for _, fragment := range []string{
				"mi.content_id IN (SELECT display_id FROM (",
				"h.user_id = $4", "h.profile_id = $5", "h.watched_at <= $6",
				"FROM user_history_hidden_items hhi",
			} {
				if !strings.Contains(where, fragment) {
					t.Fatalf("missing %q in scoped facet SQL: %s", fragment, where)
				}
			}
			wantPrefix := []any{scope, "hes%", 9, 7, "profile-1", snapshot}
			if len(args) < len(wantPrefix) || !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
				t.Fatalf("rebased args = %#v, want prefix %#v", args, wantPrefix)
			}
			if scope == "episode" {
				if !strings.Contains(where, "SELECT h.media_item_id AS display_id") || !strings.Contains(where, "FROM episode_libraries") || !strings.Contains(from, "FROM episodes e") {
					t.Fatal("episode facets must use episode identities and episode membership")
				}
			} else if !strings.Contains(where, "COALESCE(") {
				t.Fatal("ordinary history facets must retain episode-to-series display identity")
			}
		})
	}
}

func TestHistorySourceSQLAlsoScopesBrowsePlan(t *testing.T) {
	filters := BrowseFilters{Type: "movie", Limit: 20}
	scopeHistoryFacetFilters(&filters, CatalogRequest{Source: CatalogSourceHistory}, AccessFilter{UserID: 7, ProfileID: "profile-1"})
	plan, empty, err := NewBrowseRepository(nil).buildBrowsePlan(filters)
	if err != nil || empty {
		t.Fatalf("build scoped browse plan: empty=%v err=%v", empty, err)
	}
	query, args := plan.countSQL()
	if !strings.Contains(query, "mi.content_id IN (SELECT display_id FROM (") || !strings.Contains(query, "h.user_id = $2") || !strings.Contains(query, "h.profile_id = $3") {
		t.Fatalf("browse source scope or parameter rebasing missing: %s", query)
	}
	if len(args) != 4 || args[0] != "movie" || args[1] != 7 || args[2] != "profile-1" {
		t.Fatalf("unexpected browse args: %#v", args)
	}
}
