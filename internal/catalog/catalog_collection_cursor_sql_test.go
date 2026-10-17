package catalog

import (
	"regexp"
	"strings"
	"testing"
)

// Curated collection order must come from the joined membership row, in the
// membership table's continuation-index order. A correlated position subquery
// in ORDER BY makes PostgreSQL compute every member's position and sort them
// all before returning the first page.
func TestCollectionPositionOrderReadsJoinedMembership(t *testing.T) {
	for _, tc := range []struct {
		name string
		join string
		args []any
	}{
		{name: "library", join: libraryMembershipOrderJoin, args: []any{"collection-1"}},
		{name: "personal", join: personalMembershipOrderJoin, args: []any{7, "collection-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &QueryExecutor{SourceArgs: tc.args}
			applyCollectionPositionOrder(executor, tc.join)
			access := AccessFilter{UserID: 7, ProfileID: "profile-1"}
			plan, err := executor.buildPreviewPagePlan(QueryDefinition{}, access, 20, 0)
			if err != nil {
				t.Fatalf("buildPreviewPagePlan: %v", err)
			}
			first, _, err := plan.cursorSQL(nil)
			if err != nil {
				t.Fatalf("cursorSQL: %v", err)
			}
			order := orderByClause(t, first)
			if strings.Contains(strings.ToUpper(order), "SELECT") {
				t.Fatalf("ORDER BY still contains a subquery: %s", order)
			}
			if want := "ORDER BY COALESCE(cursor_membership.position,0) ASC NULLS LAST, cursor_membership.media_item_id ASC NULLS LAST"; order != want {
				t.Fatalf("ORDER BY = %q, want %q", order, want)
			}
			if !strings.Contains(first, " JOIN ") || !strings.Contains(first, "cursor_membership ON ") {
				t.Fatalf("paged query does not join the membership row: %s", first)
			}
			countSQL, _ := plan.countSQL()
			if !strings.Contains(countSQL, "cursor_membership ON ") {
				t.Fatalf("total does not count joined members: %s", countSQL)
			}

			position, member := "12", "movie-1"
			after := &QueryCursor{Keys: []QueryCursorValue{{Kind: cursorKindNumber, Value: &position}, {Kind: cursorKindText, Value: &member}}}
			next, args, err := plan.cursorSQL(after)
			if err != nil {
				t.Fatalf("cursorSQL(after): %v", err)
			}
			// The leading bound lets a continuation start its index range at
			// the cursor instead of walking from the first member.
			// The bound must compare integers: against a numeric value the
			// integer index column is cast and cannot bound the scan.
			if !regexp.MustCompile(`\(COALESCE\(cursor_membership\.position,0\) >= \$\d+::integer `).MatchString(next) {
				t.Fatalf("continuation is not bounded on an integer position: %s", next)
			}
			if !containsArg(args, position) || !containsArg(args, member) {
				t.Fatalf("continuation does not bind the cursor keys: %v", args)
			}
		})
	}
}
