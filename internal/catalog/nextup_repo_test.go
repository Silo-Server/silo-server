package catalog

import (
	"strings"
	"testing"
)

func TestBuildListNextUpQuery_PrefersRecentCompletedOverOlderPartialProgress(t *testing.T) {
	t.Parallel()

	query, args := buildListNextUpQuery(NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
	}, 20)

	expectedFragments := []string{
		"eligible_series AS (",
		"uwp_ip.completed = FALSE",
		"e_ip.series_id = ce.series_id",
		"uwp_ip.updated_at > ce.updated_at",
		"FROM user_history_hidden_items hhi",
		"uwp.updated_at <= hhi.hidden_before",
		"ORDER BY e.series_id, uwp.updated_at DESC, e.season_number DESC, e.episode_number DESC",
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

func TestBuildListNextUpQuery_EnableResumableSkipsSeriesSuppressionCTE(t *testing.T) {
	t.Parallel()

	query, _ := buildListNextUpQuery(NextUpQuery{
		UserID:          7,
		ProfileID:       "profile-1",
		EnableResumable: true,
	}, 20)

	if strings.Contains(query, "eligible_series AS (") {
		t.Fatalf("expected resumable query to skip eligible_series suppression CTE, got:\n%s", query)
	}
	if !strings.Contains(query, "FROM completed_episodes es") {
		t.Fatalf("expected resumable query to read directly from completed_episodes, got:\n%s", query)
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
