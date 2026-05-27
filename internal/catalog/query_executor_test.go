package catalog

import (
	"strings"
	"testing"
)

func TestBuildLibraryScopeJoin_AllowedLibraries(t *testing.T) {
	whereSQL, args, ok := buildLibraryScopeJoin([]int{3, 7}, nil, 4, "", "mi.content_id")
	if !ok {
		t.Fatal("expected join to be enabled")
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want single slice arg", args)
	}
	if got, ok := args[0].([]int); !ok || len(got) != 2 || got[0] != 3 || got[1] != 7 {
		t.Fatalf("args[0] = %v, want []int{3, 7}", args[0])
	}
	if !strings.Contains(whereSQL, "EXISTS (") {
		t.Fatalf("expected EXISTS semi-join, got %q", whereSQL)
	}
	if !strings.Contains(whereSQL, "FROM media_item_libraries") {
		t.Fatalf("expected reference to media_item_libraries, got %q", whereSQL)
	}
	if !strings.Contains(whereSQL, "media_folder_id = ANY($4)") {
		t.Fatalf("expected allowed library = ANY placeholder, got %q", whereSQL)
	}
}

func TestBuildLibraryScopeJoin_DisabledLibraries(t *testing.T) {
	whereSQL, args, ok := buildLibraryScopeJoin(nil, []int{9}, 2, "", "mi.content_id")
	if !ok {
		t.Fatal("expected join to be enabled")
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want single slice arg", args)
	}
	if got, ok := args[0].([]int); !ok || len(got) != 1 || got[0] != 9 {
		t.Fatalf("args[0] = %v, want []int{9}", args[0])
	}
	if !strings.Contains(whereSQL, "NOT EXISTS (") {
		t.Fatalf("expected NOT EXISTS semi-join, got %q", whereSQL)
	}
	if !strings.Contains(whereSQL, "media_folder_id = ANY($2)") {
		t.Fatalf("expected disabled library = ANY placeholder, got %q", whereSQL)
	}
}

func TestBuildLibraryScopeJoin_AllowedAndDisabledLibraries(t *testing.T) {
	whereSQL, args, ok := buildLibraryScopeJoin([]int{1, 2}, []int{8, 9}, 1, "", "mi.content_id")
	if !ok {
		t.Fatal("expected join to be enabled")
	}
	if len(args) != 2 {
		t.Fatalf("args = %v, want 2 slice args", args)
	}
	if got, ok := args[0].([]int); !ok || len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("args[0] = %v, want []int{1, 2}", args[0])
	}
	if got, ok := args[1].([]int); !ok || len(got) != 2 || got[0] != 8 || got[1] != 9 {
		t.Fatalf("args[1] = %v, want []int{8, 9}", args[1])
	}
	if !strings.Contains(whereSQL, "EXISTS (") {
		t.Fatalf("expected EXISTS semi-join, got %q", whereSQL)
	}
	if !strings.Contains(whereSQL, "NOT EXISTS (") {
		t.Fatalf("expected NOT EXISTS semi-join, got %q", whereSQL)
	}
	if !strings.Contains(whereSQL, "media_folder_id = ANY($1)") {
		t.Fatalf("expected allowed library = ANY filter, got %q", whereSQL)
	}
	if !strings.Contains(whereSQL, "media_folder_id = ANY($2)") {
		t.Fatalf("expected disabled library = ANY filter, got %q", whereSQL)
	}
}

func TestBuildLibraryScopeJoin_NoLibraryScope(t *testing.T) {
	whereSQL, args, ok := buildLibraryScopeJoin(nil, nil, 1, "", "mi.content_id")
	if ok {
		t.Fatal("expected join to be disabled")
	}
	if whereSQL != "" {
		t.Fatalf("whereSQL = %q, want empty", whereSQL)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want empty", args)
	}
}

func TestBuildLibraryScopeJoin_EpisodeScopeUsesEpisodeLibraryMembership(t *testing.T) {
	whereSQL, args, ok := buildLibraryScopeJoin([]int{4}, nil, 2, "episode", "mi.content_id")
	if !ok {
		t.Fatal("expected join to be enabled")
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want single slice arg", args)
	}
	if got, ok := args[0].([]int); !ok || len(got) != 1 || got[0] != 4 {
		t.Fatalf("args[0] = %v, want []int{4}", args[0])
	}
	if !strings.Contains(whereSQL, "FROM episode_libraries") {
		t.Fatalf("expected episode library scope to use episode_libraries, got %q", whereSQL)
	}
	if !strings.Contains(whereSQL, ".episode_id = mi.content_id") {
		t.Fatalf("expected episode library scope to match episode_id to mi.content_id, got %q", whereSQL)
	}
}

