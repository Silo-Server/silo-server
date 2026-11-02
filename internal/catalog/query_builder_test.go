package catalog

import (
	"strings"
	"testing"
)

func TestBuild_AddedAtInLastAcceptsYears(t *testing.T) {
	clause, args, err := NewQueryBuilder("mi").Build(QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{
			Match: "all",
			Rules: []QueryRule{{Field: "added_at", Op: "in_last", Value: "1y"}},
		}},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("expected no args, got %v", args)
	}
	if !strings.Contains(clause, "mi.created_at >= NOW() - INTERVAL '1 years'") {
		t.Fatalf("expected added_at in_last to use created_at and years interval, got %q", clause)
	}
}

func TestParseDurationRejectsMalformedInput(t *testing.T) {
	if _, err := parseDuration("1'; SELECT 1; --d"); err == nil {
		t.Fatal("expected malformed duration to be rejected")
	}
	if got, err := parseDuration("2Y"); err != nil || got != "2 years" {
		t.Fatalf("parseDuration(2Y) = %q, %v; want 2 years", got, err)
	}
}

func TestBuildSortPlan_AddedAtUsesScopedLibraryPlaceholdersAfterExistingArgs(t *testing.T) {
	plan, err := NewQueryBuilder("mi").
		WithLibraryScope([]int{6}).
		WithArgIdx(4).
		BuildSortPlan(QuerySort{Field: "added_at", Order: "desc"})
	if err != nil {
		t.Fatalf("BuildSortPlan returned error: %v", err)
	}
	if len(plan.Args) != 1 || plan.Args[0] != 6 {
		t.Fatalf("expected scoped library arg [6], got %v", plan.Args)
	}
	if len(plan.Joins) != 1 {
		t.Fatalf("expected one join, got %v", plan.Joins)
	}
	if !strings.Contains(plan.Joins[0], "JOIN media_item_libraries sort_added ON sort_added.content_id = mi.content_id AND sort_added.media_folder_id = $4") {
		t.Fatalf("expected offset placeholder in single-library added_at join, got %q", plan.Joins[0])
	}
	if !strings.Contains(plan.OrderBy, "sort_added.first_seen_at DESC,") {
		t.Fatalf("expected the single-library sort to follow first_seen_at, got %q", plan.OrderBy)
	}

	plan, err = NewQueryBuilder("mi").
		WithLibraryScope([]int{6, 7}).
		WithArgIdx(4).
		BuildSortPlan(QuerySort{Field: "added_at", Order: "desc"})
	if err != nil {
		t.Fatalf("BuildSortPlan returned error: %v", err)
	}
	if len(plan.Joins) != 1 || !strings.Contains(plan.Joins[0], "mil.media_folder_id IN ($4, $5)") {
		t.Fatalf("expected offset placeholders in multi-library added_at join, got %v", plan.Joins)
	}
}

func TestBuildSortClause_PersonalizedComputedSorts(t *testing.T) {
	builder := NewQueryBuilder("mi").WithUserScope(42, "profile-1")
	tests := []struct {
		field      string
		expectJoin []string
		expectExpr []string
		expectArgN int
	}{
		{
			field:      "progress",
			expectJoin: []string{"FROM user_watch_progress uwp", "uwp.position_seconds > 0", "uwp.position_seconds::double precision / NULLIF(uwp.duration_seconds, 0)"},
			expectExpr: []string{"sort_progress.progress_ratio"},
			expectArgN: 2,
		},
		{
			field:      "date_viewed",
			expectJoin: []string{"FROM user_watch_history uwh", "uwh.completed = TRUE", "FROM user_watch_progress uwp", "uwp.completed = TRUE"},
			expectExpr: []string{"sort_history_viewed.viewed_at", "sort_progress_viewed.viewed_at"},
			expectArgN: 2,
		},
		{
			field:      "plays",
			expectJoin: []string{"COUNT(*) AS play_count", "FROM user_watch_history uwh", "FROM user_watch_progress uwp", "completed_play_count"},
			expectExpr: []string{"sort_history_plays.play_count", "sort_progress_plays.completed_play_count"},
			expectArgN: 2,
		},
	}

	for _, tt := range tests {
		plan, err := builder.BuildSortPlan(QuerySort{Field: tt.field, Order: "desc"})
		if err != nil {
			t.Fatalf("%s: BuildSortPlan returned error: %v", tt.field, err)
		}
		if len(plan.Args) != tt.expectArgN {
			t.Fatalf("%s: expected %d args, got %v", tt.field, tt.expectArgN, plan.Args)
		}
		if plan.Args[0] != 42 || plan.Args[1] != "profile-1" {
			t.Fatalf("%s: expected user scope args, got %v", tt.field, plan.Args)
		}
		joined := strings.Join(plan.Joins, "\n")
		for _, fragment := range tt.expectJoin {
			if !strings.Contains(joined, fragment) {
				t.Fatalf("%s: expected joins to contain %q, got %q", tt.field, fragment, joined)
			}
		}
		for _, fragment := range tt.expectExpr {
			if !strings.Contains(plan.OrderBy, fragment) {
				t.Fatalf("%s: expected order by to contain %q, got %q", tt.field, fragment, plan.OrderBy)
			}
		}
		if !strings.Contains(plan.OrderBy, "LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) ASC") {
			t.Fatalf("%s: expected title tie-breaker, got %q", tt.field, plan.OrderBy)
		}
	}
}

