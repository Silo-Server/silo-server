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
	"github.com/Silo-Server/silo-server/internal/sections"
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
		row := handlers.SectionView{ID: sectionID, SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20, TotalCount: 1, Items: []handlers.SectionItemView{fakeCard()}}
		if viewer.ItemLimitCap > 0 {
			// A capped read of a row that holds one card more.
			row.ItemLimit, row.TotalCount, row.HasMore = min(row.ItemLimit, viewer.ItemLimitCap), 2, true
		}
		return row, nil
	case "next_up":
		return handlers.SectionView{ID: sectionID, SectionType: "next_up", Title: "Next Up", ItemLimit: 20, TotalCount: 1, Customized: true, Items: []handlers.SectionItemView{fakeCard()}}, nil
	case "franchises":
		library, creator := 1, "p-owner"
		return handlers.SectionView{ID: sectionID, SectionType: "collection_posters", Title: "Franchises", ItemLimit: 20, TotalCount: 2, Items: []handlers.SectionItemView{},
			Collections: []sections.CollectionCard{
				{ID: "01J9Z8C3W4R5T6Y7U8I9O0P1Q2", Source: sections.CollectionCardLibrary, Title: "Marvel", PosterURL: "https://s3.example.test/marvel.jpg", BackdropURL: "https://s3.example.test/marvel-backdrop.jpg", BackdropThumbhash: "3OcRJYB4d3h/iIeHeEh3eIhw+j2w", ItemCount: 12, LibraryID: &library},
				{ID: "c-ghibli", Source: sections.CollectionCardUser, Title: "Ghibli nights", PosterURL: "https://s3.example.test/collage.jpg", PosterIsCollage: true, ItemCount: 5, CreatorProfileID: &creator},
			}}, nil
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

// TestHomeCapabilities pins the home capability document: available with
// include_items_max and the item_limit maxima where the home sections
// service is wired, revalidated by its ETag, not_configured without them, and
// each advertised bound equal to the maximum its parameter validates.
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
		out.IncludeItemsMax != handlers.MaxHomeLayoutIncludedSections || out.SectionItemLimitMax != handlers.MaxSectionItemLimitCap ||
		out.RowItemLimitMax != handlers.MaxSectionItemLimitCap {
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
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "include_items_max") || strings.Contains(rec.Body.String(), "item_limit_max") {
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
	paramMaximum := func(path, name string) *float64 {
		for _, p := range doc.Paths[path]["get"].Parameters {
			if p.Name == name {
				return p.Schema.Maximum
			}
		}
		return nil
	}
	if maximum := paramMaximum(Prefix+"/home/layout", "include_items"); maximum == nil || int(*maximum) != handlers.MaxHomeLayoutIncludedSections {
		t.Fatalf("getHomeLayout include_items maximum = %v, want %d", maximum, handlers.MaxHomeLayoutIncludedSections)
	}
	for _, path := range []string{Prefix + "/home/sections", Prefix + "/library/{id}/sections", Prefix + "/home/sections/{id}/items", Prefix + "/home/layout"} {
		if maximum := paramMaximum(path, "item_limit"); maximum == nil || int(*maximum) != handlers.MaxSectionItemLimitCap {
			t.Fatalf("%s item_limit maximum = %v, want %d", path, maximum, handlers.MaxSectionItemLimitCap)
		}
	}
	if rec := do(t, h, http.MethodGet, fmt.Sprintf("%s/home/layout?include_items=%d", Prefix, handlers.MaxHomeLayoutIncludedSections+1), "", viewerHeaders()); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("include_items above the advertised maximum: %d", rec.Code)
	}
}
