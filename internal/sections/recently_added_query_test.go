package sections

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestBuildRecentlyAddedQueryUsesLibraryMembershipFastPathForSingleLibrary(t *testing.T) {
	t.Parallel()

	query, args := buildRecentlyAddedQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{"generated_source":"home_library_recent","filter_library_id":1,"filter_type":"movie"}`),
	}, nil, []int{1, 2}, catalog.AccessFilter{MaxContentRating: "PG-13"})

	for _, want := range []string{
		"FROM media_item_libraries mil JOIN media_items mi ON mi.content_id = mil.content_id",
		"mil.media_folder_id = $1",
		"mil.media_folder_id IN ($2, $3)",
		"mi.type = $4",
		"ORDER BY mil.first_seen_at DESC, mil.content_id ASC",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	if strings.Contains(query, "ORDER BY mi.created_at DESC") {
		t.Fatalf("single-library fast path should not order by media_items.created_at:\n%s", query)
	}
	if got, want := args[len(args)-1], 12; got != want {
		t.Fatalf("limit arg = %v, want %v", got, want)
	}
}

func TestBuildRecentlyAddedQueryKeepsGenericPathForMultiLibraryConfig(t *testing.T) {
	t.Parallel()

	query, _ := buildRecentlyAddedQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{"filter_library_ids":[1,2],"filter_type":"movie"}`),
	}, nil, nil, catalog.AccessFilter{})

	for _, want := range []string{
		"FROM media_items mi",
		// Library scope is a semi-join so an item in several selected
		// libraries yields one row (see buildLibraryScope).
		"EXISTS (SELECT 1 FROM media_item_libraries mil_scope_in WHERE mil_scope_in.content_id = mi.content_id AND mil_scope_in.media_folder_id = ANY($2))",
		"ORDER BY mi.created_at DESC, mi.content_id ASC",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	if strings.Contains(query, "ORDER BY mil.first_seen_at DESC") {
		t.Fatalf("multi-library generic path should not use the single-library first_seen_at sort:\n%s", query)
	}
}