// TestBuildLibraryScopeJoin_NoRedundantDistinct asserts the library scope no
// longer wraps the join in a DISTINCT subquery — Audit Pattern D
// (2026-05-01 §3 Pattern D). DISTINCT was load-bearing in the prior shape (an
// item in N allowed libraries produces N PK-distinct rows in
// media_item_libraries), so we replace the JOIN with an EXISTS semi-join that
// uses the (content_id, media_folder_id) PK index directly without fanout.
func TestBuildLibraryScopeJoin_NoRedundantDistinct(t *testing.T) {
	sql, _, _ := buildLibraryScopeJoin([]int{1, 2, 3}, nil, 1, "", "mi.content_id")
	if strings.Contains(sql, "SELECT DISTINCT") {
		t.Fatalf("library scope must not use redundant DISTINCT subquery; got %s", sql)
	}
	if !strings.Contains(sql, "EXISTS") {
		t.Fatalf("library scope should use EXISTS semi-join; got %s", sql)
	}
}

func TestBuildLibraryScopeJoin_WithDisabledLibraries(t *testing.T) {
	sql, _, _ := buildLibraryScopeJoin([]int{1, 2}, []int{9}, 1, "", "mi.content_id")
	if !strings.Contains(sql, "NOT EXISTS") {
		t.Fatalf("expected NOT EXISTS for DisabledLibraryIDs; got %s", sql)
	}
}

// TestBuildLibraryScopeJoin_PreventsContentIDFanout guards against a regression
// to a plain JOIN form that would fanout outer rows for items in multiple
// libraries (the original audit's proposed shape).
func TestBuildLibraryScopeJoin_PreventsContentIDFanout(t *testing.T) {
	sql, _, _ := buildLibraryScopeJoin([]int{1, 2, 3}, nil, 1, "", "mi.content_id")
	if strings.Contains(sql, "JOIN media_item_libraries") {
		t.Fatalf("plain JOIN form would fanout for multi-library items; got %s", sql)
	}
}

func TestEpisodeCatalogBaseRelationForLibraries_UsesEpisodeLibraries(t *testing.T) {
	relation, args, handled := episodeCatalogBaseRelationForLibraries([]int{2, 7}, []int{9}, 3)
	if !handled {
		t.Fatal("expected episode library scope to be handled in the base relation")
	}
	if len(args) != 2 {
		t.Fatalf("args = %v, want 2 slice args", args)
	}
	if got, ok := args[0].([]int); !ok || len(got) != 2 || got[0] != 2 || got[1] != 7 {
		t.Fatalf("args[0] = %v, want []int{2, 7}", args[0])
	}
	if got, ok := args[1].([]int); !ok || len(got) != 1 || got[0] != 9 {
		t.Fatalf("args[1] = %v, want []int{9}", args[1])
	}
	if !strings.Contains(relation, "el.episode_id = e.content_id") {
		t.Fatalf("expected episode library join in relation, got %q", relation)
	}
	if !strings.Contains(relation, "el.media_folder_id = ANY($3)") {
		t.Fatalf("expected allowed library = ANY filter, got %q", relation)
	}
	if !strings.Contains(relation, "NOT (el.media_folder_id = ANY($4))") {
		t.Fatalf("expected disabled library NOT (= ANY) filter, got %q", relation)
	}
	if strings.Contains(relation, "media_files mf") {
		t.Fatalf("expected relation to avoid media_files, got %q", relation)
	}
}

func TestEpisodeCatalogProjectionIncludesSharedCatalogColumns(t *testing.T) {
	sql, _, err := (&QueryExecutor{}).buildPreviewPageSQL(
		QueryDefinition{
			MediaScope: "episode",
			LibraryIDs: []int{2},
			Sort:       QuerySort{Field: "title", Order: "asc"},
		},
		AccessFilter{},
		20,
		0,
		true,
	)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL error: %v", err)
	}
	if !strings.Contains(sql, "COALESCE(si.show_status, '') AS show_status") {
		t.Fatalf("expected episode projection to include show_status, got %s", sql)
	}
	if !strings.Contains(sql, "mi.show_status") {
		t.Fatalf("expected outer catalog select to reference show_status, got %s", sql)
	}
	if !strings.Contains(sql, "LOWER(COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text)) AS sort_key") {
		t.Fatalf("expected episode projection to include sort_key, got %s", sql)
	}
	if !strings.Contains(sql, "ORDER BY mi.sort_key ASC, mi.content_id ASC") {
		t.Fatalf("expected episode title sort to use sort_key, got %s", sql)
	}
}

