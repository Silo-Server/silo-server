package catalog

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBuildListNextUpQuery_PrefersRecentCompletedOverOlderPartialProgress(t *testing.T) {
	t.Parallel()

	query, args := buildListNextUpQuery(NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
	}, 20, nil)

	expectedFragments := []string{
		"eligible_series AS (",
		"uwp_ip.position_seconds > 0",
		"e_ip.series_id = ce.series_id",
		"uwp_ip.updated_at > ce.updated_at",
		"FROM user_history_hidden_items hhi",
		"uwp.updated_at <= hhi.hidden_before",
		"AND (uwp2.completed = TRUE OR uwp2.position_seconds > 0)",
		"ORDER BY uwp.updated_at DESC, uwp.media_item_id DESC",
		"LIMIT $3",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected query to contain %q, got:\n%s", fragment, query)
		}
	}

	if strings.Contains(query, "uwp.media_item_id = ANY") {
		t.Fatalf("query must not cap completed progress before deriving series anchors, got:\n%s", query)
	}

	if len(args) != 3 {
		t.Fatalf("expected default arg count, got %d", len(args))
	}
}

func TestBuildListNextUpQuery_GlobalBoundsAnchorScan(t *testing.T) {
	t.Parallel()

	q := NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
	}
	query, args := buildListNextUpQuery(q, 20, nil)

	expectedFragments := []string{
		"WITH RECURSIVE",
		"(uwp.updated_at, uwp.media_item_id) < (w.updated_at, w.media_item_id)",
		"NOT (e.series_id = ANY(w.seen))",
		fmt.Sprintf("w.n < %d", nextUpAnchorMaxSeries),
		"frontier AS (",
		"LEFT JOIN (",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected global anchor walk to contain %q, got:\n%s", fragment, query)
		}
	}
	// Seed step, recursive step, and the anchor lateral attached to each.
	if got := strings.Count(query, "FROM user_history_hidden_items hhi"); got != 4 {
		t.Fatalf("expected hidden-items filter in every walk step and anchor, got %d occurrences:\n%s", got, query)
	}
	seed := strings.SplitN(query, "UNION ALL", 2)[0]
	if strings.Contains(seed, "(uwp.updated_at, uwp.media_item_id) < ($") {
		t.Fatalf("first batch seed must not contain a continuation cursor, got:\n%s", seed)
	}
	if strings.Contains(seed, "e.series_id = ANY($") {
		t.Fatalf("first batch seed must not contain a seen-series exclusion, got:\n%s", seed)
	}
	if len(args) != 3 {
		t.Fatalf("expected first batch args only, got %v", args)
	}

	cursorAt := time.Now().UTC()
	cursor := &nextUpWalkCursor{
		updatedAt:   cursorAt,
		mediaItemID: "episode-96",
		seen:        []string{"series-1", "series-2"},
	}
	continuedQuery, continuedArgs := buildListNextUpQuery(q, 7, cursor)
	continuedSeed := strings.SplitN(continuedQuery, "UNION ALL", 2)[0]
	continuedFragments := []string{
		"(uwp.updated_at, uwp.media_item_id) < ($4, $5)",
		"NOT (e.series_id = ANY($6))",
		"$6::text[] || pick.series_id AS seen",
		"frontier AS (",
		"LEFT JOIN (",
	}
	for _, fragment := range continuedFragments {
		if !strings.Contains(continuedQuery, fragment) {
			t.Fatalf("expected continued query to contain %q, got:\n%s", fragment, continuedQuery)
		}
	}
	if !strings.Contains(continuedSeed, "(uwp.updated_at, uwp.media_item_id) < ($4, $5)") ||
		!strings.Contains(continuedSeed, "NOT (e.series_id = ANY($6))") {
		t.Fatalf("continued batch seed must contain cursor and seen-series predicates, got:\n%s", continuedSeed)
	}
	if len(continuedArgs) != 6 || continuedArgs[3] != cursorAt || continuedArgs[4] != cursor.mediaItemID {
		t.Fatalf("unexpected continuation args: %v", continuedArgs)
	}
	seenArg, ok := continuedArgs[5].([]string)
	if !ok || len(seenArg) != len(cursor.seen) || seenArg[0] != cursor.seen[0] || seenArg[1] != cursor.seen[1] {
		t.Fatalf("expected cursor seen array verbatim, got %#v", continuedArgs[5])
	}
}

