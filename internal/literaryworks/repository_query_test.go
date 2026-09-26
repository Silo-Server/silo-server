package literaryworks

import (
	"strconv"
	"strings"
	"testing"
)

// splitCandidateBranches returns the SELECT bodies of the WITH candidates CTE.
func splitCandidateBranches(t *testing.T, query string) []string {
	t.Helper()
	start := strings.Index(query, "WITH candidates AS (")
	if start < 0 {
		t.Fatalf("query missing candidates CTE:\n%s", query)
	}
	body := query[start+len("WITH candidates AS ("):]
	end := strings.Index(body, "\n\t)")
	if end < 0 {
		t.Fatalf("query missing CTE close:\n%s", query)
	}
	return strings.Split(body[:end], "UNION")
}

func TestBuildMatchCandidateQuery_TitleOnly(t *testing.T) {
	src := MatchItem{ContentID: "c-1", Type: "ebook", Title: "Project Hail Mary"}
	query, args := buildMatchCandidateQuery(src, 20, nil, nil)

	if got := strings.Count(query, "UNION"); got != 0 {
		t.Fatalf("single predicate should not emit UNION, got %d:\n%s", got, query)
	}
	if !strings.Contains(query, "LOWER(mi.title) = LOWER($3)") {
		t.Fatalf("missing title predicate:\n%s", query)
	}
	// The OR-joined disjunction that defeated the indexes must be gone.
	if strings.Contains(query, " OR EXISTS") {
		t.Fatalf("predicates should not be OR-joined:\n%s", query)
	}
	wantArgs := []any{"c-1", "ebook", "Project Hail Mary", 20}
	assertArgs(t, args, wantArgs)
	assertLimitPlaceholder(t, query, len(args))
	assertOuterInvariants(t, query)
}

func TestBuildMatchCandidateQuery_AllPredicates(t *testing.T) {
	idx := 3.0
	src := MatchItem{
		ContentID:   "c-1",
		Type:        "audiobook",
		Title:       "Dune",
		ExternalIDs: map[string]string{"isbn": "9780441172719"},
		SeriesName:  "Dune",
		SeriesIndex: &idx,
	}
	after := &matchCandidateCursor{title: "Dune", contentID: "c-9"}
	workID := "work-1"
	query, args := buildMatchCandidateQuery(src, 20, after, &workID)

	branches := splitCandidateBranches(t, query)
	if len(branches) != 3 {
		t.Fatalf("expected 3 UNION branches, got %d:\n%s", len(branches), query)
	}
	// Every branch must be independently indexable: it carries the cheap
	// base predicates and exactly one match predicate.
	filters := []string{"LOWER(mi.title) = LOWER(", "media_item_provider_ids", "bs.series_index ="}
	for _, b := range branches {
		if !strings.Contains(b, "mi.type IN ('ebook', 'audiobook')") {
			t.Fatalf("branch missing base type predicate:\n%s", b)
		}
		matched := 0
		for _, f := range filters {
			if strings.Contains(b, f) {
				matched++
			}
		}
		if matched != 1 {
			t.Fatalf("branch should contain exactly one match predicate, got %d:\n%s", matched, b)
		}
	}
	// Candidates are the opposite format, so an audiobook source matches
	// ebook candidates via ebook_series (never audiobook_series).
	if !strings.Contains(query, "ebook_series") || strings.Contains(query, "audiobook_series") {
		t.Fatalf("audiobook source should target ebook_series:\n%s", query)
	}

	// The ignore-list and autoWorkID exclusions belong to the outer wrapper,
	// applied once to every candidate rather than per branch (dropping them
	// from a branch would resurrect ignored pairs).
	for _, b := range branches {
		if strings.Contains(b, "literary_work_match_decisions") || strings.Contains(b, "literary_work_items") {
			t.Fatalf("ignore-list must not live inside a candidate branch:\n%s", b)
		}
	}
	if strings.Count(query, "literary_work_items") == 0 {
		t.Fatalf("autoWorkID exclusions missing from outer query:\n%s", query)
	}

	wantArgs := []any{"c-1", "audiobook", "Dune", "isbn", "9780441172719", "Dune", 3.0, "work-1", "Dune", "c-9", 20}
	assertArgs(t, args, wantArgs)
	assertLimitPlaceholder(t, query, len(args))
	assertOuterInvariants(t, query)

	// Keyset pagination bound applies in the outer query.
	if !strings.Contains(query, "(mi.title, mi.content_id) > ($9, $10)") {
		t.Fatalf("keyset predicate missing or mis-numbered:\n%s", query)
	}
	// autoWorkID member exclusion references the correct placeholder.
	if !strings.Contains(query, "WHERE member.work_id=$8") {
		t.Fatalf("autoWorkID placeholder wrong:\n%s", query)
	}
}

func TestBuildMatchCandidateQuery_NoPredicates(t *testing.T) {
	src := MatchItem{ContentID: "c-1", Type: "ebook"}
	query, args := buildMatchCandidateQuery(src, 5, nil, nil)

	branches := splitCandidateBranches(t, query)
	if len(branches) != 1 {
		t.Fatalf("no predicates should yield a single branch, got %d:\n%s", len(branches), query)
	}
	// Still bounded by the ignore-list, ordering, and limit.
	assertArgs(t, args, []any{"c-1", "ebook", 5})
	assertLimitPlaceholder(t, query, len(args))
	assertOuterInvariants(t, query)
}

func assertOuterInvariants(t *testing.T, query string) {
	t.Helper()
	if !strings.Contains(query, "FROM candidates mi") {
		t.Fatalf("outer query must select from the candidates CTE:\n%s", query)
	}
	if strings.Count(query, "d.decision = 'ignored'") != 1 {
		t.Fatalf("ignored-decision NOT EXISTS must appear exactly once:\n%s", query)
	}
	if !strings.Contains(query, "ORDER BY mi.title ASC, mi.content_id ASC") {
		t.Fatalf("outer ORDER BY missing:\n%s", query)
	}
}

func assertLimitPlaceholder(t *testing.T, query string, n int) {
	t.Helper()
	want := "LIMIT $" + strconv.Itoa(n)
	if !strings.Contains(query, want) {
		t.Fatalf("expected %q in query:\n%s", want, query)
	}
}

func assertArgs(t *testing.T, got, want []any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("arg count: got %d want %d\n got=%v\nwant=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: got %#v want %#v", i, got[i], want[i])
		}
	}
}
