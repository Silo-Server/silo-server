package catalog

import (
	"regexp"
	"strings"
	"testing"
)

// TestSearchCursorSQLMatchesOffsetPlanShape pins the cursor (v2) search SQL to
// the same candidate predicates as the offset (v1) search SQL when the source
// definition carries no filter rules. Before this test the cursor builder added
// a second, unconditional `mi.content_id IN (<library preview plan>)` predicate
// that materialized and sorted the whole library on every page; on a 795k-episode
// library that alone exceeded the three-second search deadline while v1 answered
// in tens of milliseconds. The predicate must only appear when the definition
// actually has rules to enforce.
func TestSearchCursorSQLMatchesOffsetPlanShape(t *testing.T) {
	r := &ItemRepository{}
	parsed := parseSearchQuery("lincoln")
	filter := AccessFilter{AllowedLibraryIDs: []int{2}}
	definitionPredicate := regexp.MustCompile(`content_id IN \(SELECT`)

	for _, scope := range []string{"", "series", "movie"} {
		t.Run("scope="+scope, func(t *testing.T) {
			itemTypes := MediaScopeItemTypes(scope)
			v1SQL, _, v1Args := r.buildSearchSQLFromParsed(parsed, itemTypes, 6, 0, filter, true)
			def := QueryDefinition{LibraryIDs: []int{2}, MediaScope: scope, Sort: QuerySort{Field: "relevance", Order: "desc"}}
			options := &searchCursorSQL{request: SearchCursorOptions{Definition: def}}
			v2SQL, _, v2Args := r.buildMixedSearchCursorSQL(parsed, itemTypes, 6, 0, filter, true, options)
			if options.err != nil {
				t.Fatal(options.err)
			}
			if definitionPredicate.MatchString(strings.ReplaceAll(v2SQL, "content_id IN (SELECT title_mi.content_id", "indexed title candidate")) {
				t.Fatal("cursor search re-scans the library through a definition predicate although the definition has no rules")
			}
			if strings.Contains(v2SQL, "sort_added") {
				t.Fatal("cursor search carries the browse sort join; relevance search must not")
			}
			// Same candidate WHERE clauses as v1: the cursor query differs only in
			// its page CTE (no OFFSET, cursor key columns), never in what it scans.
			if got, want := scoredWhereClauses(v2SQL), scoredWhereClauses(v1SQL); !equalStrings(got, want) {
				t.Fatalf("cursor candidate predicates differ from offset search:\n got %q\nwant %q", got, want)
			}
			// v1 binds limit and offset; v2 binds only limit. Everything before is shared.
			if len(v2Args) != len(v1Args)-1 {
				t.Fatalf("cursor search binds %d args, offset search %d", len(v2Args), len(v1Args))
			}
			for i := range v2Args[:len(v2Args)-1] {
				if !equalArg(v1Args[i], v2Args[i]) {
					t.Fatalf("arg %d differs: cursor %v, offset %v", i+1, v2Args[i], v1Args[i])
				}
			}
		})
	}

	// A definition with rules still constrains candidates through the predicate.
	def := QueryDefinition{LibraryIDs: []int{2}, Groups: []QueryGroup{{Rules: []QueryRule{{Field: "year", Op: "gte", Value: 2005}}}}}
	options := &searchCursorSQL{request: SearchCursorOptions{Definition: def}}
	ruledSQL, _, _ := r.buildMixedSearchCursorSQL(parsed, nil, 6, 0, filter, true, options)
	if options.err != nil {
		t.Fatal(options.err)
	}
	if !definitionPredicate.MatchString(strings.ReplaceAll(ruledSQL, "content_id IN (SELECT title_mi.content_id", "indexed title candidate")) {
		t.Fatal("a definition with rules lost its candidate predicate")
	}

	// A scope mismatch between branch and definition still empties the branch.
	mismatch := &searchCursorSQL{request: SearchCursorOptions{Definition: QueryDefinition{MediaScope: "episode"}}}
	var conditions []string
	var args []any
	index := 1
	r.appendSearchCursorDefinition(mismatch, false, filter, &conditions, &args, &index)
	if len(conditions) != 1 || conditions[0] != "FALSE" {
		t.Fatalf("media branch under an episode definition must be emptied, got %v", conditions)
	}
}

// scoredWhereClauses returns every WHERE clause of the scored candidate CTEs,
// which is where the library, type, access and match predicates live.
func scoredWhereClauses(sql string) []string {
	scored := sql
	if i := strings.Index(sql, ", page AS ("); i >= 0 {
		scored = sql[:i]
	}
	var out []string
	for _, part := range strings.Split(scored, "\nWHERE ")[1:] {
		line := part
		if i := strings.IndexAny(line, "\n)"); i >= 0 && strings.HasPrefix(strings.TrimSpace(line), "NOT EXISTS (SELECT 1 FROM title_scored") {
			line = line[:i]
		}
		out = append(out, strings.TrimSpace(strings.SplitN(line, "\n", 2)[0]))
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalArg(a, b any) bool {
	switch x := a.(type) {
	case []int:
		y, ok := b.([]int)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
		return true
	case []string:
		y, ok := b.([]string)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// Unrestricted broad matches need a flattenable join so PostgreSQL can use
// parallel admission and DISTINCT sorting. Library policy retains the boundary
// that prevents rare terms from scanning all episodes under admitted parents.
func TestEpisodeSearchPlanningBoundaryFollowsPolicy(t *testing.T) {
	repo := &ItemRepository{}
	for _, tc := range []struct {
		name   string
		filter AccessFilter
		fenced bool
	}{
		{name: "unrestricted"},
		{name: "allowed", filter: AccessFilter{AllowedLibraryIDs: []int{2}}, fenced: true},
		{name: "deny all", filter: AccessFilter{AllowedLibraryIDs: []int{}}, fenced: true},
		{name: "disabled", filter: AccessFilter{DisabledLibraryIDs: []int{2}}, fenced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataSQL, countSQL, _ := repo.buildMixedSearchSQLFromParsed(parseSearchQuery("signal"), []string{"episode"}, 20, 0, tc.filter, true)
			for _, query := range []string{dataSQL, countSQL} {
				if got := strings.Contains(query, "search_series_parent_id"); got != tc.fenced {
					t.Fatalf("parent planning boundary = %v, want %v", got, tc.fenced)
				}
			}
		})
	}
}