func TestBuildListNextUpQuery_GlobalDateCutoffAppliesToEveryWalkStep(t *testing.T) {
	t.Parallel()

	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	query, args := buildListNextUpQuery(NextUpQuery{
		UserID:     7,
		ProfileID:  "profile-1",
		DateCutoff: &cutoff,
	}, 20, nil)

	if got := strings.Count(query, "AND uwp.updated_at >= $4"); got != 2 {
		t.Fatalf("expected date cutoff in seed and recursive step, got %d occurrences:\n%s", got, query)
	}
	if len(args) != 4 || args[3] != cutoff {
		t.Fatalf("expected one shared date-cutoff arg, got %v", args)
	}
}

func TestBuildListNextUpQuery_SeriesScopedKeepsUnboundedAnchor(t *testing.T) {
	t.Parallel()

	q := NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
		SeriesID:  "series-42",
	}
	query, args := buildListNextUpQuery(q, 20, nil)

	// The show-detail tile must anchor on the series' last completed episode
	// no matter how long ago it was watched: the recency bound would make a
	// long-idle series' tile disappear.
	if strings.Contains(query, "recent_completed AS (") {
		t.Fatalf("series-scoped query must not bound the anchor scan, got:\n%s", query)
	}
	if strings.Contains(query, "WITH RECURSIVE") {
		t.Fatalf("series-scoped query must keep its non-recursive SQL shape, got:\n%s", query)
	}
	if !strings.Contains(query, "AND e.series_id = $4") {
		t.Fatalf("series-scoped query must filter by series_id, got:\n%s", query)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args with SeriesID, got %d (%v)", len(args), args)
	}

	queryWithCursor, argsWithCursor := buildListNextUpQuery(q, 20, &nextUpWalkCursor{
		updatedAt:   time.Now().UTC(),
		mediaItemID: "ignored-episode",
		seen:        []string{"ignored-series"},
	})
	if queryWithCursor != query || fmt.Sprint(argsWithCursor) != fmt.Sprint(args) {
		t.Fatalf("series-scoped SQL and args must be byte-identical when a walk cursor is supplied")
	}
}

func TestBuildListNextUpQuery_GlobalAnchorOrdersByEpisodeNotMediaItemID(t *testing.T) {
	t.Parallel()

	// media_item_id is unique, so a season/episode tie-break appended behind it
	// can never be reached. Choosing the series and choosing which of its
	// episodes anchors the rail have to be separate orderings.
	query, _ := buildListNextUpQuery(NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
	}, 20, nil)

	if strings.Contains(query, `ORDER BY uwp.updated_at DESC, uwp.media_item_id DESC,
					e.season_number DESC, e.episode_number DESC`) {
		t.Fatalf("season/episode must not sit behind the unique media_item_id tie-break, got:\n%s", query)
	}
	if got := strings.Count(query, "ORDER BY e_a.season_number DESC, e_a.episode_number DESC"); got != 2 {
		t.Fatalf("expected episode-ordered anchor in seed and recursive step, got %d occurrences:\n%s", got, query)
	}
	if got := strings.Count(query, "AND uwp_a.updated_at = pick.updated_at"); got != 2 {
		t.Fatalf("expected anchor pinned to the series' newest completed timestamp, got %d:\n%s", got, query)
	}
}

func TestBuildListResumableFirstEpisodesQuery_GlobalKeepsCompletedSeriesGate(t *testing.T) {
	t.Parallel()

	// Global /Shows/NextUp?enableResumable=true: the resumable branch must
	// still skip series the user has completed any episode of, otherwise it
	// would double-fire alongside buildListNextUpQuery's main path.
	query, args := buildListResumableFirstEpisodesQuery(NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
	}, []string{"ep-1", "ep-2"})

	if !strings.Contains(query, "uwp_c.completed = TRUE") {
		t.Fatalf("global query must keep the completed-series gate, got:\n%s", query)
	}
	if strings.Contains(query, "AND e.series_id =") {
		t.Fatalf("global query must not have a series filter, got:\n%s", query)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args without SeriesID, got %d (%v)", len(args), args)
	}
}

func TestBuildListResumableFirstEpisodesQuery_SeriesScopedDropsCompletedGate(t *testing.T) {
	t.Parallel()

	// /Shows/Upcoming for a single series: the completed-series gate must
	// be dropped so a user who finished S01E01 and is mid-watching S01E02
	// still gets E02 back. Without the gate-drop the endpoint silently
	// returns the next aired episode (S01E03) — the Codex P2 finding
	// flagged on PR #64.
	query, args := buildListResumableFirstEpisodesQuery(NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
		SeriesID:  "series-42",
	}, []string{"ep-1", "ep-2"})

	if strings.Contains(query, "uwp_c.completed = TRUE") {
		t.Fatalf("series-scoped query must drop the completed-series gate, got:\n%s", query)
	}
	if !strings.Contains(query, "AND e.series_id = $4") {
		t.Fatalf("series-scoped query must filter by series_id at SQL level, got:\n%s", query)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args with SeriesID, got %d (%v)", len(args), args)
	}
	if got, want := args[3], "series-42"; got != want {
		t.Fatalf("expected SeriesID arg %q, got %v", want, got)
	}
}

