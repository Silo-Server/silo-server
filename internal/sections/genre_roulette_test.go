package sections

import (
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// The genre comes only from titles the row could show, so a row with a high
// rating floor never lands on a genre that leaves it empty.
func TestGenreRouletteCandidatesMatchTheRowsTitles(t *testing.T) {
	query, args := genreRouletteCandidatesQuery(8.0, nil, nil, catalog.AccessFilter{})

	for _, want := range []string{
		"mi.tmdb_vote_average >= $1",
		"mi.tmdb_vote_count >= $2",
		"mi.type = ANY($3)",
		"LIMIT $4",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query is missing %q:\n%s", want, query)
		}
	}
	if len(args) != 4 {
		t.Fatalf("args = %v, want rating, votes, types and limit", args)
	}
	if args[0] != 8.0 || args[1] != recipes.DiscoveryMinVotes {
		t.Fatalf("rating floor args = %v, %v; want 8, %d", args[0], args[1], recipes.DiscoveryMinVotes)
	}
	if types, _ := args[2].([]string); !slices.Equal(types, genreRouletteTypes) {
		t.Fatalf("types arg = %v, want %v", args[2], genreRouletteTypes)
	}
}

func TestGenreRouletteCandidatesHonorContentScope(t *testing.T) {
	query, args := genreRouletteCandidatesQuery(6.0, nil, nil, catalog.AccessFilter{
		AllowedContentIDs: []string{"movie:1"},
		NamePrefix:        "a",
	})
	if !strings.Contains(query, "mi.content_id = ANY($") || !strings.Contains(query, " LIKE $") {
		t.Fatalf("query is missing the content allow-list or name prefix:\n%s", query)
	}
	if !slices.ContainsFunc(args, func(arg any) bool { return arg == "a%" }) {
		t.Fatalf("args = %v, want the name prefix pattern", args)
	}
}
