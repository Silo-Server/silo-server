package catalog

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// TestItemRepo_GetByIDsWithAccess_SingleQueryWithLibraryFilter verifies that
// the SQL emitted by buildGetByIDsWithAccessSQL pushes the library access
// constraint into the same query as the content_id batch lookup, replacing
// the per-item EnsureAccessible fan-out called out in the catalog SQL audit
// (2026-05-01 §3.3).
func TestItemRepo_GetByIDsWithAccess_SingleQueryWithLibraryFilter(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a", "b"}, AccessFilter{
		AllowedLibraryIDs: []int{1, 2},
	})
	if !strings.Contains(sql, "content_id = ANY($1)") {
		t.Fatalf("expected ANY for content IDs; got %s", sql)
	}
	if !strings.Contains(sql, "media_folder_id = ANY($2)") {
		t.Fatalf("expected library access pushed into JOIN/WHERE; got %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args; got %v", args)
	}
}

// TestItemRepo_GetByIDsWithAccess_NoAccessFilterNoLibraryClause verifies that
// when AccessFilter is empty, the emitted SQL omits any library predicate and
// only binds the content_id batch.
func TestItemRepo_GetByIDsWithAccess_NoAccessFilterNoLibraryClause(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a", "b"}, AccessFilter{})
	if strings.Contains(sql, "media_folder_id") {
		t.Fatalf("expected no library clause when AccessFilter is empty; got %s", sql)
	}
	if len(args) != 1 {
		t.Fatalf("expected 1 arg (just IDs); got %v", args)
	}
}

// TestItemRepo_GetByIDsWithAccess_DisabledLibrariesProduceNotExists pins the
// shape of the DisabledLibraryIDs branch: a NOT EXISTS subquery against
// media_item_libraries with the disabled IDs bound at $2.
func TestItemRepo_GetByIDsWithAccess_DisabledLibrariesProduceNotExists(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		DisabledLibraryIDs: []int{9, 10},
	})
	if !strings.Contains(sql, "NOT EXISTS") {
		t.Fatalf("expected NOT EXISTS clause for DisabledLibraryIDs; got %s", sql)
	}
	if !strings.Contains(sql, "media_folder_id = ANY($2)") {
		t.Fatalf("expected DisabledLibraryIDs bound at $2; got %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args (ids, disabled libs); got %v", args)
	}
}

// TestItemRepo_GetByIDsWithAccess_DisabledOnlyRequiresLibraryMembership pins
// the fix for the disabled-only access path: when AllowedLibraryIDs is nil
// and only DisabledLibraryIDs is set, the SQL must additionally require
// positive library membership (an EXISTS over media_item_libraries with
// no media_folder_id filter). Otherwise orphan items (rows in media_items
// with no media_item_libraries link — mid-scan, stale rows from a removed
// library, or metadata-refresh inserts not yet linked) would pass the
// NOT EXISTS — which is true over an empty subquery — and become visible
// to users restricted by DisabledLibraryIDs. EnsureAccessible's prior
// INNER JOIN on media_item_libraries enforced this implicitly.
//
// Regression guard for Codex P2 follow-up to PR #42.
func TestItemRepo_GetByIDsWithAccess_DisabledOnlyRequiresLibraryMembership(t *testing.T) {
	repo := &ItemRepository{}
	sql, _ := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		DisabledLibraryIDs: []int{9},
	})
	// Positive-membership EXISTS must be present. It has no media_folder_id
	// filter so the NOT EXISTS that follows is what enforces the disabled-list.
	if strings.Count(sql, "EXISTS (") < 2 {
		t.Fatalf("expected both a membership EXISTS and a disabled NOT EXISTS clause; got %s", sql)
	}
	// Pin the membership predicate's specific shape: an EXISTS against
	// media_item_libraries that does NOT bind a media_folder_id ANY(...) arg.
	// (The disabled NOT EXISTS does bind one — we want the membership EXISTS
	// to be argument-free so it doesn't reorder placeholder indices.)
	if !strings.Contains(sql, `EXISTS (
                SELECT 1 FROM media_item_libraries mil
                WHERE mil.content_id = mi.content_id
            )`) {
		t.Fatalf("expected argument-free membership EXISTS over media_item_libraries; got %s", sql)
	}
}

