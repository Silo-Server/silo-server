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

// A requested library outside the viewer's allowed set must not fall back to
// an unscoped query: the empty intersection has to match nothing, for item and
// episode scopes, on both the page and the total query.
func TestQueryExecutorRequestedLibraryOutsideAccessMatchesNothing(t *testing.T) {
	cases := []struct {
		name      string
		requested []int
		allowed   []int
		disabled  []int
		wantEmpty bool
		// wantBound is the effective library scope the plan must bind when
		// the scope is not empty.
		wantBound []int
	}{
		{name: "inaccessible library", requested: []int{1}, allowed: []int{3}, wantEmpty: true},
		{name: "nonexistent library", requested: []int{999}, allowed: []int{3}, wantEmpty: true},
		{name: "inaccessible library with disabled libraries", requested: []int{1}, allowed: []int{3}, disabled: []int{5}, wantEmpty: true},
		{name: "empty allowlist", allowed: []int{}, wantEmpty: true},
		{name: "empty allowlist with disabled libraries", allowed: []int{}, disabled: []int{5}, wantEmpty: true},
		{name: "accessible library", requested: []int{3}, allowed: []int{3, 4}, wantBound: []int{3}},
		{name: "no request, restricted viewer", allowed: []int{3, 4}, wantBound: []int{3, 4}},
		{name: "partly accessible request", requested: []int{1, 3}, allowed: []int{3}, wantBound: []int{3}},
		{name: "unrestricted viewer, nonexistent library", requested: []int{999}, wantBound: []int{999}},
	}
	for _, scope := range []string{"", "movie", "episode"} {
		for _, tc := range cases {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				executor := &QueryExecutor{Scope: scope}
				def := QueryDefinition{MediaScope: scope, LibraryIDs: tc.requested}
				filter := AccessFilter{AllowedLibraryIDs: tc.allowed, DisabledLibraryIDs: tc.disabled}
				for _, includeTotal := range []bool{false, true} {
					sql, args, err := executor.buildPreviewPageSQL(def, filter, 20, 0, includeTotal)
					if err != nil {
						t.Fatalf("build preview SQL (total=%v): %v", includeTotal, err)
					}
					if got := strings.Contains(sql, "1 = 0"); got != tc.wantEmpty {
						t.Fatalf("total=%v: fail-closed predicate present = %v, want %v:\n%s", includeTotal, got, tc.wantEmpty, sql)
					}
					if tc.wantEmpty {
						continue
					}
					// A non-empty scope must bind the effective libraries,
					// not the raw request.
					if !slicesContainArg(args, tc.wantBound) {
						t.Fatalf("total=%v: library scope %v not bound in args %#v\n%s", includeTotal, tc.wantBound, args, sql)
					}
				}
			})
		}
	}
}

func slicesContainArg(args []any, want []int) bool {
	for _, arg := range args {
		if ids, ok := arg.([]int); ok && reflect.DeepEqual(ids, want) {
			return true
		}
	}
	return false
}

func TestSingleEpisodeCatalogLibraryIDFailsClosedOutsideAccess(t *testing.T) {
	cases := []struct {
		name      string
		requested []int
		allowed   []int
		wantID    int
		wantEmpty bool
		wantOK    bool
	}{
		{name: "inaccessible library", requested: []int{1}, allowed: []int{3}, wantEmpty: true, wantOK: true},
		{name: "nonexistent library", requested: []int{999}, allowed: []int{3}, wantEmpty: true, wantOK: true},
		{name: "empty allowlist", allowed: []int{}, wantEmpty: true, wantOK: true},
		{name: "accessible library", requested: []int{3}, allowed: []int{3, 4}, wantID: 3, wantOK: true},
		{name: "single allowed library", allowed: []int{3}, wantID: 3, wantOK: true},
		{name: "unrestricted, no library", wantOK: false},
		{name: "several libraries", requested: []int{3, 4}, allowed: []int{3, 4}, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, empty, ok := singleEpisodeCatalogLibraryID(
				QueryDefinition{LibraryIDs: tc.requested},
				AccessFilter{AllowedLibraryIDs: tc.allowed},
			)
			if id != tc.wantID || empty != tc.wantEmpty || ok != tc.wantOK {
				t.Fatalf("got (%d, empty=%v, ok=%v), want (%d, empty=%v, ok=%v)", id, empty, ok, tc.wantID, tc.wantEmpty, tc.wantOK)
			}
		})
	}
}

func TestEffectiveLibraryScope(t *testing.T) {
	cases := []struct {
		name      string
		requested []int
		allowed   []int
		wantIDs   []int
		wantEmpty bool
	}{
		{name: "unrestricted, no request"},
		{name: "unrestricted, requested", requested: []int{1, 2}, wantIDs: []int{1, 2}},
		{name: "restricted, no request", allowed: []int{3}, wantIDs: []int{3}},
		{name: "restricted, overlapping request", requested: []int{1, 3}, allowed: []int{3, 4}, wantIDs: []int{3}},
		{name: "restricted, disjoint request", requested: []int{1}, allowed: []int{3}, wantEmpty: true},
		{name: "empty allowlist", allowed: []int{}, wantEmpty: true},
		{name: "empty allowlist, requested", requested: []int{1}, allowed: []int{}, wantEmpty: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids, empty := effectiveLibraryScope(tc.requested, tc.allowed)
			if !reflect.DeepEqual(ids, tc.wantIDs) || empty != tc.wantEmpty {
				t.Fatalf("got (%v, empty=%v), want (%v, empty=%v)", ids, empty, tc.wantIDs, tc.wantEmpty)
			}
		})
	}
}
