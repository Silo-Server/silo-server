package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

// --- home fakes ---

type fakeHome struct {
	err       error
	lastQuery handlers.CalendarQuery
	lastCmd   handlers.HomeDismissalCommand
	undone    []string
}

func (f *fakeHome) Calendar(_ context.Context, q handlers.CalendarQuery, _ catalogpkg.AccessFilter) (handlers.CalendarView, error) {
	if f.err != nil {
		return handlers.CalendarView{}, f.err
	}
	f.lastQuery = q
	if q.Filter == "watchlist" {
		return handlers.CalendarView{Events: []handlers.CalendarDayView{}}, nil
	}
	title, season, episode := "Premiere", 2, 1
	airAt, airTime, zone := "2026-01-10T02:00:00Z", "21:00", "America/New_York"
	series := "series:severance"
	return handlers.CalendarView{Events: []handlers.CalendarDayView{{Date: "2026-01-09", Items: []handlers.CalendarEventView{
		{ContentID: "episode:severance-s02e01", Type: "episode", Title: "Severance", EpisodeTitle: &title, SeriesID: &series, SeasonNumber: &season, EpisodeNumber: &episode,
			AirDate: "2026-01-09", AirTime: &airTime, AirAt: &airAt, AirTimezone: &zone, LocalAirDate: "2026-01-09", PosterURL: "https://s3.example.test/poster.jpg", Badges: []string{"season_premiere"}},
		{ContentID: "movie:heat-1995", Type: "movie", Title: "Heat", AirDate: "2026-01-09", LocalAirDate: "2026-01-09", Watched: true, Badges: nil},
	}}}}, nil
}

func (f *fakeHome) DismissHomeItem(_ context.Context, cmd handlers.HomeDismissalCommand) error {
	if f.err != nil {
		return f.err
	}
	if err := f.validate(cmd); err != nil {
		return err
	}
	f.lastCmd = cmd
	return nil
}

// validate mirrors the seam's per-surface requirements so the tests see
// the v1 decision rendered as a v2 problem.
func (f *fakeHome) validate(cmd handlers.HomeDismissalCommand) error {
	switch cmd.Surface {
	case "continue_watching":
		if cmd.ProgressUpdatedAt == "" {
			return &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "progress_updated_at is required", Field: "progress_updated_at"}
		}
	case "next_up":
		if cmd.SeriesID == "" {
			return &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "series_id is required", Field: "series_id"}
		}
	}
	return nil
}

func (f *fakeHome) UndismissHomeItem(_ context.Context, userID int, profileID, surface, itemID string) error {
	if f.err != nil {
		return f.err
	}
	f.undone = append(f.undone, profileID+"/"+surface+"/"+itemID)
	return nil
}

func (f *fakeHome) HomeLayout(_ context.Context) (handlers.SectionLayoutView, error) {
	if f.err != nil {
		return handlers.SectionLayoutView{}, f.err
	}
	return handlers.SectionLayoutView{Sections: []handlers.SectionLayoutEntryView{{ID: "continue_watching", SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20}, {ID: "next_up", SectionType: "next_up", Title: "Next Up", ItemLimit: 20, Customized: true}}}, nil
}

func homeDeps(t *testing.T) (Dependencies, *fakeHome) {
	t.Helper()
	deps := libraryViewDeps(t)
	fake := &fakeHome{}
	deps.Calendar = fake
	deps.HomeDismissals = fake
	deps.HomeSections = fake
	return deps, fake
}

// --- tests ---