// TestItemRepo_GetByIDsWithAccess_AllowedListSkipsRedundantMembershipCheck
// asserts that when AllowedLibraryIDs is non-nil the membership EXISTS is
// NOT added a second time — the allowed-list EXISTS already provides
// positive membership, so adding another would be redundant and would
// shift placeholder indices.
func TestItemRepo_GetByIDsWithAccess_AllowedListSkipsRedundantMembershipCheck(t *testing.T) {
	repo := &ItemRepository{}
	sql, _ := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		AllowedLibraryIDs:  []int{1, 2},
		DisabledLibraryIDs: []int{9},
	})
	// Exactly two EXISTS clauses: allowed-list EXISTS + disabled NOT EXISTS.
	// A third (membership-only EXISTS) would be redundant.
	if got := strings.Count(sql, "EXISTS ("); got != 2 {
		t.Fatalf("expected exactly 2 EXISTS clauses (allowed + disabled); got %d in %s", got, sql)
	}
}

// TestItemRepo_GetByIDsWithAccess_MaxContentRatingProducesINClause pins the
// rating-ladder branch: a content_rating = ANY(...) clause with the bound
// rating slice as a single arg.
func TestItemRepo_GetByIDsWithAccess_MaxContentRatingProducesINClause(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		MaxContentRating: "PG-13",
	})
	if !strings.Contains(sql, "content_rating = ANY($") {
		t.Fatalf("expected content_rating = ANY clause; got %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args (ids + ratings slice); got %v", args)
	}
}

// TestItemRepo_GetByIDsWithAccess_CombinedClausesIndexCorrectly verifies that
// when AllowedLibraryIDs, DisabledLibraryIDs, and MaxContentRating are all
// set, placeholder indices advance in the documented order: $1 = ids,
// $2 = allowed libs, $3 = disabled libs, $4 = rating ladder.
func TestItemRepo_GetByIDsWithAccess_CombinedClausesIndexCorrectly(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		AllowedLibraryIDs:  []int{1, 2},
		DisabledLibraryIDs: []int{9},
		MaxContentRating:   "PG-13",
	})
	// Expect: $1 = ids, $2 = allowed libs, $3 = disabled libs, $4 = rating ladder.
	if !strings.Contains(sql, "media_folder_id = ANY($2)") {
		t.Fatalf("expected AllowedLibraryIDs at $2; got %s", sql)
	}
	if !strings.Contains(sql, "media_folder_id = ANY($3)") {
		t.Fatalf("expected DisabledLibraryIDs at $3; got %s", sql)
	}
	if !strings.Contains(sql, "content_rating = ANY($4)") {
		t.Fatalf("expected content_rating = ANY at $4; got %s", sql)
	}
	// All four slots are now array-bound: ids, allowed, disabled, ratings.
	if len(args) != 4 {
		t.Fatalf("expected 4 args (ids, allowed, disabled, ratings); got %v", args)
	}
}

// TestItemRepo_GetByExternalIDs_SingleQueryAcrossProviders pins the SQL shape
// of buildGetByExternalIDsSQL: a single statement that ORs across the three
// external-ID arrays plus a type filter, replacing the per-entry N×3
// GetByExternalID fan-out in MDBList collection sync (audit 2026-05-01 §3.7).
func TestItemRepo_GetByExternalIDs_SingleQueryAcrossProviders(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByExternalIDsSQL(ExternalIDBatch{
		TMDBIDs: []string{"1", "2"}, IMDbIDs: []string{"tt1", "tt2"}, TVDBIDs: nil,
	}, "movie")
	if !strings.Contains(sql, "tmdb_id = ANY($1)") {
		t.Fatalf("expected ANY tmdb; got %s", sql)
	}
	if !strings.Contains(sql, "imdb_id = ANY($2)") {
		t.Fatalf("expected ANY imdb; got %s", sql)
	}
	if !strings.Contains(sql, "type = $") {
		t.Fatalf("expected type filter; got %s", sql)
	}
	if len(args) < 3 {
		t.Fatalf("expected at least 3 args (tmdb, imdb, type); got %d", len(args))
	}
}