func TestBuild_WatchedRollupAndRowTypeBranching(t *testing.T) {
	buildWatched := func(t *testing.T, scope string) (string, []any) {
		t.Helper()
		clause, args, err := NewQueryBuilder("mi").
			WithMediaScope(scope).
			WithUserScope(42, "profile-1").
			Build(QueryDefinition{
				MediaScope: scope,
				Match:      "all",
				Groups: []QueryGroup{{
					Match: "all",
					Rules: []QueryRule{{Field: "watched", Op: "is", Value: true}},
				}},
			})
		if err != nil {
			t.Fatalf("scope %q: Build returned error: %v", scope, err)
		}
		// Exactly one (user_id, profile_id) pair is bound regardless of branch,
		// shared via $1/$2 — a stray $3 would mean the helpers drifted from the
		// shared-placeholder contract.
		if len(args) != 2 || args[0] != 42 || args[1] != "profile-1" {
			t.Fatalf("scope %q: expected user scope args, got %v", scope, args)
		}
		if !strings.Contains(clause, "$1") || !strings.Contains(clause, "$2") {
			t.Fatalf("scope %q: expected $1 and $2 placeholders, got %q", scope, clause)
		}
		if strings.Contains(clause, "$3") {
			t.Fatalf("scope %q: completion clause must reuse $1/$2 only, got %q", scope, clause)
		}
		return clause, args
	}

	tests := []struct {
		name        string
		scope       string
		expectSQL   []string
		unexpectSQL []string
	}{
		{
			name:  "series scope rolls up over available episodes",
			scope: "series",
			expectSQL: []string{
				"FROM episodes",
				"episodes.series_id = mi.content_id",
				"FROM episode_libraries el",
				"el.episode_id = episodes.content_id",
				"AND NOT (",                               // no available child is incomplete
				"uwp.media_item_id = episodes.content_id", // leaf keyed to the episode
			},
			unexpectSQL: []string{"CASE", "mi.type =", "episodes.season_id"},
		},
		{
			name:  "movie scope uses the leaf only",
			scope: "movie",
			expectSQL: []string{
				"FROM user_watch_progress uwp",
				"uwp.media_item_id = mi.content_id",
				"FROM user_watch_history uwh",
			},
			unexpectSQL: []string{"CASE", "FROM episodes", "FROM ebook_reader_progress erp"},
		},
		{
			name:  "unscoped query branches on row type",
			scope: "",
			expectSQL: []string{
				"CASE",
				"WHEN mi.type = 'series' THEN",
				"WHEN mi.type = 'season' THEN",
				"WHEN mi.type = 'ebook' THEN",
				"episodes.series_id = mi.content_id",
				"episodes.season_id = mi.content_id",
				"FROM ebook_reader_progress erp",
				"erp.content_id = mi.content_id",
				"el.episode_id = episodes.content_id",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clause, _ := buildWatched(t, tt.scope)
			for _, fragment := range tt.expectSQL {
				if !strings.Contains(clause, fragment) {
					t.Fatalf("expected clause to contain %q, got %q", fragment, clause)
				}
			}
			for _, fragment := range tt.unexpectSQL {
				if strings.Contains(clause, fragment) {
					t.Fatalf("did not expect clause to contain %q, got %q", fragment, clause)
				}
			}
		})
	}
}

func TestBuild_GroupAllCombinesPositiveTechnicalFiltersOnSameFile(t *testing.T) {
	clause, args, err := NewQueryBuilder("mi").Build(QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{
			{
				Match: "all",
				Rules: []QueryRule{
					{Field: "resolution", Op: "is", Value: "2160p"},
					{Field: "hdr", Op: "is", Value: true},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(args) != 2 || args[0] != "2160p" || args[1] != true {
		t.Fatalf("expected args [2160p true], got %v", args)
	}
	if strings.Count(clause, "FROM media_files mf") != 1 {
		t.Fatalf("expected one shared media_files EXISTS clause, got %q", clause)
	}
	if !strings.Contains(clause, "mf.hdr = $2") {
		t.Fatalf("expected hdr predicate in shared clause, got %q", clause)
	}
}

func TestBuildParenthesizesResultForSafeANDComposition(t *testing.T) {
	// Build's callers (query_executor.go, handlers/catalog.go) AND library and
	// access-filter constraints onto the returned expression. A match-any
	// group emits a top-level OR, so the result must come back parenthesized —
	// otherwise SQL precedence lets the first OR arm bypass every constraint
	// appended afterward.
	tests := []struct {
		name string
		def  QueryDefinition
		want string
	}{
		{
			name: "single match-any group",
			def: QueryDefinition{
				Match: "all",
				Groups: []QueryGroup{{
					Match: "any",
					Rules: []QueryRule{
						{Field: "genre", Op: "contains", Value: "Action"},
						{Field: "genre", Op: "contains", Value: "Adventure"},
					},
				}},
			},
			want: "(mi.genres @> ARRAY[$1]::text[] OR mi.genres @> ARRAY[$2]::text[])",
		},
		{
			name: "match-any top level",
			def: QueryDefinition{
				Match: "any",
				Groups: []QueryGroup{
					{Match: "all", Rules: []QueryRule{{Field: "genre", Op: "contains", Value: "Action"}}},
					{Match: "all", Rules: []QueryRule{{Field: "genre", Op: "contains", Value: "Adventure"}}},
				},
			},
			want: "((mi.genres @> ARRAY[$1]::text[]) OR (mi.genres @> ARRAY[$2]::text[]))",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clause, _, err := NewQueryBuilder("mi").Build(tt.def)
			if err != nil {
				t.Fatalf("Build returned error: %v", err)
			}
			if clause != tt.want {
				t.Fatalf("Build clause = %q, want %q", clause, tt.want)
			}
		})
	}
}
