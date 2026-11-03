package catalog

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

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

	if !strings.Contains(query, "WHERE mi.type = 'movie' AND LEAST(mi.digital_release_date, mi.physical_release_date) BETWEEN $1::date AND $2::date AND 1 = 0") {
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

// TestCalendarMovieHomeReleaseDB pins the movie calendar date: a movie is
// listed once, on its earliest home release (digital or physical), never on
// its primary (usually theatrical) date, and the event names which release it
// is. A movie with no home release yet is not listed.
func TestCalendarMovieHomeReleaseDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var libraryID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("calendar-digital-%d", suffix),
	).Scan(&libraryID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, libraryID)
	})

	date := func(s string) *string { return &s }
	movies := []struct {
		name     string
		primary  *string
		digital  *string
		physical *string
		wantDate string
		wantKind string
	}{
		{"digital only", date("2026-10-02"), date("2026-11-05"), nil, "2026-11-05", HomeReleaseDigital},
		{"physical first", nil, date("2026-11-20"), date("2026-11-03"), "2026-11-03", HomeReleasePhysical},
		{"digital first", nil, date("2026-11-08"), date("2026-11-22"), "2026-11-08", HomeReleaseDigital},
		{"same day", nil, date("2026-11-12"), date("2026-11-12"), "2026-11-12", HomeReleaseDigital},
		{"physical only", nil, nil, date("2026-11-18"), "2026-11-18", HomeReleasePhysical},
		{"theatrical only", date("2026-11-14"), nil, nil, "", ""},
		{"home release after the window", date("2026-11-25"), date("2026-12-15"), nil, "", ""},
	}

	itemRepo := NewItemRepository(pool)
	ids := make(map[string]string, len(movies))
	for i, m := range movies {
		id := fmt.Sprintf("movie:calendar-digital-%d-%d", suffix, i)
		ids[id] = m.name
		if err := itemRepo.Upsert(ctx, &models.MediaItem{
			ContentID:           id,
			Type:                "movie",
			Title:               fmt.Sprintf("Calendar Digital %d %s", suffix, m.name),
			Status:              "matched",
			ReleaseDate:         m.primary,
			DigitalReleaseDate:  m.digital,
			PhysicalReleaseDate: m.physical,
		}); err != nil {
			t.Fatalf("upsert %s: %v", m.name, err)
		}
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, libraryID)
		t.Cleanup(func() {
			batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, id)
		})

		// The item round-trips the dates, so a later upsert of the read-back
		// item cannot clear them.
		stored, err := itemRepo.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", m.name, err)
		}
		got := [2]string{derefDate(stored.DigitalReleaseDate), derefDate(stored.PhysicalReleaseDate)}
		if want := [2]string{derefDate(m.digital), derefDate(m.physical)}; got != want {
			t.Fatalf("%s: stored (digital, physical) = %q, want %q", m.name, got, want)
		}
	}

	events, err := NewCalendarRepository(pool).ListEvents(ctx, CalendarFilter{
		Start:             time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC),
		End:               time.Date(2026, time.November, 30, 0, 0, 0, 0, time.UTC),
		AllowedLibraryIDs: []int{libraryID},
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	got := make(map[string]CalendarEvent)
	for _, ev := range events {
		name, ok := ids[ev.ContentID]
		if !ok {
			continue
		}
		if _, dup := got[name]; dup {
			t.Fatalf("%s: listed more than once", name)
		}
		got[name] = ev
	}
	for _, m := range movies {
		ev, listed := got[m.name]
		if m.wantDate == "" {
			if listed {
				t.Errorf("%s: listed on %s, want it outside the window", m.name, ev.AirDate.Format(time.DateOnly))
			}
			continue
		}
		if d := ev.AirDate.Format(time.DateOnly); !listed || d != m.wantDate || ev.HomeRelease != m.wantKind {
			t.Errorf("%s: listed %v on %s as %q, want %s as %q", m.name, listed, d, ev.HomeRelease, m.wantDate, m.wantKind)
		}
	}
}

func derefDate(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