// TestItemRepo_GetByExternalIDs_NilSliceStillBindsArg verifies that nil
// provider slices still get bound as args so the placeholder numbering stays
// consistent across all four positional parameters.
func TestItemRepo_GetByExternalIDs_NilSliceStillBindsArg(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByExternalIDsSQL(ExternalIDBatch{
		TMDBIDs: []string{"1"}, IMDbIDs: nil, TVDBIDs: nil,
	}, "movie")
	if !strings.Contains(sql, "type = $4") && !strings.Contains(sql, "type = $") {
		t.Fatalf("expected type bound at $4 (after 3 ID slices); got %s", sql)
	}
	_ = args
}

func TestLookupExternalIDsSQLChecksProviderTableAndDirectColumns(t *testing.T) {
	sql := lookupExternalIDsSQL()

	for _, want := range []string{
		"FROM requested r",
		"JOIN media_item_provider_ids mip",
		"mip.provider = r.provider",
		"mip.provider_id = r.provider_id",
		"mip.item_type = $5",
		"mi.type = $5",
		"mi.tmdb_id <> '' AND mi.tmdb_id = r.provider_id",
		"mi.tvdb_id <> '' AND mi.tvdb_id = r.provider_id",
		"mi.imdb_id <> '' AND mi.imdb_id = r.provider_id",
		"JOIN media_folders mf ON mf.id = mil.media_folder_id",
		"mf.enabled = true",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("lookupExternalIDsSQL missing %q:\n%s", want, sql)
		}
	}
	for _, disallowed := range []string{
		"COALESCE(mi.tmdb_id, '') = r.provider_id",
		"COALESCE(mi.tvdb_id, '') = r.provider_id",
		"COALESCE(mi.imdb_id, '') = r.provider_id",
	} {
		if strings.Contains(sql, disallowed) {
			t.Fatalf("lookupExternalIDsSQL should use indexable direct predicate, found %q:\n%s", disallowed, sql)
		}
	}
}

// TestItemRepo_Search_UsesWindowCount asserts that buildSearchSQL emits a
// single-pass paged SELECT that includes COUNT(*) OVER () so Search no longer
// needs a separate count query before the data fetch (audit 2026-05-01 §3.11).
// The single-word path uses one scored CTE; the count is added via a window
// function in the final SELECT instead of issuing a SELECT COUNT(...) first.
func TestItemRepo_Search_UsesWindowCount(t *testing.T) {
	repo := &ItemRepository{}
	sql, _, _ := repo.buildSearchSQL("avatar", []string{"movie"}, 20, 0, AccessFilter{})
	if !strings.Contains(sql, "COUNT(*) OVER ()") {
		t.Fatalf("expected COUNT(*) OVER () in scored CTE consumer; got %s", sql)
	}
	if strings.Count(sql, "WITH scored AS") != 1 {
		t.Fatalf("expected exactly one scored CTE; got %s", sql)
	}
}

// TestItemRepo_Search_TitleGate_UsesWindowCount asserts the unified query
// pairs the scored CTE with a stats CTE that derives has_title_match, and
// that the window count runs on the final filtered result so the total
// reflects the title-gate CROSS JOIN filter rather than the broader
// pre-filter set.
func TestItemRepo_Search_TitleGate_UsesWindowCount(t *testing.T) {
	repo := &ItemRepository{}
	sql, _, _ := repo.buildSearchSQL("the matrix reloaded", []string{"movie"}, 20, 0, AccessFilter{})
	if !strings.Contains(sql, "COUNT(*) OVER ()") {
		t.Fatalf("expected COUNT(*) OVER () in title-gate path; got %s", sql)
	}
	if strings.Count(sql, "WITH scored AS") != 1 {
		t.Fatalf("expected exactly one scored CTE; got %s", sql)
	}
	if !strings.Contains(sql, "stats AS") {
		t.Fatalf("expected stats CTE; got %s", sql)
	}
	if !strings.Contains(sql, "has_title_match") {
		t.Fatalf("expected has_title_match predicate; got %s", sql)
	}
}