func TestBuildListNextUpQuery_DroppedSeriesExcludedFromEveryWalkStep(t *testing.T) {
	t.Parallel()

	dropped := []string{"series-dropped"}
	query, args := buildListNextUpQuery(NextUpQuery{
		UserID:           7,
		ProfileID:        "profile-1",
		droppedSeriesIDs: dropped,
	}, 20, &nextUpWalkCursor{mediaItemID: "m", seen: []string{"s"}})
	if got := strings.Count(query, "AND NOT (e.series_id = ANY($7::text[]))"); got != 2 {
		t.Fatalf("expected dropped exclusion in seed and recursive step, got %d:\n%s", got, query)
	}
	if len(args) != 7 {
		t.Fatalf("expected dropped IDs as the seventh arg, got %v", args)
	}

	withoutDrops, _ := buildListNextUpQuery(NextUpQuery{UserID: 7, ProfileID: "profile-1"}, 20, nil)
	if strings.Contains(withoutDrops, "::text[]))") {
		t.Fatalf("query without drops must not carry the exclusion:\n%s", withoutDrops)
	}

	scoped, _ := buildListNextUpQuery(NextUpQuery{UserID: 7, ProfileID: "profile-1", SeriesID: "series-dropped", droppedSeriesIDs: dropped}, 20, nil)
	if strings.Contains(scoped, "ANY($") {
		t.Fatalf("series-scoped query must not exclude drops:\n%s", scoped)
	}

	resumable, resumableArgs := buildListResumableFirstEpisodesQuery(NextUpQuery{UserID: 7, ProfileID: "profile-1", droppedSeriesIDs: dropped}, []string{"ep"})
	if !strings.Contains(resumable, "AND NOT (e.series_id = ANY($4::text[]))") || len(resumableArgs) != 4 {
		t.Fatalf("global resumable query must exclude drops, got args %v:\n%s", resumableArgs, resumable)
	}
}

func TestSortNextUpByActivity_BreaksTiesByCompletionThenSeries(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	results := []NextUpResult{
		{SeriesID: "c", ActivityAt: at, CompletedAt: at.Add(-2 * time.Hour)},
		{SeriesID: "b", ActivityAt: at, CompletedAt: at.Add(-time.Hour)},
		{SeriesID: "z", ActivityAt: at.Add(-time.Minute), CompletedAt: at.Add(-time.Minute)},
		{SeriesID: "a", ActivityAt: at, CompletedAt: at.Add(-2 * time.Hour)},
		{SeriesID: "y", ActivityAt: at.Add(time.Minute), CompletedAt: at.Add(-48 * time.Hour)},
	}
	sortNextUpByActivity(results)
	got := make([]string, len(results))
	for i, res := range results {
		got[i] = res.SeriesID
	}
	if want := "y b a c z"; strings.Join(got, " ") != want {
		t.Fatalf("order = %q, want %q", strings.Join(got, " "), want)
	}
}

func TestNextUpReleaseScope(t *testing.T) {
	t.Parallel()

	five := 5
	cases := []struct {
		name     string
		q        NextUpQuery
		wantCond string
		wantArgs []interface{}
	}{
		{"unrestricted", NextUpQuery{}, "", nil},
		{"disabled", NextUpQuery{AccessFilter: AccessFilter{DisabledLibraryIDs: []int{3}}},
			" AND NOT (ea.library_id = ANY($9::int[]))", []interface{}{[]int{3}}},
		{"allowed", NextUpQuery{AccessFilter: AccessFilter{AllowedLibraryIDs: []int{1, 2}, DisabledLibraryIDs: []int{2}}},
			" AND ea.library_id = ANY($9::int[])", []interface{}{[]int{1}}},
		{"section library", NextUpQuery{LibraryID: &five}, " AND ea.library_id = ANY($9::int[])", []interface{}{[]int{5}}},
		{"no reachable library", NextUpQuery{LibraryIDs: []int{4}, AccessFilter: AccessFilter{AllowedLibraryIDs: []int{1}}},
			" AND ea.library_id = ANY($9::int[])", []interface{}{[]int{}}},
	}
	for _, tc := range cases {
		cond, args := nextUpReleaseScope(tc.q, 9)
		if cond != tc.wantCond || fmt.Sprint(args) != fmt.Sprint(tc.wantArgs) {
			t.Errorf("%s: got %q %v, want %q %v", tc.name, cond, args, tc.wantCond, tc.wantArgs)
		}
	}
}
