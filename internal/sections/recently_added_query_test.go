package sections

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// A library's own Recently Added row stays inside the viewer's libraries and
// orders by when each item first appeared in that library, not by when the
// catalog row was created.
func TestBuildRecentlyAddedQueryOrdersSingleLibraryByFirstSeen(t *testing.T) {
	t.Parallel()

	query, args := buildRecentlyAddedQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{"generated_source":"home_library_recent","filter_library_id":1,"filter_type":"movie"}`),
	}, nil, []int{1, 2}, catalog.AccessFilter{})

	if !strings.Contains(query, "mil.media_folder_id IN ($2, $3)") || len(args) < 3 || args[1] != 1 || args[2] != 2 {
		t.Fatalf("single-library query is not limited to the allowed libraries:\n%s\nargs = %v", query, args)
	}
	if !strings.Contains(query, "ORDER BY mil.first_seen_at DESC") {
		t.Fatalf("single-library query does not order by the library's first-seen time:\n%s", query)
	}
	if strings.Contains(query, "ORDER BY mi.created_at DESC") {
		t.Fatalf("single-library query orders by media_items.created_at:\n%s", query)
	}
}

func TestBuildRecentlyAddedSingleLibraryQueryRejectsAnyDisabledMembership(t *testing.T) {
	t.Parallel()

	query, _ := buildRecentlyAddedQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{"generated_source":"home_library_recent","filter_library_id":1,"filter_type":"movie"}`),
	}, nil, nil, catalog.AccessFilter{DisabledLibraryIDs: []int{9}})

	if !strings.Contains(query, "mil.media_folder_id = $1") {
		t.Fatalf("single-library fast path must keep its direct indexed membership, got:\n%s", query)
	}
	if !strings.Contains(query, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil_scope_out") {
		t.Fatalf("single-library fast path must reject any disabled membership, got:\n%s", query)
	}
	if strings.Contains(query, "mil.media_folder_id NOT IN") {
		t.Fatalf("row-local deny leaks dual-membership items, got:\n%s", query)
	}
}
