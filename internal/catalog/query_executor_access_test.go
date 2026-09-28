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
