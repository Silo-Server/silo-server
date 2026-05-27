package catalog

import (
	"strings"
	"testing"
)

func TestBuildSortClause_LastAirDateUsesNullsLast(t *testing.T) {
	// Migration 103 denormalized the aired-episode aggregate onto
	// media_items.last_air_date_at, replacing the per-row correlated
	// MAX(e.air_date) subquery and the sort-time derived-table JOIN
	// (audit 2026-05-01 §2.1 hot path #1).
	plan, err := NewQueryBuilder("mi").BuildSortPlan(QuerySort{
		Field: "last_air_date",
		Order: "desc",
	})
	if err != nil {
		t.Fatalf("BuildSortPlan returned error: %v", err)
	}
	if len(plan.Args) != 0 {
		t.Fatalf("expected no args, got %v", plan.Args)
	}
	if len(plan.Joins) != 0 {
		t.Fatalf("expected no joins (denormalized column), got %v", plan.Joins)
	}
	if !strings.Contains(plan.OrderBy, "mi.last_air_date_at") {
		t.Fatalf("expected last_air_date sort to read mi.last_air_date_at, got %q", plan.OrderBy)
	}
	if !strings.Contains(plan.OrderBy, "NULLS LAST") {
		t.Fatalf("expected last_air_date sort to push undated items last, got %q", plan.OrderBy)
	}
	if !strings.Contains(plan.OrderBy, "LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) ASC") {
		t.Fatalf("expected title tie-breaker, got %q", plan.OrderBy)
	}
}