func TestEpisodeCatalogSingleLibraryAddedAtUsesDirectMembershipJoin(t *testing.T) {
	sql, _, err := (&QueryExecutor{}).buildPreviewPageSQL(
		QueryDefinition{
			MediaScope: "episode",
			LibraryIDs: []int{2},
			Sort:       QuerySort{Field: "added_at", Order: "desc"},
		},
		AccessFilter{},
		20,
		0,
		true,
	)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL error: %v", err)
	}
	if !strings.Contains(sql, "JOIN episode_libraries sort_added") {
		t.Fatalf("expected direct episode_libraries join for single-library added_at sort, got %s", sql)
	}
	if !strings.Contains(sql, "sort_added.media_folder_id = $2") {
		t.Fatalf("expected direct added_at join to bind the single library, got %s", sql)
	}
	if strings.Contains(sql, "GROUP BY el.episode_id") {
		t.Fatalf("single-library added_at sort should avoid aggregate membership join, got %s", sql)
	}
	if !strings.Contains(sql, "ORDER BY sort_added.first_seen_at DESC, mi.sort_key ASC, mi.content_id ASC") {
		t.Fatalf("expected added_at order to use first_seen_at without NULLS LAST, got %s", sql)
	}
}

func TestRebindSQLPlaceholders(t *testing.T) {
	got := rebindSQLPlaceholders("mi.created_at <= $1 AND mi.year >= $2", 3)
	want := "mi.created_at <= $4 AND mi.year >= $5"
	if got != want {
		t.Fatalf("rebindSQLPlaceholders() = %q, want %q", got, want)
	}
}

// TestQueryExecutor_PreviewPage_LastWatched_InjectsUserHistoryCTE asserts that
// when a filter references last_watched, the executor (a) splices in the
// user_last_watched CTE, (b) LEFT JOINs uhist on mi.content_id, and (c) binds
// userID/profileID at the start of the arg list — replacing the previous two
// correlated MAX subqueries (audit 2026-05-01 §3.1 Pattern B).
func TestQueryExecutor_PreviewPage_LastWatched_InjectsUserHistoryCTE(t *testing.T) {
	exec := &QueryExecutor{Scope: "movie", BaseRelationSQL: "media_items mi"}
	def := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{
			Match: "all",
			Rules: []QueryRule{{Field: "last_watched", Op: "in_last", Value: "30d"}},
		}},
	}
	access := AccessFilter{UserID: 42, ProfileID: "p1"}
	sql, args, err := exec.buildPreviewPageSQL(def, access, 20, 0, true)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL error: %v", err)
	}
	if !strings.Contains(sql, "WITH user_last_watched AS (") {
		t.Fatalf("expected user_last_watched CTE; got:\n%s", sql)
	}
	if !strings.Contains(sql, "LEFT JOIN user_last_watched uhist ON uhist.media_item_id = mi.content_id") {
		t.Fatalf("expected LEFT JOIN to user_last_watched; got:\n%s", sql)
	}
	if !strings.Contains(sql, "uhist.last_watched") {
		t.Fatalf("expected reference to uhist.last_watched; got:\n%s", sql)
	}
	if strings.Contains(sql, "SELECT MAX(uwh.watched_at)") {
		t.Fatalf("must not emit correlated MAX subquery anymore; got:\n%s", sql)
	}
	// $1, $2 must be the user/profile bound for the CTE.
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args (CTE userID/profileID); got %v", args)
	}
	if got, ok := args[0].(int); !ok || got != 42 {
		t.Fatalf("args[0] = %v, want userID 42", args[0])
	}
	if got, ok := args[1].(string); !ok || got != "p1" {
		t.Fatalf("args[1] = %v, want profileID \"p1\"", args[1])
	}
}

// TestQueryExecutor_PreviewPage_NoLastWatched_NoCTE asserts that queries
// without last_watched do not pay the CTE cost.
func TestQueryExecutor_PreviewPage_NoLastWatched_NoCTE(t *testing.T) {
	exec := &QueryExecutor{Scope: "movie", BaseRelationSQL: "media_items mi"}
	sql, _, err := exec.buildPreviewPageSQL(QueryDefinition{}, AccessFilter{}, 20, 0, true)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL error: %v", err)
	}
	if strings.Contains(sql, "user_last_watched") {
		t.Fatalf("did not expect user_last_watched CTE for query without last_watched; got:\n%s", sql)
	}
}