func TestGetCalendar(t *testing.T) {
	deps, fake := homeDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/calendar?start=2026-01-05&end=2026-01-11&timezone=America/New_York&library_id=1", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"date":"2026-01-09"`, `"air_at":"2026-01-10T02:00:00.000Z"`, `"badges":["season_premiere"]`, `"badges":[]`, `"watched":true`, `"local_air_date":"2026-01-09"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s: %s", want, body)
		}
	}
	if strings.Contains(body, `"air_time":null`) || strings.Contains(body, `"poster_url":""`) {
		t.Errorf("absent members must be omitted: %s", body)
	}
	if fake.lastQuery.Filter != "all" || fake.lastQuery.Location.String() != "America/New_York" || fake.lastQuery.LibraryID == nil || *fake.lastQuery.LibraryID != 1 ||
		fake.lastQuery.Start.Format("2006-01-02") != "2026-01-05" || fake.lastQuery.End.Format("2006-01-02") != "2026-01-11" {
		t.Fatalf("query = %+v", fake.lastQuery)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/calendar?start=2026-01-05&end=2026-01-05&filter=watchlist", "", viewerHeaders())
	if rec.Code != 200 || rec.Body.String() != `{"events":[]}`+"\n" || fake.lastQuery.Location != nil && fake.lastQuery.Location.String() != "UTC" {
		t.Fatal(rec.Code, rec.Body.String(), fake.lastQuery.Location)
	}
	for _, q := range []string{"end=2026-01-11", "start=2026-01-05", "start=2026-01-05&end=2026-01-04", "start=2026-01-05&end=2026-02-05", "start=2026-01-05&end=2026-01-11&filter=mine",
		"start=2026-01-05&end=2026-01-11&timezone=Mars/Olympus", "start=2026-01-05&end=2026-01-11&library_id=x", "start=20260105&end=2026-01-11"} {
		requireProblem(t, do(t, h, http.MethodGet, "/api/v2/calendar?"+q, "", viewerHeaders()), TypeValidationFailed)
	}
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/calendar?start=2026-01-05&end=2026-02-05", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.end" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/calendar?start=2026-01-05&end=2026-01-11", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/calendar?start=2026-01-05&end=2026-01-11", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "failed to fetch calendar events"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/calendar?start=2026-01-05&end=2026-01-11", "", viewerHeaders()), TypeInternalError)
	deps.Calendar = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/calendar?start=2026-01-05&end=2026-01-11", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestHomeDismissals(t *testing.T) {
	deps, fake := homeDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPut, "/api/v2/home/dismissals/continue_watching/movie:heat-1995", `{"progress_updated_at":"2026-01-02T03:04:05.678Z"}`, viewerHeaders())
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if fake.lastCmd.UserID != 1 || fake.lastCmd.ProfileID != "p-owner" || fake.lastCmd.Surface != "continue_watching" || fake.lastCmd.ItemID != "movie:heat-1995" || fake.lastCmd.ProgressUpdatedAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("cmd = %+v", fake.lastCmd)
	}
	rec = do(t, h, http.MethodPut, "/api/v2/home/dismissals/next_up/episode:severance-s02e01", `{"series_id":"series:severance"}`, viewerHeaders())
	if rec.Code != 204 || fake.lastCmd.SeriesID != "series:severance" || fake.lastCmd.ProgressUpdatedAt != "" {
		t.Fatal(rec.Code, rec.Body.String(), fake.lastCmd)
	}
	p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/home/dismissals/continue_watching/movie:heat-1995", `{}`, viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.progress_updated_at" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/home/dismissals/next_up/episode:x", `{"progress_updated_at":"2026-01-02T03:04:05Z"}`, viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.series_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/home/dismissals/recently_added/movie:heat-1995", `{}`, viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/home/dismissals/next_up/episode:x", `{"series_id":"s","extra":1}`, viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/home/dismissals/continue_watching/movie:heat-1995", `{"progress_updated_at":"yesterday"}`, viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/home/dismissals/next_up/episode:x", `{"series_id":"s"}`, bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/home/dismissals/next_up/episode:x", `{"series_id":"s"}`, nil), TypeAuthenticationRequired)

	rec = do(t, h, http.MethodDelete, "/api/v2/home/dismissals/next_up/episode:severance-s02e01", "", viewerHeaders())
	if rec.Code != 204 || len(fake.undone) != 1 || fake.undone[0] != "p-owner/next_up/episode:severance-s02e01" {
		t.Fatal(rec.Code, rec.Body.String(), fake.undone)
	}
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/home/dismissals/recently_added/movie:heat-1995", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/home/dismissals/next_up/episode:x", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to save dismissal"}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/home/dismissals/next_up/episode:x", `{"series_id":"s"}`, viewerHeaders()), TypeInternalError)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/home/dismissals/next_up/episode:x", "", viewerHeaders()), TypeInternalError)
	deps.HomeDismissals = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodDelete, "/api/v2/home/dismissals/next_up/episode:x", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestGetHomeLayout(t *testing.T) {
	deps, fake := homeDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/home/layout", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"next_up"`) || !strings.Contains(rec.Body.String(), `"customized":true`) || strings.Contains(rec.Body.String(), "items") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/layout", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/layout", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to load sections"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/layout", "", viewerHeaders()), TypeInternalError)
	deps.HomeSections = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/home/layout", "", viewerHeaders()), TypeDependencyUnavailable)
}
