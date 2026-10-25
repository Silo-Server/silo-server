package catalog

import (
	"strings"
	"testing"
)

func TestApplyEpisodeCatalogAccessFilterRejectsAnyDisabledMembership(t *testing.T) {
	var whereParts []string
	var args []any
	argIdx := 2
	applyEpisodeCatalogAccessFilter(
		7,
		AccessFilter{DisabledLibraryIDs: []int{9}},
		&whereParts,
		&args,
		&argIdx,
	)

	where := strings.Join(whereParts, " AND ")
	if !strings.Contains(where, "EXISTS (SELECT 1 FROM episode_libraries el_scope_any") {
		t.Fatalf("optimized episode catalog path must require current episode membership, got %s", where)
	}
	if !strings.Contains(where, "NOT EXISTS (SELECT 1 FROM episode_libraries el_scope_out") {
		t.Fatalf("optimized episode catalog path must reject any disabled membership, got %s", where)
	}
	if !strings.Contains(where, "el_scope_out.media_folder_id = ANY($2)") {
		t.Fatalf("disabled libraries must bind at $2, got %s", where)
	}
	assertEpisodeParentDisabledAccess(t, where, "ece.series_id")
	if strings.Contains(where, "SELECT e_parent.series_id") {
		t.Fatalf("read-model path must use ece.series_id without an episode lookup, got %s", where)
	}
	if len(args) != 3 || argIdx != 5 {
		t.Fatalf("args = %v, argIdx = %d; want the episode disabled list, the series library and disabled list, and 5", args, argIdx)
	}
}

func TestEpisodeCatalogEntryQueryFallsBackForSameFileTechnicalAnd(t *testing.T) {
	group := QueryGroup{
		Match: "all",
		Rules: []QueryRule{
			{Field: "resolution", Op: "is", Value: "2160p"},
			{Field: "audio_language", Op: "is", Value: "en"},
		},
	}

	_, _, _, ok, err := buildEpisodeCatalogEntryGroupWhere(group, 1)
	if err != nil {
		t.Fatalf("buildEpisodeCatalogEntryGroupWhere returned error: %v", err)
	}
	if ok {
		t.Fatal("same-file technical AND should fall back to the generic media_files EXISTS path")
	}
}

// An in_progress filter keeps unknown-duration rows; a progress sort needs a
// computable ratio before falling back.
func TestEpisodeCatalogUserStateCTEDurationGuard(t *testing.T) {
	const guard = "COALESCE(uwp.duration_seconds, 0) > 0"
	for _, tc := range []struct {
		name      string
		rule      QueryRule
		sort      QuerySort
		wantGuard bool
	}{
		{"in_progress filter keeps unknown-duration rows", QueryRule{Field: "in_progress", Op: "is", Value: true}, QuerySort{Field: "title", Order: "asc"}, false},
		{"progress sort requires progress ratio", QueryRule{Field: "genre", Op: "is", Value: "Comedy"}, QuerySort{Field: "progress", Order: "desc"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := QueryDefinition{
				Match:  "all",
				Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{tc.rule}}},
				Sort:   tc.sort,
			}.Normalize()
			plan, ok, err := extractEpisodeCatalogUserStatePlan(def)
			if err != nil {
				t.Fatalf("extractEpisodeCatalogUserStatePlan returned error: %v", err)
			}
			if !ok {
				t.Fatal("expected a user-state plan")
			}
			if sql := episodeCatalogUserStateCTE(plan); strings.Contains(sql, guard) != tc.wantGuard {
				t.Fatalf("CTE contains %q = %v, want %v:\n%s", guard, !tc.wantGuard, tc.wantGuard, sql)
			}
		})
	}
}

func TestExtractEpisodeCatalogUserStatePlanForLastWatched(t *testing.T) {
	def := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{
			Match: "all",
			Rules: []QueryRule{
				{Field: "last_watched", Op: "in_last", Value: "30d"},
				{Field: "genre", Op: "is", Value: "Comedy"},
			},
		}},
		Sort: QuerySort{Field: "title", Order: "asc"},
	}.Normalize()

	plan, ok, err := extractEpisodeCatalogUserStatePlan(def)
	if err != nil {
		t.Fatalf("extractEpisodeCatalogUserStatePlan returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected last_watched filter to use a user-state plan")
	}
	if plan.source != "viewed" || plan.alias != "uv" || !plan.positiveSource {
		t.Fatalf("unexpected user-state plan: %+v", plan)
	}
	if len(plan.userClauses) != 1 || !strings.Contains(plan.userClauses[0], "uv.last_watched") {
		t.Fatalf("unexpected user clauses: %#v", plan.userClauses)
	}
	if len(plan.entryDef.Groups) != 1 || len(plan.entryDef.Groups[0].Rules) != 1 {
		t.Fatalf("expected genre rule to remain in entry def, got %+v", plan.entryDef.Groups)
	}
}

func TestBuildEpisodeCatalogEntryQueryWhereParenthesizesMatchAnyGroup(t *testing.T) {
	// Both episode preview paths append this expression to an AND-joined
	// whereParts list that already carries the library, content-rating, and
	// series-parent-guard constraints. A match-any group emits a top-level OR,
	// so the result must come back parenthesized — otherwise SQL precedence
	// detaches an OR arm from every one of those constraints.
	def := QueryDefinition{
		MediaScope: "episode",
		Match:      "all",
		Groups: []QueryGroup{{
			Match: "any",
			Rules: []QueryRule{
				{Field: "genre", Op: "contains", Value: "Action"},
				{Field: "genre", Op: "contains", Value: "Adventure"},
			},
		}},
	}.Normalize()

	where, args, nextArgIdx, ok, err := buildEpisodeCatalogEntryQueryWhere(def, 5)
	if err != nil {
		t.Fatalf("buildEpisodeCatalogEntryQueryWhere returned error: %v", err)
	}
	if !ok {
		t.Fatal("buildEpisodeCatalogEntryQueryWhere unexpectedly fell back")
	}
	want := "(ece.genres @> ARRAY[$5]::text[] OR ece.genres @> ARRAY[$6]::text[])"
	if where != want {
		t.Fatalf("WHERE = %q, want %q", where, want)
	}
	if nextArgIdx != 7 || len(args) != 2 {
		t.Fatalf("nextArgIdx = %d, len(args) = %d; want 7, 2", nextArgIdx, len(args))
	}
}
