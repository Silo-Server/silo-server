package handlers

import (
	"strings"
	"testing"
)

func TestBuildPostCatalogLibraryScopeDisabledOnlyUsesItemLevelExclusion(t *testing.T) {
	conditions, args, nextArgIdx, earlyEmpty := buildPostCatalogLibraryScope(0, nil, []int{9}, 3)
	if earlyEmpty {
		t.Fatal("disabled-only scope must not be treated as empty")
	}
	sql := strings.Join(conditions, " AND ")
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil_scope_any") {
		t.Fatalf("disabled-only scope must require positive membership; got %s", sql)
	}
	if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil_scope_out") {
		t.Fatalf("disabled membership must be rejected with NOT EXISTS; got %s", sql)
	}
	if strings.Contains(sql, "NOT (mil.media_folder_id = ANY(") {
		t.Fatalf("row-local disabled-library predicate leaks dual-membership items; got %s", sql)
	}
	if len(args) != 1 || nextArgIdx != 4 {
		t.Fatalf("args = %v, nextArgIdx = %d; want one disabled-list arg and 4", args, nextArgIdx)
	}
}

func TestBuildPostCatalogLibraryScopeCombinesRequestedAndAllowedLibraries(t *testing.T) {
	conditions, args, _, earlyEmpty := buildPostCatalogLibraryScope(7, []int{1, 2}, []int{9}, 1)
	if earlyEmpty {
		t.Fatal("non-empty allowlist must not be treated as empty")
	}
	sql := strings.Join(conditions, " AND ")
	if got := strings.Count(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil_scope_in"); got != 1 {
		t.Fatalf("requested library and allowlist must share one positive membership EXISTS; got %d in %s", got, sql)
	}
	for _, want := range []string{
		"mil_scope_in.media_folder_id = $1",
		"mil_scope_in.media_folder_id = ANY($2)",
		"mil_scope_out.media_folder_id = ANY($3)",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("library scope missing %q: %s", want, sql)
		}
	}
	if len(args) != 3 {
		t.Fatalf("args = %v, want requested, allowed, and disabled scopes", args)
	}
}

func TestBuildPostCatalogLibraryScopeEmptyAllowedLibrariesIsEmpty(t *testing.T) {
	_, _, _, earlyEmpty := buildPostCatalogLibraryScope(0, []int{}, nil, 1)
	if !earlyEmpty {
		t.Fatal("empty non-nil allowlist must produce an empty result")
	}
}
