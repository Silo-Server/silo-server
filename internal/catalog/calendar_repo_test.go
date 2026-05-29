package catalog

import (
	"strings"
	"testing"
	"time"
)

func TestCalendarEventsOrderByClause_SortsAirTimeBeforeUntimedItems(t *testing.T) {
	if !strings.Contains(calendarEventsOrderByClause, "air_time ASC NULLS LAST") {
		t.Fatalf("calendar order clause = %q, want air_time ASC NULLS LAST", calendarEventsOrderByClause)
	}
}

func TestBuildListEventsQuery_UsesCTEsForFinalesAndSeasonPremieres(t *testing.T) {
	t.Parallel()

	repo := &CalendarRepository{}
	query, args := repo.buildListEventsQuery(CalendarFilter{
		Start: time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, time.April, 12, 0, 0, 0, 0, time.UTC),
	})

	expectedFragments := []string{
		"WITH filtered_episodes AS (",
		"episode_seasons AS (",
		"season_finales AS (",
		"filtered_seasons AS (",
		"episode_one_with_air_date AS (",
		"LEFT JOIN season_finales sf ON sf.series_id = fe.series_id AND sf.season_number = fe.season_number",
		"LEFT JOIN episode_one_with_air_date e1",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected query to contain %q, got:\n%s", fragment, query)
		}
	}

	unexpectedFragments := []string{
		"(SELECT MAX(",
		"SELECT MAX(e2.episode_number)",
		"NOT EXISTS (SELECT 1 FROM episodes e WHERE e.series_id = s.series_id",
	}
	for _, fragment := range unexpectedFragments {
		if strings.Contains(query, fragment) {
			t.Fatalf("expected query not to contain %q, got:\n%s", fragment, query)
		}
	}

	if len(args) != 2 {
		t.Fatalf("expected start/end args only, got %d", len(args))
	}
}

func TestBuildListEventsQuery_UsesNotExistsForDisabledLibraries(t *testing.T) {
	t.Parallel()

	repo := &CalendarRepository{}
	query, _ := repo.buildListEventsQuery(CalendarFilter{
		Start:              time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC),
		End:                time.Date(2026, time.April, 12, 0, 0, 0, 0, time.UTC),
		DisabledLibraryIDs: []int{42},
	})

	expectedFragments := []string{
		"EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id)",
		"EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = e.series_id)",
		"NOT EXISTS (SELECT 1 FROM media_item_libraries mil_disabled WHERE mil_disabled.content_id = mi.content_id AND mil_disabled.media_folder_id = ANY($3))",
		"NOT EXISTS (SELECT 1 FROM media_item_libraries mil_disabled WHERE mil_disabled.content_id = e.series_id AND mil_disabled.media_folder_id = ANY($4))",
		"NOT EXISTS (SELECT 1 FROM media_item_libraries mil_disabled WHERE mil_disabled.content_id = s.series_id AND mil_disabled.media_folder_id = ANY($5))",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected query to contain %q, got:\n%s", fragment, query)
		}
	}

	if strings.Contains(query, "media_folder_id NOT IN") {
		t.Fatalf("expected query not to use NOT IN for disabled libraries, got:\n%s", query)
	}
}

func TestBuildListEventsQuery_RejectsExplicitLibraryOutsideAllowedScope(t *testing.T) {
	t.Parallel()

	repo := &CalendarRepository{}
	libraryID := 7
	query, _ := repo.buildListEventsQuery(CalendarFilter{
		Start:             time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC),
		End:               time.Date(2026, time.April, 12, 0, 0, 0, 0, time.UTC),
		LibraryID:         &libraryID,
		AllowedLibraryIDs: []int{1, 2},
	})

	if !strings.Contains(query, "WHERE mi.type = 'movie' AND mi.release_date BETWEEN $1::date AND $2::date AND 1 = 0") {
		t.Fatalf("expected movie branch to short-circuit inaccessible library selection, got:\n%s", query)
	}
	if !strings.Contains(query, "e.air_date BETWEEN $1::date AND $2::date AND e.season_number > 0 AND 1 = 0") {
		t.Fatalf("expected episode branch to short-circuit inaccessible library selection, got:\n%s", query)
	}
	if !strings.Contains(query, "s.air_date BETWEEN $1::date AND $2::date AND s.season_number > 0 AND 1 = 0") {
		t.Fatalf("expected season branch to short-circuit inaccessible library selection, got:\n%s", query)
	}
}

func TestBuildListEventsQuery_AppliesIDRestriction(t *testing.T) {
	t.Parallel()

	repo := &CalendarRepository{}
	query, args := repo.buildListEventsQuery(CalendarFilter{
		Start:         time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC),
		End:           time.Date(2026, time.April, 12, 0, 0, 0, 0, time.UTC),
		RestrictByIDs: true,
		RestrictToIDs: []string{"series-1", "movie-2"},
	})

	for _, fragment := range []string{
		"mi.content_id = ANY($3)",
		"e.series_id = ANY($3)",
		"s.series_id = ANY($3)",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected query to contain %q, got:\n%s", fragment, query)
		}
	}
	if len(args) != 3 {
		t.Fatalf("expected start/end/ids args, got %d", len(args))
	}
}

func TestBuildListEventsQuery_EmptyRestrictionMatchesNothing(t *testing.T) {
	t.Parallel()

	repo := &CalendarRepository{}
	query, args := repo.buildListEventsQuery(CalendarFilter{
		Start:         time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC),
		End:           time.Date(2026, time.April, 12, 0, 0, 0, 0, time.UTC),
		RestrictByIDs: true,
		RestrictToIDs: nil,
	})

	if strings.Count(query, "1 = 0") != 3 {
		t.Fatalf("expected each branch to short-circuit with 1 = 0, got:\n%s", query)
	}
	if len(args) != 2 {
		t.Fatalf("expected only start/end args, got %d", len(args))
	}
}
