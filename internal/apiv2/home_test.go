package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// --- home fakes ---

type fakeHome struct {
	err        error
	lastQuery  handlers.CalendarQuery
	lastCmd    handlers.HomeDismissalCommand
	lastViewer handlers.SectionViewer
	// lastInclude is the include_items the layout seam last saw.
	lastInclude int
	undone      []string
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

// HomeLayoutWithItems answers the fake layout and, like the seam, the first
// includeItems rows as HomeSectionItems answers them.
func (f *fakeHome) HomeLayoutWithItems(ctx context.Context, viewer handlers.SectionViewer, includeItems int) (handlers.HomeLayoutView, error) {
	if f.err != nil {
		return handlers.HomeLayoutView{}, f.err
	}
	f.lastInclude = includeItems
	f.lastViewer = viewer
	view := handlers.HomeLayoutView{
		Layout:           handlers.SectionLayoutView{Sections: []handlers.SectionLayoutEntryView{{ID: "continue_watching", SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20}, {ID: "next_up", SectionType: "next_up", Title: "Next Up", ItemLimit: 20, Customized: true}}},
		IncludedSections: []handlers.SectionView{},
	}
	for i, s := range view.Layout.Sections {
		if i >= includeItems {
			break
		}
		row, err := f.HomeSectionItems(ctx, s.ID, viewer)
		if err != nil {
			return handlers.HomeLayoutView{}, err
		}
		view.IncludedSections = append(view.IncludedSections, row)
	}
	return view, nil
}

func (f *fakeHome) HomeSections(_ context.Context, viewer handlers.SectionViewer) (handlers.SectionsView, error) {
	if f.err != nil {
		return handlers.SectionsView{}, f.err
	}
	f.lastViewer = viewer
	return handlers.SectionsView{Sections: []handlers.SectionView{{ID: "continue_watching", SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20, TotalCount: 1, Items: []handlers.SectionItemView{fakeCard()}}}}, nil
}

func (f *fakeHome) HomeSectionItems(_ context.Context, sectionID string, viewer handlers.SectionViewer) (handlers.SectionView, error) {
	if f.err != nil {
		return handlers.SectionView{}, f.err
	}
	f.lastViewer = viewer
	switch sectionID {
	case "continue_watching":
		return handlers.SectionView{ID: sectionID, SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20, TotalCount: 1, Items: []handlers.SectionItemView{fakeCard()}}, nil
	case "next_up":
		return handlers.SectionView{ID: sectionID, SectionType: "next_up", Title: "Next Up", ItemLimit: 20, TotalCount: 1, Customized: true, Items: []handlers.SectionItemView{fakeCard()}}, nil
	}
	return handlers.SectionView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Section not found"}
}

func (f *fakeHome) Recipes() []handlers.RecipeCategoryView {
	return []handlers.RecipeCategoryView{
		{Category: "library_staples", Recipes: []recipes.RecipeDefinition{{Type: "recently_added", Category: recipes.CategoryLibraryStaples, Presets: []recipes.GalleryPreset{}, AvoidDuplicates: true}}},
		{Category: "mood", Recipes: []recipes.RecipeDefinition{{Type: "mood", Category: recipes.CategoryMood, SupportsRotation: true, Presets: []recipes.GalleryPreset{
			{Key: "cozy", DisplayName: "Cozy night in", Icon: "moon", DescriptionShort: "Warm and gentle", DefaultParams: json.RawMessage(`{"mood":"cozy","genres":["comedy"]}`)},
			{Key: "bare", DisplayName: "Bare", Icon: "dot", DescriptionShort: "No params"},
		}}}},
	}
}

func (f *fakeHome) RecipeCandidates(_ context.Context, recipeType string) ([]handlers.Candidate, error) {
	if f.err != nil {
		return nil, f.err
	}
	if recipeType != "custom_filter" {
		return nil, &handlers.APIError{Status: http.StatusNotFound, Code: "unknown_recipe", Message: "no candidate source for this recipe type"}
	}
	return []handlers.Candidate{{Value: "action", DisplayName: "Action", Subtitle: "12 titles"}, {Value: "drama", DisplayName: "Drama"}}, nil
}

func homeDeps(t *testing.T) (Dependencies, *fakeHome) {
	t.Helper()
	deps := libraryViewDeps(t)
	fake := &fakeHome{}
	deps.Calendar = fake
	deps.HomeDismissals = fake
	deps.HomeSections = fake
	deps.Recipes = fake
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

// TestGetHomeLayoutIncludedSections pins the include_items option: absent
// or 0 answers the layout with an empty included_sections, N answers the
// first N rows byte for byte as getHomeSectionItems does, and the bound is
// a validation failure.
func TestGetHomeLayoutIncludedSections(t *testing.T) {
	deps, fake := homeDeps(t)
	h := newTestHandler(t, deps)
	type layoutBody struct {
		Sections         []json.RawMessage `json:"sections"`
		IncludedSections []json.RawMessage `json:"included_sections"`
	}
	decode := func(t *testing.T, path string) layoutBody {
		t.Helper()
		rec := do(t, h, http.MethodGet, path, "", viewerHeaders())
		if rec.Code != 200 {
			t.Fatal(path, rec.Code, rec.Body.String())
		}
		var body layoutBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	plain := do(t, h, http.MethodGet, "/api/v2/home/layout", "", viewerHeaders()).Body.String()
	if !strings.HasSuffix(plain, `,"included_sections":[]}`+"\n") || fake.lastInclude != 0 {
		t.Fatalf("include_items absent: %s (seam saw %d)", plain, fake.lastInclude)
	}
	if zero := do(t, h, http.MethodGet, "/api/v2/home/layout?include_items=0", "", viewerHeaders()).Body.String(); zero != plain {
		t.Fatalf("include_items=0 = %s, want %s", zero, plain)
	}

	body := decode(t, "/api/v2/home/layout?include_items=2&image_size=large")
	if fake.lastInclude != 2 || fake.lastViewer.ImageSize != imagesize.Large || fake.lastViewer.Access.ProfileID != "p-owner" {
		t.Fatalf("seam saw include=%d viewer=%+v", fake.lastInclude, fake.lastViewer)
	}
	if len(body.Sections) != 2 || len(body.IncludedSections) != 2 {
		t.Fatalf("sections=%d included=%d, want 2 and 2", len(body.Sections), len(body.IncludedSections))
	}
	for i, id := range []string{"continue_watching", "next_up"} {
		row := do(t, h, http.MethodGet, "/api/v2/home/sections/"+id+"/items?image_size=large", "", viewerHeaders())
		if got, want := string(body.IncludedSections[i]), strings.TrimSuffix(row.Body.String(), "\n"); got != want {
			t.Fatalf("included_sections[%d] = %s, want the getHomeSectionItems body %s", i, got, want)
		}
	}

	for _, bad := range []string{"-1", "5", "two"} {
		p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/layout?include_items="+bad, "", viewerHeaders()), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != "query.include_items" {
			t.Fatalf("include_items=%s errors = %+v", bad, p.Errors)
		}
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/layout?include_items=1&image_size=huge", "", viewerHeaders()), TypeValidationFailed)
}

func TestListHomeSections(t *testing.T) {
	deps, fake := homeDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/home/sections?image_size=large&backdrop_width=2688", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"continue_watching"`) || !strings.Contains(rec.Body.String(), `"total_count":1`) || !strings.Contains(rec.Body.String(), `"items":[{`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if fake.lastViewer.ImageSize != imagesize.Large || fake.lastViewer.ImageWidths != (imagesize.Widths{Backdrop: 2688}) ||
		fake.lastViewer.Access.UserID != 1 || fake.lastViewer.Access.ProfileID != "p-owner" {
		t.Fatalf("viewer = %+v", fake.lastViewer)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/sections?image_size=huge", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/sections", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/sections", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to load sections"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/sections", "", viewerHeaders()), TypeInternalError)
	deps.HomeSections = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/home/sections", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestGetHomeSectionItems(t *testing.T) {
	deps, fake := homeDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/home/sections/continue_watching/items", "", viewerHeaders())
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), `{"id":"continue_watching"`) || !strings.Contains(rec.Body.String(), `"items":[{`) || strings.Contains(rec.Body.String(), `"section":`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if fake.lastViewer.ImageSize != imagesize.Unset {
		t.Fatalf("viewer = %+v", fake.lastViewer)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/sections/nope/items", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/sections/continue_watching/items", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/sections/continue_watching/items", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to load sections"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/home/sections/continue_watching/items", "", viewerHeaders()), TypeInternalError)
}

func TestListSectionRecipes(t *testing.T) {
	deps, _ := homeDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/sections/recipes", "", viewerHeaders())
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(body, `{"categories":[{"category":"library_staples"`) || !strings.Contains(body, `"presets":[]`) ||
		!strings.Contains(body, `"default_params":{"genres":["comedy"],"mood":"cozy"}`) || !strings.Contains(body, `"key":"bare"`) || !strings.Contains(body, `"default_params":{}`) ||
		strings.Contains(body, "hidden") {
		t.Fatal(rec.Code, body)
	}
	if rec := do(t, h, http.MethodGet, "/api/v2/sections/recipes", "", bearer(memberToken)); rec.Code != 200 || rec.Body.String() != body {
		t.Fatal("account-scoped gallery read:", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/sections/recipes", "", nil), TypeAuthenticationRequired)
	deps.Recipes = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/sections/recipes", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestListSectionRecipeCandidates(t *testing.T) {
	deps, fake := homeDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/sections/recipes/custom_filter/candidates", "", viewerHeaders())
	if rec.Code != 200 || rec.Body.String() != `{"candidates":[{"value":"action","display_name":"Action","subtitle":"12 titles"},{"value":"drama","display_name":"Drama","subtitle":""}]}`+"\n" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/sections/recipes/nope/candidates", "", viewerHeaders()), TypeNotFound)
	if rec2 := do(t, h, http.MethodGet, "/api/v2/sections/recipes/custom_filter/candidates", "", bearer(memberToken)); rec2.Code != 200 || rec2.Body.String() != rec.Body.String() {
		t.Fatal("account-scoped candidates read:", rec2.Code, rec2.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/sections/recipes/custom_filter/candidates", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "candidate_error", Message: "boom"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/sections/recipes/custom_filter/candidates", "", viewerHeaders()), TypeInternalError)
}

// TestHomeCapabilities pins the home capability document: available with
// include_items_max where the home sections service is wired, revalidated by
// its ETag, not_configured without it, and include_items_max equal to the
// include_items maximum getHomeLayout validates.
func TestHomeCapabilities(t *testing.T) {
	path := Prefix + "/home/capabilities"
	deps, _ := homeDeps(t)
	h := newTestHandler(t, deps)
	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)

	rec := do(t, h, http.MethodGet, path, "", viewerHeaders())
	var out HomeCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || out.State != StateAvailable || out.Allowed == nil || !*out.Allowed || out.Revision == "" ||
		out.IncludeItemsMax != handlers.MaxHomeLayoutIncludedSections {
		t.Fatalf("capabilities: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != cachePrivateNoCache {
		t.Fatalf("Cache-Control = %q", got)
	}
	tag := rec.Header().Get("ETag")
	if cached := do(t, h, http.MethodGet, path, "", with(viewerHeaders(), "If-None-Match", tag)); tag == "" || cached.Code != http.StatusNotModified {
		t.Fatalf("revalidation with %q: %d", tag, cached.Code)
	}

	deps.HomeSections = nil
	rec = do(t, newTestHandler(t, deps), http.MethodGet, path, "", viewerHeaders())
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "include_items_max") {
		t.Fatalf("unconfigured: %d %s", rec.Code, rec.Body.String())
	}
	out = HomeCapabilities{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.State != StateNotConfigured || out.Allowed == nil || *out.Allowed {
		t.Fatalf("unconfigured: %+v %v", out, err)
	}

	// The advertised bound is the one the layout enforces.
	data, err := generatedOpenAPIBytes()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name   string `json:"name"`
				Schema struct {
					Maximum *float64 `json:"maximum"`
				} `json:"schema"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var maximum *float64
	for _, p := range doc.Paths[Prefix+"/home/layout"]["get"].Parameters {
		if p.Name == "include_items" {
			maximum = p.Schema.Maximum
		}
	}
	if maximum == nil || int(*maximum) != handlers.MaxHomeLayoutIncludedSections {
		t.Fatalf("getHomeLayout include_items maximum = %v, want %d", maximum, handlers.MaxHomeLayoutIncludedSections)
	}
	if rec := do(t, h, http.MethodGet, fmt.Sprintf("%s/home/layout?include_items=%d", Prefix, handlers.MaxHomeLayoutIncludedSections+1), "", viewerHeaders()); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("include_items above the advertised maximum: %d", rec.Code)
	}
}
