package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type stubSectionEpisodeFetcher struct {
	calls int
	meta  map[string]sections.SectionItemMeta
}

func (s *stubSectionEpisodeFetcher) FetchEpisodesByContentIDs(_ context.Context, _ []string, _ catalog.AccessFilter) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error) {
	s.calls++
	return nil, s.meta, nil
}

func TestSectionBackdropPathUsesExpectedVariants(t *testing.T) {
	tests := []struct {
		name        string
		sectionType sections.SectionType
		path        string
		want        string
	}{
		{
			name:        "continue watching uses w1280",
			sectionType: sections.SectionContinueWatching,
			path:        "tmdb/movies/550/backdrop/original.webp",
			want:        "tmdb/movies/550/backdrop/w1280.webp",
		},
		{
			name:        "next up uses w1280",
			sectionType: sections.SectionNextUp,
			path:        "/tmdb/movies/550/backdrop/original.webp",
			want:        "/tmdb/movies/550/backdrop/w1280.webp",
		},
		{
			name:        "other sections use w1920",
			sectionType: sections.SectionRecentlyAdded,
			path:        "/tmdb/shows/1399/backdrop/original.jpg",
			want:        "/tmdb/shows/1399/backdrop/w1920.jpg",
		},
		{
			name:        "continue watching episode still clamps to w780",
			sectionType: sections.SectionContinueWatching,
			path:        "tvdb/series/73141/seasons/22/episodes/9/still/original.webp",
			want:        "tvdb/series/73141/seasons/22/episodes/9/still/w780.webp",
		},
		{
			name:        "featured section episode still clamps to w780",
			sectionType: sections.SectionRecentlyAdded,
			path:        "tvdb/series/73141/seasons/22/episodes/9/still/original.webp",
			want:        "tvdb/series/73141/seasons/22/episodes/9/still/w780.webp",
		},
		{
			name:        "http paths pass through",
			sectionType: sections.SectionContinueWatching,
			path:        "https://images.example.com/backdrop/original.jpg",
			want:        "https://images.example.com/backdrop/original.jpg",
		},
		{
			name:        "plugin paths pass through",
			sectionType: sections.SectionContinueWatching,
			path:        "plugin://tmdb/backdrop/original.jpg",
			want:        "plugin://tmdb/backdrop/original.jpg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sectionBackdropPath(tt.sectionType, tt.path); got != tt.want {
				t.Fatalf("sectionBackdropPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestBuildSectionsResponseEnrichesEpisodeMetadata(t *testing.T) {
	seasonNumber := 1
	episodeNumber := 1
	seriesID := "series-1"
	fetcher := &stubSectionEpisodeFetcher{
		meta: map[string]sections.SectionItemMeta{
			"episode-1": {
				SeriesID:      &seriesID,
				SeriesTitle:   "American Dad!",
				SeasonNumber:  &seasonNumber,
				EpisodeNumber: &episodeNumber,
			},
		},
	}
	h := &SectionHandler{episodeFetcher: fetcher}
	withItems := []sections.SectionWithItems{
		{
			ResolvedSection: sections.ResolvedSection{ID: "released", SectionType: sections.SectionCustomFilter, Title: "Released"},
			Items: []*models.MediaItem{{
				ContentID: "episode-1",
				Type:      "episode",
				Title:     "Dumbston Checks In",
				Status:    "matched",
			}},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/sections", nil)
	resp := h.buildSectionsResponse(req, withItems)

	if fetcher.calls != 1 {
		t.Fatalf("episode metadata fetch calls = %d, want 1", fetcher.calls)
	}
	item := resp.Sections[0].Items[0]
	if item.SeriesTitle != "American Dad!" {
		t.Fatalf("series title = %q, want %q", item.SeriesTitle, "American Dad!")
	}
	if item.SeasonNumber == nil || *item.SeasonNumber != 1 {
		t.Fatalf("season number = %v, want 1", item.SeasonNumber)
	}
	if item.EpisodeNumber == nil || *item.EpisodeNumber != 1 {
		t.Fatalf("episode number = %v, want 1", item.EpisodeNumber)
	}
}

func TestBuildSectionsResponseKeepsExistingEpisodeMeta(t *testing.T) {
	seasonNumber := 2
	episodeNumber := 6
	seriesID := "series-existing"
	fetcher := &stubSectionEpisodeFetcher{
		meta: map[string]sections.SectionItemMeta{
			"episode-1": {
				SeriesTitle: "Fetched Series",
			},
		},
	}
	h := &SectionHandler{episodeFetcher: fetcher}
	withItems := []sections.SectionWithItems{
		{
			ResolvedSection: sections.ResolvedSection{ID: "next", SectionType: sections.SectionNextUp, Title: "Next"},
			Items: []*models.MediaItem{{
				ContentID: "episode-1",
				Type:      "episode",
				Title:     "Episode 6",
				Status:    "matched",
			}},
			ItemMeta: map[string]sections.SectionItemMeta{
				"episode-1": {
					SeriesID:      &seriesID,
					SeriesTitle:   "Only Child",
					SeasonNumber:  &seasonNumber,
					EpisodeNumber: &episodeNumber,
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/sections", nil)
	resp := h.buildSectionsResponse(req, withItems)

	if fetcher.calls != 0 {
		t.Fatalf("episode metadata fetch calls = %d, want 0", fetcher.calls)
	}
	item := resp.Sections[0].Items[0]
	if item.SeriesTitle != "Only Child" {
		t.Fatalf("series title = %q, want %q", item.SeriesTitle, "Only Child")
	}
	if item.SeasonNumber == nil || *item.SeasonNumber != 2 {
		t.Fatalf("season number = %v, want 2", item.SeasonNumber)
	}
	if item.EpisodeNumber == nil || *item.EpisodeNumber != 6 {
		t.Fatalf("episode number = %v, want 6", item.EpisodeNumber)
	}
}

type countingSectionImageResolver struct {
	batchCalls  int
	singleCalls int
	variant     string
	paths       []string
}

func (r *countingSectionImageResolver) ResolveImageURL(_ context.Context, path string, variant string) string {
	r.singleCalls++
	return "single:" + variant + ":" + path
}

func (r *countingSectionImageResolver) ResolveImageURLs(_ context.Context, paths []string, variant string) map[string]string {
	resolved := r.ResolveImageURLsWithExpiry(context.Background(), paths, variant)
	urls := make(map[string]string, len(resolved))
	for path, value := range resolved {
		urls[path] = value.URL
	}
	return urls
}

func (r *countingSectionImageResolver) ResolveImageURLWithExpiry(_ context.Context, path string, variant string) catalog.ResolvedImageURL {
	r.singleCalls++
	return catalog.ResolvedImageURL{URL: "single:" + variant + ":" + path}
}

func (r *countingSectionImageResolver) ResolveImageURLsWithExpiry(_ context.Context, paths []string, variant string) map[string]catalog.ResolvedImageURL {
	r.batchCalls++
	r.variant = variant
	r.paths = append([]string{}, paths...)
	resolved := make(map[string]catalog.ResolvedImageURL, len(paths))
	for _, path := range paths {
		resolved[path] = catalog.ResolvedImageURL{URL: "resolved:" + path}
	}
	return resolved
}

func TestBuildSectionsResponseBatchResolvesImageURLs(t *testing.T) {
	resolver := &countingSectionImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	h := &SectionHandler{DetailSvc: detailSvc}

	items := make([]*models.MediaItem, 0, 100)
	for i := range 100 {
		items = append(items, &models.MediaItem{
			ContentID:    fmt.Sprintf("item-%03d", i),
			Type:         "movie",
			Title:        fmt.Sprintf("Movie %03d", i),
			PosterPath:   "/poster/original.jpg",
			BackdropPath: "/backdrop/original.jpg",
			LogoPath:     "/logo/original.png",
		})
	}
	withItems := []sections.SectionWithItems{
		{
			ResolvedSection: sections.ResolvedSection{ID: "continue", SectionType: sections.SectionContinueWatching, Title: "Continue"},
			Items:           items,
		},
		{
			ResolvedSection: sections.ResolvedSection{ID: "next-up", SectionType: sections.SectionNextUp, Title: "Next Up"},
			Items: []*models.MediaItem{{
				ContentID:    "item-extra",
				Type:         "movie",
				Title:        "Movie Extra",
				PosterPath:   "/poster/original.jpg",
				BackdropPath: "/backdrop/original.jpg",
				LogoPath:     "/logo/original.png",
			}},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/sections", nil)
	resp := h.buildSectionsResponse(req, withItems)

	if resolver.batchCalls != 1 {
		t.Fatalf("batch calls = %d, want 1", resolver.batchCalls)
	}
	if resolver.singleCalls != 0 {
		t.Fatalf("single calls = %d, want 0", resolver.singleCalls)
	}
	if resolver.variant != "featured" {
		t.Fatalf("variant = %q, want featured", resolver.variant)
	}
	sort.Strings(resolver.paths)
	wantPaths := []string{"/backdrop/w1280.jpg", "/logo/original.png", "/poster/w500.jpg"}
	if fmt.Sprint(resolver.paths) != fmt.Sprint(wantPaths) {
		t.Fatalf("resolved paths = %v, want %v", resolver.paths, wantPaths)
	}
	if got := resp.Sections[0].Items[0].PosterURL; got != "resolved:/poster/w500.jpg" {
		t.Fatalf("poster URL = %q", got)
	}
	if got := resp.Sections[0].Items[0].BackdropURL; got != "resolved:/backdrop/w1280.jpg" {
		t.Fatalf("backdrop URL = %q", got)
	}
	if got := resp.Sections[0].Items[0].LogoURL; got != "resolved:/logo/original.png" {
		t.Fatalf("logo URL = %q", got)
	}
}

func TestValidateSectionConfigAcceptsContinueTypes(t *testing.T) {
	tests := []string{
		`{"continue_type":"watching"}`,
		`{"continue_type":"listening"}`,
		`{"continue_type":"reading"}`,
		`{"filter_type":"audiobook"}`,
	}

	for _, config := range tests {
		t.Run(config, func(t *testing.T) {
			if msg, ok := validateSectionConfig(sections.SectionContinueWatching, []byte(config)); !ok {
				t.Fatalf("validateSectionConfig(%s) rejected config: %s", config, msg)
			}
		})
	}
}

func TestValidateSectionConfigRejectsUnknownContinueType(t *testing.T) {
	msg, ok := validateSectionConfig(sections.SectionContinueWatching, []byte(`{"continue_type":"scrolling"}`))
	if ok {
		t.Fatal("validateSectionConfig accepted unknown continue_type")
	}
	if msg != "continue_type must be 'watching', 'listening', or 'reading'" {
		t.Fatalf("message = %q", msg)
	}
}

func TestInjectNextUpAfterContiguousContinueRows(t *testing.T) {
	in := []sections.ResolvedSection{
		{
			ID:          "cw",
			SectionType: sections.SectionContinueWatching,
			Title:       "Continue Watching",
			Config:      sections.ContinueTypeConfig(sections.ContinueTypeWatching),
		},
		{
			ID:          "cl",
			SectionType: sections.SectionContinueWatching,
			Title:       "Continue Listening",
			Config:      sections.ContinueTypeConfig(sections.ContinueTypeListening),
		},
		{ID: "recent", SectionType: sections.SectionRecentlyAdded, Title: "Recently Added"},
	}

	got := injectNextUpSection(in)
	gotIDs := make([]string, 0, len(got))
	for _, section := range got {
		gotIDs = append(gotIDs, section.ID)
	}
	wantIDs := []string{"cw", "cl", "system-next-up", "recent"}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("section ids = %v, want %v", gotIDs, wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("section ids = %v, want %v", gotIDs, wantIDs)
		}
	}
}

func TestDropEmptySeasonalSectionsRemovesOnlyEmptySeasonal(t *testing.T) {
	in := []sections.SectionWithItems{
		// empty seasonal — drop
		{ResolvedSection: sections.ResolvedSection{ID: "a", SectionType: sections.SectionSeasonalThemed}},
		// non-empty seasonal — keep
		{ResolvedSection: sections.ResolvedSection{ID: "b", SectionType: sections.SectionSeasonalThemed}, Items: []*models.MediaItem{{ContentID: "1"}}},
		// empty non-seasonal — keep
		{ResolvedSection: sections.ResolvedSection{ID: "c", SectionType: sections.SectionRecentlyAdded}},
		// non-empty non-seasonal — keep
		{ResolvedSection: sections.ResolvedSection{ID: "d", SectionType: sections.SectionHiddenGems}, Items: []*models.MediaItem{{ContentID: "2"}}},
	}
	out := dropEmptySeasonalSections(in)
	if len(out) != 3 {
		t.Fatalf("expected 3, got %d", len(out))
	}
	for _, w := range out {
		if w.ID == "a" {
			t.Errorf("empty seasonal section was not dropped")
		}
	}
}

func TestDropEmptySeasonalSectionsHandlesNilAndEmpty(t *testing.T) {
	if got := dropEmptySeasonalSections(nil); len(got) != 0 {
		t.Errorf("expected empty/nil result for nil input, got %v", got)
	}
	if got := dropEmptySeasonalSections([]sections.SectionWithItems{}); len(got) != 0 {
		t.Errorf("expected empty result for empty input, got %v", got)
	}
}

func TestLibraryDefaultSectionsUsesFolderType(t *testing.T) {
	got := libraryDefaultSections(&models.MediaFolder{Type: "series"}, 12)

	if len(got) != 6 {
		t.Fatalf("expected 6 series default sections, got %d", len(got))
	}
	if got[1].Title != "Recently Added TV" {
		t.Fatalf("section 1 title = %q, want %q", got[1].Title, "Recently Added TV")
	}
	if got[2].Title != "Recently Released Episodes" {
		t.Fatalf("section 2 title = %q, want %q", got[2].Title, "Recently Released Episodes")
	}
	if got[4].SectionType != sections.SectionRecommendedForYou {
		t.Fatalf("section 4 type = %q, want %q", got[4].SectionType, sections.SectionRecommendedForYou)
	}
}

func TestFilterResolvedSectionsByAccessHidesBlockedLibraryRows(t *testing.T) {
	sectionsIn := []sections.ResolvedSection{
		{
			ID:          "continue",
			SectionType: sections.SectionContinueWatching,
			Title:       "Continue Watching",
			Config:      sections.ContinueTypeConfig(sections.ContinueTypeWatching),
		},
		{
			ID:          "allowed-library",
			SectionType: sections.SectionRecentlyAdded,
			Title:       "Recently Added in Allowed",
			Config:      sections.GeneratedHomeLibraryRecentConfig(11),
		},
		{
			ID:          "blocked-library",
			SectionType: sections.SectionRecentlyAdded,
			Title:       "Recently Added in Blocked",
			Config:      sections.GeneratedHomeLibraryRecentConfig(42),
		},
	}

	got := filterResolvedSectionsByAccess(sectionsIn, catalog.AccessFilter{
		AllowedLibraryIDs: []int{11},
	})

	if len(got) != 2 {
		t.Fatalf("filtered sections length = %d, want 2", len(got))
	}
	if got[0].ID != "continue" || got[1].ID != "allowed-library" {
		t.Fatalf("filtered section ids = [%s %s], want [continue allowed-library]", got[0].ID, got[1].ID)
	}
}

func TestFilterResolvedSectionsByAccessHidesDisabledOnlyRows(t *testing.T) {
	sectionsIn := []sections.ResolvedSection{
		{
			ID:          "disabled-library",
			SectionType: sections.SectionRecentlyReleased,
			Title:       "Recently Released in Disabled",
			Config:      sections.GeneratedHomeLibraryRecentConfig(42),
		},
		{
			ID:          "mixed-libraries",
			SectionType: sections.SectionRecentlyReleased,
			Title:       "Mixed",
			Config:      []byte(`{"filter_library_ids":[11,42]}`),
		},
	}

	got := filterResolvedSectionsByAccess(sectionsIn, catalog.AccessFilter{
		DisabledLibraryIDs: []int{42},
	})

	if len(got) != 1 {
		t.Fatalf("filtered sections length = %d, want 1", len(got))
	}
	if got[0].ID != "mixed-libraries" {
		t.Fatalf("filtered section id = %s, want mixed-libraries", got[0].ID)
	}
}

func TestApplyDiversityFilterSkipsDuplicatesInLaterAvoidSection(t *testing.T) {
	in := []sections.SectionWithItems{
		{ResolvedSection: sections.ResolvedSection{ID: "ra", SectionType: sections.SectionRecentlyAdded}, Items: []*models.MediaItem{{ContentID: "abc"}}},
		{ResolvedSection: sections.ResolvedSection{ID: "hg", SectionType: sections.SectionHiddenGems}, Items: []*models.MediaItem{{ContentID: "abc"}, {ContentID: "def"}}},
	}
	out := applyDiversityFilter(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(out))
	}
	if len(out[0].Items) != 1 || out[0].Items[0].ContentID != "abc" {
		t.Errorf("first section should keep abc")
	}
	if len(out[1].Items) != 1 || out[1].Items[0].ContentID != "def" {
		t.Errorf("hidden_gems should drop abc, keep def; got %v", out[1].Items)
	}
	if out[1].TotalCount != 1 {
		t.Errorf("hidden_gems TotalCount should be 1, got %d", out[1].TotalCount)
	}
}

func TestApplyDiversityFilterPreservesItemsInNonAvoidSections(t *testing.T) {
	in := []sections.SectionWithItems{
		{ResolvedSection: sections.ResolvedSection{ID: "ra", SectionType: sections.SectionRecentlyAdded}, Items: []*models.MediaItem{{ContentID: "abc"}}},
		{ResolvedSection: sections.ResolvedSection{ID: "rr", SectionType: sections.SectionRecentlyReleased}, Items: []*models.MediaItem{{ContentID: "abc"}}},
	}
	out := applyDiversityFilter(in)
	if len(out[0].Items) != 1 || len(out[1].Items) != 1 {
		t.Errorf("non-avoid sections should keep their items; got %d/%d", len(out[0].Items), len(out[1].Items))
	}
}

func TestApplyDiversityFilterAvoidSectionDoesNotShadowItself(t *testing.T) {
	in := []sections.SectionWithItems{
		{ResolvedSection: sections.ResolvedSection{ID: "hg", SectionType: sections.SectionHiddenGems}, Items: []*models.MediaItem{{ContentID: "abc"}}},
	}
	out := applyDiversityFilter(in)
	if len(out[0].Items) != 1 {
		t.Errorf("first section should not filter itself; got %d items", len(out[0].Items))
	}
}

func TestApplyDiversityFilterHandlesNilAndEmpty(t *testing.T) {
	if got := applyDiversityFilter(nil); got != nil && len(got) != 0 {
		t.Errorf("nil input returned non-empty: %v", got)
	}
	if got := applyDiversityFilter([]sections.SectionWithItems{}); len(got) != 0 {
		t.Errorf("empty input returned non-empty: %v", got)
	}
}

// TestSaveProfileOverridesRejectsAdminOnlyRecipeWhenSettingDisabled verifies that
// a non-admin profile attempting to save a user-added admin_curated_list override
// gets 403 when the allow_profile_custom_sections setting is disabled (the default).
func TestSaveProfileOverridesRejectsAdminOnlyRecipeWhenSettingDisabled(t *testing.T) {
	// StoreProvider is nil so we test the gate in isolation.
	// The gate runs BEFORE the StoreProvider check, so a 403 is returned before
	// the nil-StoreProvider 500 path is reached.
	h := &SectionHandler{}
	body := []byte(`{"scope":"home","library_id":"","overrides":[{"is_user_added":true,"user_section_type":"admin_curated_list","user_config":{"item_ids":["a"]}}]}`)
	req := httptest.NewRequest(http.MethodPut, "/profile/sections", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Authenticate as a non-admin profile.
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{Role: "user", UserID: 1})
	ctx = apimw.SetProfileID(ctx, "p1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	h.HandleSaveProfileOverrides(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestSaveProfileOverridesUnknownRecipeReturnsBadRequest verifies that an
// unregistered user_section_type is rejected before reaching the store.
func TestSaveProfileOverridesUnknownRecipeReturnsBadRequest(t *testing.T) {
	h := &SectionHandler{}
	body := []byte(`{"scope":"home","library_id":"","overrides":[{"is_user_added":true,"user_section_type":"no_such_recipe","user_config":{}}]}`)
	req := httptest.NewRequest(http.MethodPut, "/profile/sections", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{Role: "user", UserID: 1})
	ctx = apimw.SetProfileID(ctx, "p1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	h.HandleSaveProfileOverrides(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestSaveProfileOverridesLegacyShapeCannotBypassGate verifies that a non-admin
// cannot smuggle an admin-only recipe past the gate by omitting is_user_added
// and using the legacy section_type/config fields. The resolver treats any
// override with empty section_id as user-added, so the save handler must too.
func TestSaveProfileOverridesLegacyShapeCannotBypassGate(t *testing.T) {
	h := &SectionHandler{}
	// section_id is empty and is_user_added is omitted — pre-fix this skipped
	// the gate entirely, even though the resolver would still surface this as
	// a user-added admin_curated_list section.
	body := []byte(`{"scope":"home","library_id":"","overrides":[{"section_id":"","section_type":"admin_curated_list","config":{"item_ids":["a"]}}]}`)
	req := httptest.NewRequest(http.MethodPut, "/profile/sections", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{Role: "user", UserID: 1})
	ctx = apimw.SetProfileID(ctx, "p1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	h.HandleSaveProfileOverrides(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for legacy-shape bypass, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestSaveProfileOverridesLegacyShapeUnknownRecipeIsRejected ensures that
// legacy-shape user-added overrides whose section_type is not a registered
// recipe are rejected as 400, not silently passed through.
func TestSaveProfileOverridesLegacyShapeUnknownRecipeIsRejected(t *testing.T) {
	h := &SectionHandler{}
	body := []byte(`{"scope":"home","library_id":"","overrides":[{"section_id":"","section_type":"no_such_recipe","config":{}}]}`)
	req := httptest.NewRequest(http.MethodPut, "/profile/sections", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{Role: "user", UserID: 1})
	ctx = apimw.SetProfileID(ctx, "p1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	h.HandleSaveProfileOverrides(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestToSectionOverridesPropagatesUserAddedFields verifies the storage→resolver
// shim copies the four user-added fields (IsUserAdded / UserSectionType /
// UserConfig / UserTitle). Without this propagation the resolver sees zero
// values and silently drops profile-built sections — even after the SQLite
// persistence layer correctly stores them.
func TestToSectionOverridesPropagatesUserAddedFields(t *testing.T) {
	stored := []userstore.SectionOverride{
		{
			ID:              "ov-1",
			SectionID:       "",
			IsUserAdded:     true,
			UserSectionType: "hidden_gems",
			UserConfig:      `{"min_rating":7.5}`,
			UserTitle:       "Hidden Gems",
		},
		{
			ID:          "ov-2",
			SectionID:   "admin-1",
			SectionType: "recently_added",
			Title:       "Renamed",
		},
	}

	got := toSectionOverrides(stored)
	if len(got) != 2 {
		t.Fatalf("got %d overrides, want 2", len(got))
	}

	if !got[0].IsUserAdded {
		t.Error("user-added IsUserAdded = false, want true")
	}
	if string(got[0].UserSectionType) != "hidden_gems" {
		t.Errorf("UserSectionType = %q, want hidden_gems", got[0].UserSectionType)
	}
	if string(got[0].UserConfig) != `{"min_rating":7.5}` {
		t.Errorf("UserConfig = %q, want %q", string(got[0].UserConfig), `{"min_rating":7.5}`)
	}
	if got[0].UserTitle != "Hidden Gems" {
		t.Errorf("UserTitle = %q, want Hidden Gems", got[0].UserTitle)
	}

	// Admin-section customization should leave the user-added fields zero.
	if got[1].IsUserAdded || got[1].UserSectionType != "" || len(got[1].UserConfig) != 0 || got[1].UserTitle != "" {
		t.Errorf("legacy customization leaked user-added fields: %+v", got[1])
	}
}