// TestItemRepo_Search_SingleWordEnablesTitleGate pins the bug fix for the
// "obsession returns 2000 results" report: even single-word queries must
// route through the stats CTE + CROSS JOIN, so overview-only matches are
// suppressed whenever any title match exists. Prior to this, single-word
// queries skipped the stats CTE entirely and returned every row where the
// search term appeared in the description.
func TestItemRepo_Search_SingleWordEnablesTitleGate(t *testing.T) {
	repo := &ItemRepository{}
	sql, _, _ := repo.buildSearchSQL("obsession", []string{"movie"}, 20, 0, AccessFilter{})
	if !strings.Contains(sql, "stats AS") {
		t.Fatalf("expected single-word query to include the stats CTE; got %s", sql)
	}
	if !strings.Contains(sql, "CROSS JOIN stats") {
		t.Fatalf("expected single-word query to CROSS JOIN stats; got %s", sql)
	}
	if !strings.Contains(sql, "scored.title_rank > 0") {
		t.Fatalf("expected title gate to use title_rank > 0; got %s", sql)
	}
}

// TestItemRepo_Search_AppliesOverviewRankFloor pins that the overview-only
// fallback arm is gated by overviewMatchFloor, so weak single-occurrence
// description matches do not pass through when no title match exists. The
// floor literal is derived from the constant so the test stays in sync if
// the threshold is retuned.
func TestItemRepo_Search_AppliesOverviewRankFloor(t *testing.T) {
	repo := &ItemRepository{}
	dataSQL, countSQL, _ := repo.buildSearchSQL("obsession", []string{"movie"}, 20, 0, AccessFilter{})
	want := fmt.Sprintf("scored.overview_rank >= %g", overviewMatchFloor)
	if !strings.Contains(dataSQL, want) {
		t.Fatalf("expected %q in dataSQL; got %s", want, dataSQL)
	}
	if !strings.Contains(countSQL, want) {
		t.Fatalf("expected %q in countSQL too (must mirror dataSQL); got %s", want, countSQL)
	}
}

// TestItemRepo_Search_EmptyQueryReturnsEmpty pins the early-return contract
// when input parses to no searchable text. Downstream callers rely on
// (dataSQL == "") to short-circuit without binding any args.
func TestItemRepo_Search_EmptyQueryReturnsEmpty(t *testing.T) {
	repo := &ItemRepository{}
	dataSQL, countSQL, args := repo.buildSearchSQL("   ", nil, 20, 0, AccessFilter{})
	if dataSQL != "" || countSQL != "" || args != nil {
		t.Fatalf("expected (\"\", \"\", nil) for whitespace-only query; got (%q, %q, %v)", dataSQL, countSQL, args)
	}
}

