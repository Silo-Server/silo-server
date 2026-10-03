package catalog

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

func TestQueryExecutorGroupQueryPreservesAccessFilters(t *testing.T) {
	executor := &QueryExecutor{}
	def := QueryDefinition{
		Match:      "all",
		LibraryIDs: []int{42},
		Groups: []QueryGroup{{
			Match: "any",
			Rules: []QueryRule{
				{Field: "genre", Op: "contains", Value: "Action"},
				{Field: "genre", Op: "contains", Value: "Adventure"},
			},
		}},
	}

	sql, args, err := executor.buildPreviewPageSQL(def, AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}, 20, 0, false)
	if err != nil {
		t.Fatalf("build preview SQL: %v", err)
	}
	groupedFilter := "WHERE (mi.genres @> ARRAY[$1]::text[] OR mi.genres @> ARRAY[$2]::text[]) AND EXISTS"
	if !strings.Contains(sql, groupedFilter) {
		t.Fatalf("group predicate is not isolated from access filters:\n%s", sql)
	}
	if len(args) != 6 || args[0] != "Action" || args[1] != "Adventure" ||
		!reflect.DeepEqual(args[2], []int{42}) || args[4] != 42 || args[5] != 21 {
		t.Fatalf("unexpected query args: %#v", args)
	}
	// The ceiling binds the minimum age it stands for; PG is 8, so an R title
	// (17) cannot satisfy the predicate.
	if args[3] != 8 {
		t.Fatalf("ceiling arg = %#v, want the PG ceiling bound as age 8", args[3])
	}
}

// A viewer's allowlist that shares no library with the query admits nothing.
// The intersection is empty then, and an empty library list would otherwise
// mean every library: jellycompat BoxSets, collection items and custom rows
// pass a query's own library_ids straight to the executor.
func TestQueryExecutorMatchesNothingOutsideTheViewersLibraries(t *testing.T) {
	disjoint := AccessFilter{AllowedLibraryIDs: []int{1}}
	noLibraries := AccessFilter{AllowedLibraryIDs: []int{}, DisabledLibraryIDs: []int{3}}
	for _, tc := range []struct {
		name   string
		def    QueryDefinition
		access AccessFilter
		empty  bool
	}{
		{name: "libraries outside the allowlist", def: QueryDefinition{LibraryIDs: []int{2}}, access: disjoint, empty: true},
		{name: "episode libraries outside the allowlist", def: QueryDefinition{MediaScope: "episode", LibraryIDs: []int{2}}, access: disjoint, empty: true},
		{name: "empty allowlist beside a disabled library", def: QueryDefinition{}, access: noLibraries, empty: true},
		{name: "empty episode allowlist beside a disabled library", def: QueryDefinition{MediaScope: "episode"}, access: noLibraries, empty: true},
		{name: "overlapping libraries", def: QueryDefinition{LibraryIDs: []int{1, 2}}, access: disjoint},
		{name: "overlapping episode libraries", def: QueryDefinition{MediaScope: "episode", LibraryIDs: []int{1, 2}}, access: disjoint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &QueryExecutor{Scope: tc.def.MediaScope}
			sql, _, err := executor.buildPreviewPageSQL(tc.def, tc.access, 20, 0, false)
			if err != nil {
				t.Fatalf("build preview SQL: %v", err)
			}
			if got := strings.Contains(sql, "1 = 0"); got != tc.empty {
				t.Fatalf("matches nothing = %v, want %v:\n%s", got, tc.empty, sql)
			}
		})
	}
}
