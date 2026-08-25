package catalog

import (
	"strings"
	"testing"
)

func TestBuildBrowsePlanDisabledLibrariesUseItemLevelExclusion(t *testing.T) {
	plan, earlyEmpty, err := (&BrowseRepository{}).buildBrowsePlan(BrowseFilters{
		DisabledLibraryIDs: []int{9},
		Limit:              20,
	})
	if err != nil {
		t.Fatalf("buildBrowsePlan: %v", err)
	}
	if earlyEmpty {
		t.Fatal("disabled-library scope must not be treated as empty")
	}

	sql, _ := plan.pagedSQL(false)
	assertDisabledLibraryItemScope(t, sql, "media_item_libraries", "content_id", "mi.content_id")
	if strings.Contains(plan.fromClause, "JOIN media_item_libraries") {
		t.Fatalf("disabled-only browse must not fan out through a membership JOIN; got:\n%s", sql)
	}
	if plan.groupByClause != "" {
		t.Fatalf("disabled-only browse must not need GROUP BY after item-level filtering; got %q", plan.groupByClause)
	}
}

func TestBuildBrowsePlanSingleLibraryKeepsFastPathWithDisabledLibraries(t *testing.T) {
	plan, earlyEmpty, err := (&BrowseRepository{}).buildBrowsePlan(BrowseFilters{
		LibraryID:          7,
		DisabledLibraryIDs: []int{9},
		Sort:               "recently_added",
		Limit:              20,
	})
	if err != nil {
		t.Fatalf("buildBrowsePlan: %v", err)
	}
	if earlyEmpty {
		t.Fatal("single enabled library plus a deny list must not be treated as empty")
	}

	sql, _ := plan.pagedSQL(false)
	if !strings.Contains(sql, "JOIN media_item_libraries mil") || !strings.Contains(sql, "mil.media_folder_id = $1") {
		t.Fatalf("single-library browse must retain its direct indexed membership path; got:\n%s", sql)
	}
	if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil_scope_out") {
		t.Fatalf("single-library browse must independently reject disabled membership; got:\n%s", sql)
	}
	if plan.groupByClause != "" {
		t.Fatalf("single-library browse must not add a GROUP BY for the independent deny check; got %q", plan.groupByClause)
	}
}

func TestFilterWhereClauseDisabledLibrariesUseItemLevelExclusion(t *testing.T) {
	fromClause, whereClause, _, earlyEmpty := filterWhereClauseForSource(
		BrowseFilters{DisabledLibraryIDs: []int{9}},
		"media_items mi",
		"",
	)
	if earlyEmpty {
		t.Fatal("disabled-library scope must not be treated as empty")
	}

	assertDisabledLibraryItemScope(t, whereClause, "media_item_libraries", "content_id", "mi.content_id")
	if strings.Contains(fromClause, "JOIN media_item_libraries") {
		t.Fatalf("disabled-only facet query must not fan out through a membership JOIN; got FROM %s %s", fromClause, whereClause)
	}
}

func TestFilterWhereClauseDisabledLibrariesUseScopeMembershipTable(t *testing.T) {
	fromClause, whereClause, _, earlyEmpty := filterWhereClauseForSource(
		BrowseFilters{DisabledLibraryIDs: []int{9}},
		"episodes e JOIN media_items mi ON mi.content_id = e.content_id",
		"episode",
	)
	if earlyEmpty {
		t.Fatal("disabled-library scope must not be treated as empty")
	}

	assertDisabledLibraryItemScope(t, whereClause, "episode_libraries", "episode_id", "mi.content_id")
	if strings.Contains(fromClause, "JOIN episode_libraries") {
		t.Fatalf("disabled-only episode facet query must not fan out through a membership JOIN; got FROM %s %s", fromClause, whereClause)
	}
}

func assertDisabledLibraryItemScope(t *testing.T, sql, table, keyColumn, itemExpr string) {
	t.Helper()
	positive := "EXISTS (SELECT 1 FROM " + table
	negative := "NOT EXISTS (SELECT 1 FROM " + table
	correlation := "." + keyColumn + " = " + itemExpr

	if !strings.Contains(sql, positive) {
		t.Fatalf("disabled-only scope must still require positive membership; got:\n%s", sql)
	}
	if !strings.Contains(sql, negative) {
		t.Fatalf("disabled membership must be rejected with NOT EXISTS; got:\n%s", sql)
	}
	if !strings.Contains(sql, correlation) {
		t.Fatalf("library predicates must correlate %s to %s; got:\n%s", keyColumn, itemExpr, sql)
	}
	if strings.Contains(sql, "NOT (mil.media_folder_id = ANY(") {
		t.Fatalf("row-local disabled-library predicate leaks dual-membership items; got:\n%s", sql)
	}
}