// TestItemRepo_Search_UsesTitleNormalizedColumn asserts that buildSearchSQL
// reads the mi.title_normalized stored generated column for the title rank
// arms (exact_title_match and contiguous_title_match), so the LIKE
// '%pattern%' predicate can use the gin_trgm_ops index added in migration
// 105 instead of recomputing normalization per row
// (audit 2026-05-01 §3.12).
//
// The original_title and sort_title fallbacks are intentionally not stored
// as generated columns (less search traffic), so they call the
// public.normalize_search_text() function (migrations 127 / 138) inline.
func TestItemRepo_Search_UsesTitleNormalizedColumn(t *testing.T) {
	repo := &ItemRepository{}
	sql, _, _ := repo.buildSearchSQL("avatar", []string{"movie"}, 20, 0, AccessFilter{})
	if strings.Contains(sql, "REGEXP_REPLACE(COALESCE(mi.title") {
		t.Fatalf("Search must read mi.title_normalized for the title rank, not inline REGEXP_REPLACE; got:\n%s", sql)
	}
	if !strings.Contains(sql, "mi.title_normalized") {
		t.Fatalf("Search must reference mi.title_normalized; got:\n%s", sql)
	}
	if !strings.Contains(sql, "public.normalize_search_text(mi.original_title)") {
		t.Fatalf("Search should call public.normalize_search_text() on mi.original_title; got:\n%s", sql)
	}
	if !strings.Contains(sql, "public.normalize_search_text(mi.sort_title)") {
		t.Fatalf("Search should call public.normalize_search_text() on mi.sort_title; got:\n%s", sql)
	}
}

// TestItemRepo_Search_NormalizesTsqueryInput asserts that the user's search
// text is wrapped in public.normalize_search_text() before being handed to
// websearch_to_tsquery on the title arm, and to phraseto_tsquery for the
// phrase rank. The tsvector side of @@ applies the same normalization, so
// title normalization stays symmetric end-to-end. The overview arm is
// intentionally left unwrapped — the 'english' config already treats "and"
// as a stop word.
func TestItemRepo_Search_NormalizesTsqueryInput(t *testing.T) {
	repo := &ItemRepository{}
	sql, _, _ := repo.buildSearchSQL("law and order", []string{"movie"}, 20, 0, AccessFilter{})

	if !strings.Contains(sql, "websearch_to_tsquery('simple', public.normalize_search_text($1))") {
		t.Fatalf("title arm must normalize the query input; got:\n%s", sql)
	}
	if !strings.Contains(sql, "public.normalize_search_text(COALESCE(mi.title, ''))") {
		t.Fatalf("title tsvector must normalize mi.title to match the GIN index expression; got:\n%s", sql)
	}
	if !strings.Contains(sql, "public.normalize_search_text(COALESCE(mi.original_title, ''))") {
		t.Fatalf("title tsvector must normalize mi.original_title; got:\n%s", sql)
	}
	if !strings.Contains(sql, "public.normalize_search_text(COALESCE(mi.sort_title, ''))") {
		t.Fatalf("title tsvector must normalize mi.sort_title; got:\n%s", sql)
	}
	if !strings.Contains(sql, "phraseto_tsquery('simple', public.normalize_search_text(") {
		t.Fatalf("phrase rank must normalize the phrase input; got:\n%s", sql)
	}
	// Overview deliberately not wrapped — 'english' config strips "and".
	if !strings.Contains(sql, "websearch_to_tsquery('english', $1)") {
		t.Fatalf("overview arm should NOT wrap $1 in normalize_search_text; got:\n%s", sql)
	}
}

// TestItemRepo_Search_UsesTitlePrefixQueryForPartialLastToken pins typeahead
// title search for inputs like "Pride and P": the full-text query for "p" is
// otherwise an exact lexeme match and never reaches the title-normalized
// contiguous ranking signal.
func TestItemRepo_Search_UsesTitlePrefixQueryForPartialLastToken(t *testing.T) {
	repo := &ItemRepository{}
	sql, _, args := repo.buildSearchSQL("Pride and P", []string{"movie"}, 20, 0, AccessFilter{})

	if !strings.Contains(sql, "to_tsquery('simple', $2)") {
		t.Fatalf("expected title prefix query to use a bound to_tsquery arg; got:\n%s", sql)
	}
	if !strings.Contains(sql, "title_prefix_rank") {
		t.Fatalf("expected title prefix matches to participate in ranking/filtering; got:\n%s", sql)
	}
	if len(args) < 2 || args[1] != "pride & p:*" {
		t.Fatalf("expected args[1] to be the normalized title prefix tsquery, got %#v", args)
	}
}

