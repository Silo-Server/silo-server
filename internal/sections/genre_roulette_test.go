package sections

import (
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

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