func TestBuildSortClause_ReleaseDateUsesNullsLast(t *testing.T) {
	clause, args, err := NewQueryBuilder("mi").BuildSortClause(QuerySort{
		Field: "release_date",
		Order: "desc",
	})
	if err != nil {
		t.Fatalf("BuildSortClause returned error: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("expected no args, got %v", args)
	}
	if !strings.Contains(clause, "COALESCE(mi.release_date::text, NULLIF(BTRIM(mi.first_air_date), '')) DESC") {
		t.Fatalf("expected release_date sort clause, got %q", clause)
	}
	if !strings.Contains(clause, "NULLS LAST") {
		t.Fatalf("expected release_date sort to push undated items last, got %q", clause)
	}
}

func TestBuildSortClause_ReleaseDateUsesEpisodeAirDateForEpisodeScope(t *testing.T) {
	clause, args, err := NewQueryBuilder("mi").
		WithMediaScope("episode").
		BuildSortClause(QuerySort{
			Field: "release_date",
			Order: "desc",
		})
	if err != nil {
		t.Fatalf("BuildSortClause returned error: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("expected no args, got %v", args)
	}
	if !strings.Contains(clause, "mi.episode_air_date DESC") {
		t.Fatalf("expected episode release_date sort to use raw episode_air_date, got %q", clause)
	}
	if !strings.Contains(clause, "NULLS LAST") {
		t.Fatalf("expected episode release_date sort to push undated items last, got %q", clause)
	}
}

func TestBuildSortClause_AddedAtUsesScopedFirstSeenAt(t *testing.T) {
	plan, err := NewQueryBuilder("mi").
		WithLibraryScope([]int{3, 7}).
		BuildSortPlan(QuerySort{Field: "added_at", Order: "desc"})
	if err != nil {
		t.Fatalf("BuildSortPlan returned error: %v", err)
	}
	if len(plan.Args) != 2 || plan.Args[0] != 3 || plan.Args[1] != 7 {
		t.Fatalf("expected scoped library args [3 7], got %v", plan.Args)
	}
	if len(plan.Joins) != 1 {
		t.Fatalf("expected one join, got %v", plan.Joins)
	}
	if !strings.Contains(plan.Joins[0], "SELECT mil.content_id, MIN(mil.first_seen_at) AS added_at") {
		t.Fatalf("expected aggregated first_seen_at join, got %q", plan.Joins[0])
	}
	if !strings.Contains(plan.Joins[0], "mil.media_folder_id IN ($1, $2)") {
		t.Fatalf("expected scoped library filter, got %q", plan.Joins[0])
	}
	if !strings.Contains(plan.OrderBy, "sort_added.added_at DESC NULLS LAST") {
		t.Fatalf("expected joined added_at ordering, got %q", plan.OrderBy)
	}
}

func TestBuildSortClause_AddedAtFallsBackToCreatedAtWithoutLibraryScope(t *testing.T) {
	plan, err := NewQueryBuilder("mi").BuildSortPlan(QuerySort{Field: "added_at", Order: "desc"})
	if err != nil {
		t.Fatalf("BuildSortPlan returned error: %v", err)
	}
	if len(plan.Args) != 0 {
		t.Fatalf("expected no args, got %v", plan.Args)
	}
	if len(plan.Joins) != 0 {
		t.Fatalf("expected no joins without library scope, got %v", plan.Joins)
	}
	if !strings.Contains(plan.OrderBy, "mi.created_at DESC") {
		t.Fatalf("expected created_at fallback without library scope, got %q", plan.OrderBy)
	}
}

func TestBuildSortClause_ContentRatingUsesRankedOrdering(t *testing.T) {
	clause, _, err := NewQueryBuilder("mi").BuildSortClause(QuerySort{
		Field: "content_rating",
		Order: "asc",
	})
	if err != nil {
		t.Fatalf("BuildSortClause returned error: %v", err)
	}
	if !strings.Contains(clause, "WHEN UPPER(NULLIF(BTRIM(mi.content_rating), '')) = 'TV-14' THEN 2") {
		t.Fatalf("expected maturity rank CASE expression, got %q", clause)
	}
	if !strings.Contains(clause, "LOWER(COALESCE(NULLIF(BTRIM(mi.content_rating), ''), '~~~~')) ASC") {
		t.Fatalf("expected normalized label ordering, got %q", clause)
	}
}

func TestBuildSortClause_ResolutionUsesRankedFileQualities(t *testing.T) {
	plan, err := NewQueryBuilder("mi").BuildSortPlan(QuerySort{
		Field: "resolution",
		Order: "desc",
	})
	if err != nil {
		t.Fatalf("BuildSortPlan returned error: %v", err)
	}
	if len(plan.Joins) != 1 {
		t.Fatalf("expected one join, got %v", plan.Joins)
	}
	if !strings.Contains(plan.Joins[0], "WHEN '2160P' THEN 4") || !strings.Contains(plan.Joins[0], "WHEN '4320P' THEN 5") {
		t.Fatalf("expected resolution ranking CASE expression, got %q", plan.Joins[0])
	}
	if !strings.Contains(plan.Joins[0], "GROUP BY mf.content_id") {
		t.Fatalf("expected grouped file stats join, got %q", plan.Joins[0])
	}
	if !strings.Contains(plan.OrderBy, "sort_files.max_resolution_rank DESC NULLS LAST") {
		t.Fatalf("expected sparse resolution sort to push missing items last, got %q", plan.OrderBy)
	}
}

func TestBuildSortPlan_FileSortsUseScopedLibraryPlaceholdersAfterExistingArgs(t *testing.T) {
	plan, err := NewQueryBuilder("mi").
		WithLibraryScope([]int{6}).
		WithArgIdx(4).
		BuildSortPlan(QuerySort{Field: "resolution", Order: "desc"})
	if err != nil {
		t.Fatalf("BuildSortPlan returned error: %v", err)
	}
	if len(plan.Args) != 1 || plan.Args[0] != 6 {
		t.Fatalf("expected scoped library arg [6], got %v", plan.Args)
	}
	if len(plan.Joins) != 1 {
		t.Fatalf("expected one join, got %v", plan.Joins)
	}
	if !strings.Contains(plan.Joins[0], "mf.media_folder_id IN ($4)") {
		t.Fatalf("expected offset placeholder in file-sort join, got %q", plan.Joins[0])
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
	if !strings.Contains(plan.Joins[0], "mil.media_folder_id IN ($4)") {
		t.Fatalf("expected offset placeholder in added_at join, got %q", plan.Joins[0])
	}
}

func TestBuildSortPlan_AddedAtUsesEpisodeLibraryMembershipForEpisodeScope(t *testing.T) {
	plan, err := NewQueryBuilder("mi").
		WithMediaScope("episode").
		WithLibraryScope([]int{6}).
		BuildSortPlan(QuerySort{Field: "added_at", Order: "desc"})
	if err != nil {
		t.Fatalf("BuildSortPlan returned error: %v", err)
	}
	if len(plan.Joins) != 1 {
		t.Fatalf("expected one join, got %v", plan.Joins)
	}
	if !strings.Contains(plan.Joins[0], "FROM episode_libraries el") {
		t.Fatalf("expected episode added_at join to use episode_libraries, got %q", plan.Joins[0])
	}
	if !strings.Contains(plan.Joins[0], "GROUP BY el.episode_id") {
		t.Fatalf("expected episode added_at join to group by episode_id, got %q", plan.Joins[0])
	}
	if !strings.Contains(plan.Joins[0], "sort_added.content_id = mi.content_id") {
		t.Fatalf("expected episode added_at join to match episode content_id, got %q", plan.Joins[0])
	}
}

func TestBuildSortPlan_FileSortUsesEpisodeIDsForEpisodeScope(t *testing.T) {
	plan, err := NewQueryBuilder("mi").
		WithMediaScope("episode").
		BuildSortPlan(QuerySort{Field: "resolution", Order: "desc"})
	if err != nil {
		t.Fatalf("BuildSortPlan returned error: %v", err)
	}
	if len(plan.Joins) != 1 {
		t.Fatalf("expected one join, got %v", plan.Joins)
	}
	if !strings.Contains(plan.Joins[0], "SELECT mf.episode_id AS content_id") {
		t.Fatalf("expected episode file sort join to group by episode_id, got %q", plan.Joins[0])
	}
	if !strings.Contains(plan.Joins[0], "GROUP BY mf.episode_id") {
		t.Fatalf("expected episode file sort join to aggregate by episode_id, got %q", plan.Joins[0])
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
			expectJoin: []string{"FROM user_watch_progress uwp", "uwp.completed = FALSE", "uwp.position_seconds::double precision / NULLIF(uwp.duration_seconds, 0)"},
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

func TestBuild_ResolutionNormalizes4KAndScopesMediaFiles(t *testing.T) {
	clause, args, err := NewQueryBuilder("mi").
		WithLibraryScope([]int{3}).
		Build(QueryDefinition{
			Match: "all",
			Groups: []QueryGroup{
				{
					Match: "all",
					Rules: []QueryRule{
						{Field: "resolution", Op: "is", Value: "4K"},
					},
				},
			},
		})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(args) != 2 || args[0] != 3 || args[1] != "2160p" {
		t.Fatalf("expected args [3 2160p], got %v", args)
	}
	if !strings.Contains(clause, "WHEN '4k' THEN '2160p'") || !strings.Contains(clause, "WHEN 'uhd' THEN '2160p'") {
		t.Fatalf("expected normalized resolution CASE expression, got %q", clause)
	}
	if !strings.Contains(clause, "mf.media_folder_id IN ($1)") {
		t.Fatalf("expected scoped media-file clause, got %q", clause)
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

func TestBuild_GroupAllCombinesHDRAndDolbyVisionOnSameFile(t *testing.T) {
	clause, args, err := NewQueryBuilder("mi").Build(QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{
			{
				Match: "all",
				Rules: []QueryRule{
					{Field: "hdr", Op: "is", Value: true},
					{Field: "dolby_vision", Op: "is", Value: true},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(args) != 1 || args[0] != true {
		t.Fatalf("expected args [true], got %v", args)
	}
	if strings.Count(clause, "FROM media_files mf") != 1 {
		t.Fatalf("expected one shared media_files EXISTS clause, got %q", clause)
	}
	if !strings.Contains(clause, "mf.hdr = $1") || !strings.Contains(clause, "vt->>'dolby_vision'") {
		t.Fatalf("expected HDR and Dolby Vision predicates in shared clause, got %q", clause)
	}
}

func TestBuild_AuthorClauseUsesPersonKindAuthor(t *testing.T) {
	clause, args, err := NewQueryBuilder("mi").Build(QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{
			{Match: "all", Rules: []QueryRule{{Field: "author", Op: "is", Value: "Brandon Sanderson"}}},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(args) != 1 || args[0] != "Brandon Sanderson" {
		t.Fatalf("expected author arg, got %v", args)
	}
	// PersonKindAuthor == 7. We compare against the integer constant so an
	// accidental kind drift breaks the test rather than silently hitting
	// the wrong table partition.
	if !strings.Contains(clause, "ip.kind = 7") {
		t.Fatalf("expected ip.kind = 7 (PersonKindAuthor) in clause, got %q", clause)
	}
}

func TestBuild_NarratorClauseUsesPersonKindNarrator(t *testing.T) {
	clause, _, err := NewQueryBuilder("mi").Build(QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{
			{Match: "all", Rules: []QueryRule{{Field: "narrator", Op: "is", Value: "Michael Kramer"}}},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if !strings.Contains(clause, "ip.kind = 8") {
		t.Fatalf("expected ip.kind = 8 (PersonKindNarrator) in clause, got %q", clause)
	}
}

func TestBuild_SeriesClauseJoinsAudiobookSeries(t *testing.T) {
	clause, args, err := NewQueryBuilder("mi").Build(QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{
			{Match: "all", Rules: []QueryRule{{Field: "series", Op: "is", Value: "Mistborn"}}},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(args) != 1 || args[0] != "Mistborn" {
		t.Fatalf("expected series arg, got %v", args)
	}
	if !strings.Contains(clause, "FROM audiobook_series s") {
		t.Fatalf("expected audiobook_series join in series clause, got %q", clause)
	}
	// Both sides trimmed + case-folded so user-typed input matches stored
	// values written by the scanner regardless of incidental whitespace.
	if !strings.Contains(clause, "LOWER(BTRIM(s.series_name))") || !strings.Contains(clause, "LOWER(BTRIM($1))") {
		t.Fatalf("expected case+whitespace-insensitive series match, got %q", clause)
	}
}

func TestBuildSortPlan_AuthorJoinsLateralOnPersonKindAuthor(t *testing.T) {
	plan, err := NewQueryBuilder("mi").BuildSortPlan(QuerySort{Field: "author", Order: "asc"})
	if err != nil {
		t.Fatalf("BuildSortPlan(author) returned error: %v", err)
	}
	if len(plan.Joins) != 1 {
		t.Fatalf("expected exactly one join, got %d: %v", len(plan.Joins), plan.Joins)
	}
	if !strings.Contains(plan.Joins[0], "LEFT JOIN LATERAL") || !strings.Contains(plan.Joins[0], "ip.kind = 7") {
		t.Fatalf("expected LATERAL on item_people with kind=7, got %q", plan.Joins[0])
	}
	if !strings.Contains(plan.OrderBy, "sort_author.name") {
		t.Fatalf("expected ORDER BY on sort_author.name, got %q", plan.OrderBy)
	}
}

func TestBuildSortPlan_NarratorJoinsLateralOnPersonKindNarrator(t *testing.T) {
	plan, err := NewQueryBuilder("mi").BuildSortPlan(QuerySort{Field: "narrator", Order: "asc"})
	if err != nil {
		t.Fatalf("BuildSortPlan(narrator) returned error: %v", err)
	}
	if len(plan.Joins) != 1 || !strings.Contains(plan.Joins[0], "ip.kind = 8") {
		t.Fatalf("expected LATERAL on item_people with kind=8, got %v", plan.Joins)
	}
	if !strings.Contains(plan.OrderBy, "sort_narrator.name") {
		t.Fatalf("expected ORDER BY on sort_narrator.name, got %q", plan.OrderBy)
	}
}

func TestBuildSortPlan_SeriesOrdersByNameThenIndex(t *testing.T) {
	plan, err := NewQueryBuilder("mi").BuildSortPlan(QuerySort{Field: "series", Order: "asc"})
	if err != nil {
		t.Fatalf("BuildSortPlan(series) returned error: %v", err)
	}
	if len(plan.Joins) != 1 || !strings.Contains(plan.Joins[0], "audiobook_series sort_series") {
		t.Fatalf("expected LEFT JOIN audiobook_series, got %v", plan.Joins)
	}
	// Series name primary, series_index secondary; both nulls last so
	// books without a series_index still appear under their series.
	if !strings.Contains(plan.OrderBy, "sort_series.series_name ASC NULLS LAST") ||
		!strings.Contains(plan.OrderBy, "sort_series.series_index ASC NULLS LAST") {
		t.Fatalf("expected name+index ordering with NULLS LAST, got %q", plan.OrderBy)
	}
}

func TestBuild_SeriesClauseNegationWrapsInNOT(t *testing.T) {
	clause, _, err := NewQueryBuilder("mi").Build(QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{
			{Match: "all", Rules: []QueryRule{{Field: "series", Op: "is_not", Value: "Mistborn"}}},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(clause), "WHERE")), "NOT (") &&
		!strings.Contains(clause, "NOT (") {
		t.Fatalf("expected is_not to wrap the EXISTS in NOT(...), got %q", clause)
	}
}

func TestBuild_PersonClauseMatchesByResolvedPersonID(t *testing.T) {
	clause, args, err := NewQueryBuilder("mi").Build(QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{
			{
				Match: "all",
				Rules: []QueryRule{
					{Field: "actor", Op: "is", Value: "Sigourney Weaver"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(args) != 1 || args[0] != "Sigourney Weaver" {
		t.Fatalf("expected actor arg, got %v", args)
	}
	if !strings.Contains(clause, "ip.person_id IN (") || !strings.Contains(clause, "FROM people p") {
		t.Fatalf("expected person id resolution subquery, got %q", clause)
	}
	if strings.Contains(clause, "JOIN people") {
		t.Fatalf("expected no join against people table in person clause, got %q", clause)
	}
}

func TestQueryBuilder_AudioLanguageFilter_UsesGeneratedColumn(t *testing.T) {
	// Migration 104 added a STORED text[] generated column for audio
	// language codes. The audio_language filter should use GIN-indexed
	// array containment instead of unnesting JSONB per row
	// (audit 2026-05-01 §2.5b).
	qb := NewQueryBuilder("mi")
	clause, err := qb.buildAudioLanguageClause(QueryRule{
		Field: "audio_language", Op: "is", Value: "ja",
	})
	if err != nil {
		t.Fatalf("buildAudioLanguageClause error: %v", err)
	}
	if !strings.Contains(clause, "audio_language_codes @>") {
		t.Fatalf("expected GIN containment on audio_language_codes; got %s", clause)
	}
	if strings.Contains(clause, "jsonb_array_elements(COALESCE(mf.audio_tracks") {
		t.Fatalf("audio_language filter must not unnest audio_tracks JSONB anymore; got %s", clause)
	}
}

func TestQueryBuilder_SubtitleLanguageFilter_UsesGeneratedColumn(t *testing.T) {
	qb := NewQueryBuilder("mi")
	clause, err := qb.buildSubtitleLanguageClause(QueryRule{
		Field: "subtitle_language", Op: "is", Value: "en",
	})
	if err != nil {
		t.Fatalf("buildSubtitleLanguageClause error: %v", err)
	}
	if !strings.Contains(clause, "subtitle_language_codes @>") {
		t.Fatalf("expected GIN containment on subtitle_language_codes; got %s", clause)
	}
	// Option A: external_subtitles still need a JSONB unnest because the
	// generated column only covers embedded subtitle_tracks.
	if strings.Contains(clause, "jsonb_array_elements(COALESCE(mf.subtitle_tracks") {
		t.Fatalf("subtitle_language filter must not unnest subtitle_tracks JSONB anymore; got %s", clause)
	}
}

func TestQueryBuilder_SameFileAudioLanguagePredicate_UsesGeneratedColumn(t *testing.T) {
	qb := NewQueryBuilder("mi")
	predicate, err := qb.buildTechnicalMediaFilePredicate(QueryRule{
		Field: "audio_language", Op: "is", Value: "ja",
	}, "mf")
	if err != nil {
		t.Fatalf("buildTechnicalMediaFilePredicate error: %v", err)
	}
	if !strings.Contains(predicate, "audio_language_codes @>") {
		t.Fatalf("expected GIN containment on audio_language_codes; got %s", predicate)
	}
	if strings.Contains(predicate, "jsonb_array_elements(COALESCE(mf.audio_tracks") {
		t.Fatalf("same-file audio_language predicate must not unnest audio_tracks JSONB anymore; got %s", predicate)
	}
}

func TestQueryBuilder_SameFileSubtitleLanguagePredicate_UsesGeneratedColumn(t *testing.T) {
	qb := NewQueryBuilder("mi")
	predicate, err := qb.buildTechnicalMediaFilePredicate(QueryRule{
		Field: "subtitle_language", Op: "is", Value: "en",
	}, "mf")
	if err != nil {
		t.Fatalf("buildTechnicalMediaFilePredicate error: %v", err)
	}
	if !strings.Contains(predicate, "subtitle_language_codes @>") {
		t.Fatalf("expected GIN containment on subtitle_language_codes; got %s", predicate)
	}
	if strings.Contains(predicate, "jsonb_array_elements(COALESCE(mf.subtitle_tracks") {
		t.Fatalf("same-file subtitle_language predicate must not unnest subtitle_tracks JSONB anymore; got %s", predicate)
	}
}

// TestLastWatchedExpr_UsesDerivedTableJoin asserts that lastWatchedExpr no
// longer emits the two correlated MAX subqueries (audit 2026-05-01 §3.1 Pattern
// B). The expression should reference the user_last_watched derived-table CTE
// the executor injects when the builder requires it.
func TestLastWatchedExpr_UsesDerivedTableJoin(t *testing.T) {
	qb := NewQueryBuilder("mi").WithUserScope(1, "p1")
	expr := qb.lastWatchedExpr("last_watched")
	if strings.Contains(expr, "SELECT MAX(uwh.watched_at)") {
		t.Fatalf("last_watched must not use correlated MAX subquery; got %s", expr)
	}
	if !strings.Contains(expr, "uhist.last_watched") {
		t.Fatalf("expected derived-table join alias uhist.last_watched; got %s", expr)
	}
}

// TestQueryBuilder_LastWatched_RequiresCTE asserts that referencing
// last_watched flips the builder flag the executor checks before injecting the
// user_last_watched CTE.
func TestQueryBuilder_LastWatched_RequiresCTE(t *testing.T) {
	qb := NewQueryBuilder("mi").WithUserScope(1, "p1")
	_ = qb.lastWatchedExpr("last_watched")
	if !qb.RequiresUserHistoryCTE() {
		t.Fatalf("after lastWatchedExpr, builder should require CTE")
	}
}