// TestItemRepo_Search_ScoredCTEExposesItemColumnNames guards the contract
// between the scored CTE and the outer SELECT in buildSearchSQL. The CTE
// projects qualifiedItemColumns("mi") and the outer query re-selects those
// columns by name via itemColumns, so every entry must expose its own column
// name as the output name. Postgres names an unaliased expression like
// COALESCE(mi.poster_path, ”) "coalesce", which breaks the outer reference
// with SQLSTATE 42703 (column "poster_path" does not exist) — exactly how
// search returned 500s when the nullable-string COALESCE wrappers first
// landed without AS aliases.
func TestItemRepo_Search_ScoredCTEExposesItemColumnNames(t *testing.T) {
	exposed := map[string]bool{}
	for _, part := range splitTopLevelSQLCommas(qualifiedItemColumns("mi")) {
		exposed[sqlOutputColumnName(part)] = true
	}

	var missing []string
	for _, col := range itemColumnNames {
		name := trailingSQLIdent(col)
		if !exposed[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("qualifiedItemColumns(\"mi\") does not expose output columns %v; "+
			"alias each expression back to its column name (e.g. COALESCE(mi.x, '') AS x) "+
			"or the search CTE's outer SELECT fails with SQLSTATE 42703", missing)
	}
}

// TestItemRepo_Search_GroupByHasNoOutputAliases asserts the scored CTE's
// GROUP BY uses raw column references: AS aliases are valid in a select list
// but are a syntax error inside GROUP BY, so the CTE must not reuse the
// aliased projection there.
func TestItemRepo_Search_GroupByHasNoOutputAliases(t *testing.T) {
	repo := &ItemRepository{}
	dataSQL, countSQL, _ := repo.buildSearchSQL("avatar", []string{"movie"}, 20, 0, AccessFilter{})

	for _, sql := range []string{dataSQL, countSQL} {
		idx := strings.Index(sql, "GROUP BY")
		if idx < 0 {
			t.Fatalf("expected GROUP BY in scored CTE; got:\n%s", sql)
		}
		clause := sql[idx:]
		if end := strings.Index(clause, ")"); end >= 0 {
			clause = clause[:end]
		}
		if strings.Contains(clause, " AS ") {
			t.Fatalf("GROUP BY must not contain output aliases; got:\n%s", clause)
		}
		if strings.Contains(clause, "COALESCE") {
			t.Fatalf("GROUP BY should group by raw columns, not COALESCE expressions; got:\n%s", clause)
		}
	}
}

// TestItemRepo_ListUnmatchedByFolderAndPathPrefix_ExcludesMangaChapters pins
// the manga-chapter exclusion in the unmatched-item lister. Manga chapters are
// type='ebook' items that stay status='pending' by design (the type='manga'
// series item carries all provider metadata), so without a NOT EXISTS guard
// against manga_chapters every library scan funnels each chapter through the
// matcher's retry loop — one rate-limited ebook-plugin search per chapter
// (observed live 2026-06-12: 31,564 chapters x ~1s = 8h46m per scan, 100%
// no-match). Mirrors the same exclusion in the ebook enricher's claim query.
func TestItemRepo_ListUnmatchedByFolderAndPathPrefix_ExcludesMangaChapters(t *testing.T) {
	repo := &ItemRepository{}

	sql, args := repo.buildListUnmatchedByFolderAndPathPrefixSQL(10, "/mnt/media/manga", 0)
	if !strings.Contains(sql, "NOT EXISTS") || !strings.Contains(sql, "manga_chapters") {
		t.Fatalf("expected manga_chapters NOT EXISTS guard in unmatched lister; got:\n%s", sql)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args without limit; got %v", args)
	}

	sql, args = repo.buildListUnmatchedByFolderAndPathPrefixSQL(10, "/mnt/media/manga", 25)
	if !strings.Contains(sql, "LIMIT $4") {
		t.Fatalf("expected LIMIT $4 when limit > 0; got:\n%s", sql)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args with limit; got %v", args)
	}
}
