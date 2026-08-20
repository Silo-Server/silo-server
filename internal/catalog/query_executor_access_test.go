package catalog

import (
	"reflect"
	"slices"
	"strings"
	"testing"
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

	sql, args, err := executor.buildPreviewPageSQL(def, AccessFilter{MaxContentRating: "PG"}, 20, 0, false)
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
	allowedRatings, ok := args[3].([]string)
	if !ok || !slices.Contains(allowedRatings, "PG") || slices.Contains(allowedRatings, "R") {
		t.Fatalf("rating arg = %#v, want PG allowed and R blocked", args[3])
	}
}
