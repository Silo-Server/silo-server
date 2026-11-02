package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userdb"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sectionBackdropPath(tt.sectionType, tt.path); got != tt.want {
				t.Fatalf("sectionBackdropPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestBuildSectionsResponseSupportsMixedEpisodeAndSeriesRecentItems(t *testing.T) {
	seasonNumber := 3
	episodeNumber := 7
	seriesID := "series-running"
	fetcher := &stubSectionEpisodeFetcher{meta: map[string]sections.SectionItemMeta{
		"episode-new": {
			SeriesID:      &seriesID,
			SeriesTitle:   "Running Show",
			SeasonNumber:  &seasonNumber,
			EpisodeNumber: &episodeNumber,
		},
	}}
	// Each card's own hint is profile-independent, so it reaches the response
	// only after the resolver validates it.
	resolver := &stubSectionPlayableTargetResolver{targets: map[string]string{
		playTargetKey("episode", "episode-new", "episode-new"):         "episode-new",
		playTargetKey("series", "series-batch", "episode-batch-first"): "episode-batch-first",
	}}
	h := &SectionHandler{episodeFetcher: fetcher, playableTargets: resolver}
	withItems := []sections.SectionWithItems{{
		ResolvedSection: sections.ResolvedSection{ID: "tv-recent", SectionType: sections.SectionRecentlyAdded, Title: "Recently Added in TV Shows"},
		Items: []*models.MediaItem{
			{ContentID: "episode-new", Type: "episode", Title: "A New Arrival", Status: "matched", PlayContentID: "episode-new"},
			{ContentID: "series-batch", Type: "series", Title: "Batch Show", Status: "matched", PlayContentID: "episode-batch-first"},
		},
	}}

	resp := h.buildSectionsResponse(httptest.NewRequest(http.MethodGet, "/sections", nil), withItems, nil)
	if fetcher.calls != 1 {
		t.Fatalf("episode metadata fetch calls = %d, want 1", fetcher.calls)
	}
	if len(resp.Sections) != 1 || len(resp.Sections[0].Items) != 2 {
		t.Fatalf("response shape = %#v", resp)
	}
	hints := make(map[string]string, len(resolver.query.Items))
	for _, input := range resolver.query.Items {
		hints[input.ContentID] = input.PreferredContentID
	}
	if hints["series-batch"] != "episode-batch-first" || hints["episode-new"] != "episode-new" {
		t.Fatalf("resolver hints = %v, want the items' own play targets", hints)
	}
	episodeItem := resp.Sections[0].Items[0]
	if episodeItem.Type != "episode" || episodeItem.SeriesTitle != "Running Show" || episodeItem.SeasonNumber == nil || *episodeItem.SeasonNumber != 3 || episodeItem.EpisodeNumber == nil || *episodeItem.EpisodeNumber != 7 {
		t.Fatalf("episode response = %#v", episodeItem)
	}
	if episodeItem.PlayContentID != "episode-new" {
		t.Fatalf("episode play content id = %q, want episode-new", episodeItem.PlayContentID)
	}
	seriesItem := resp.Sections[0].Items[1]
	if seriesItem.Type != "series" || seriesItem.Title != "Batch Show" || seriesItem.SeasonNumber != nil || seriesItem.EpisodeNumber != nil {
		t.Fatalf("series response = %#v", seriesItem)
	}
	if seriesItem.PlayContentID != "episode-batch-first" {
		t.Fatalf("series play content id = %q, want episode-batch-first", seriesItem.PlayContentID)
	}
}

// newSectionTestUserStore builds an empty in-memory user store; the section
// tests only need a usable store instance, not seeded rows.
func newSectionTestUserStore(t *testing.T) userstore.UserStore {
	t.Helper()

	db, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return userdb.NewSQLiteUserStore(db)
}

type stubSectionPlayableTargetResolver struct {
	query   catalog.PlayableTargetQuery
	targets map[string]string
}

// playTargetKey mirrors how the handlers look a resolved target up, so a stub
// can key its answers exactly the way the real resolver does.
func playTargetKey(mediaType, contentID, preferredContentID string) string {
	return catalog.PlayableTargetInput{
		Type:               mediaType,
		ContentID:          contentID,
		PreferredContentID: preferredContentID,
	}.Key()
}

func (s *stubSectionPlayableTargetResolver) ResolvePlayableTargets(_ context.Context, query catalog.PlayableTargetQuery) (map[string]string, error) {
	s.query = query
	return s.targets, nil
}

// Section rails must resolve play targets with the same profile progress and
// library scope the catalog endpoints use, so the same series card resumes at
// the same episode on both surfaces.
func TestBuildSectionsResponseScopesPlayTargetsToLibraryAndProgress(t *testing.T) {
	resolver := &stubSectionPlayableTargetResolver{targets: map[string]string{
		playTargetKey("series", "series-1", ""): "episode-s03e06",
	}}
	store := newSectionTestUserStore(t)
	h := &SectionHandler{
		playableTargets: resolver,
		StoreProvider:   testUserStoreProvider{store: store},
	}
	withItems := []sections.SectionWithItems{{
		ResolvedSection: sections.ResolvedSection{ID: "recent", SectionType: sections.SectionRecentlyAdded, Title: "Recently Added"},
		Items: []*models.MediaItem{
			{ContentID: "series-1", Type: "series", Title: "Long Runner", Status: "matched"},
		},
	}}

	req := httptest.NewRequest(http.MethodGet, "/library/7/sections", nil)
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{Role: "user", UserID: 42})
	ctx = apimw.SetProfileID(ctx, "p1")
	req = req.WithContext(ctx)

	libraryID := 7
	resp := h.buildSectionsResponse(req, withItems, &libraryID)

	if got := resolver.query.LibraryIDs; len(got) != 1 || got[0] != 7 {
		t.Fatalf("library IDs = %v, want [7]", got)
	}
	if resolver.query.ProgressStore == nil {
		t.Fatal("progress store = nil, want the request's user store")
	}
	if got := resp.Sections[0].Items[0].PlayContentID; got != "episode-s03e06" {
		t.Fatalf("play content id = %q, want episode-s03e06", got)
	}
}

// Section responses keep the first occurrence of a content ID within each row.
// A later row may still contain the same item because cross-section overlap is
// a separate, recipe-controlled behavior.
func TestBuildSectionsResponseDeduplicatesItemsWithinEachSection(t *testing.T) {
	resolver := &stubSectionPlayableTargetResolver{targets: map[string]string{
		playTargetKey("series", "series-1", "episode-s02e05"): "episode-s02e05",
	}}
	h := &SectionHandler{playableTargets: resolver}
	withItems := []sections.SectionWithItems{
		{
			ResolvedSection: sections.ResolvedSection{ID: "recent", SectionType: sections.SectionRecentlyAdded, Title: "Recently Added", ItemLimit: 3},
			Items: []*models.MediaItem{
				nil,
				{ContentID: "series-1", Type: "series", Title: "Long Runner", Status: "matched", PlayContentID: "episode-s02e05"},
				{ContentID: "", Type: "movie", Title: "Invalid", Status: "matched"},
				{ContentID: "movie-2", Type: "movie", Title: "Second", Status: "matched"},
				{ContentID: "series-1", Type: "series", Title: "Long Runner", Status: "matched", PlayContentID: "episode-s01e01"},
				{ContentID: "movie-3", Type: "movie", Title: "Third", Status: "matched"},
			},
			TotalCount: 6,
		},
		{
			ResolvedSection: sections.ResolvedSection{ID: "featured", SectionType: sections.SectionRecentlyReleased, Title: "Featured"},
			Items: []*models.MediaItem{
				{ContentID: "series-1", Type: "series", Title: "Long Runner", Status: "matched", PlayContentID: "episode-s02e05"},
				{ContentID: "series-1", Type: "series", Title: "Long Runner", Status: "matched", PlayContentID: "episode-s01e01"},
			},
			TotalCount: 10,
		},
	}

	resp := h.buildSectionsResponse(httptest.NewRequest(http.MethodGet, "/sections", nil), withItems, nil)
	if len(resp.Sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(resp.Sections))
	}
	items := resp.Sections[0].Items
	if len(items) != 3 {
		t.Fatalf("first section items = %d, want 3", len(items))
	}
	if got := []string{items[0].ContentID, items[1].ContentID, items[2].ContentID}; !reflect.DeepEqual(got, []string{"series-1", "movie-2", "movie-3"}) {
		t.Fatalf("first section IDs = %v, want [series-1 movie-2 movie-3]", got)
	}
	if items[0].PlayContentID != "episode-s02e05" {
		t.Fatalf("first occurrence play content id = %q, want episode-s02e05", items[0].PlayContentID)
	}
	if resp.Sections[0].TotalCount != 3 {
		t.Fatalf("first section total count = %d, want 3", resp.Sections[0].TotalCount)
	}
	if got := resp.Sections[1].Items; len(got) != 1 || got[0].ContentID != "series-1" {
		t.Fatalf("second section items = %#v, want cross-section series-1", got)
	}
	if resp.Sections[1].TotalCount != 10 {
		t.Fatalf("second section full total count = %d, want 10", resp.Sections[1].TotalCount)
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

func TestValidateSectionConfigRejectsUnknownContinueType(t *testing.T) {
	msg, ok := validateSectionConfig(sections.SectionContinueWatching, []byte(`{"continue_type":"scrolling"}`), false)
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

func TestInjectNextUpMatchesContinueWatchingItemLimit(t *testing.T) {
	watching := sections.ContinueTypeConfig(sections.ContinueTypeWatching)
	listening := sections.ContinueTypeConfig(sections.ContinueTypeListening)
	cases := []struct {
		name string
		in   []sections.ResolvedSection
		want int
	}{
		{
			name: "user limit on continue watching",
			in: []sections.ResolvedSection{
				{ID: "cw", SectionType: sections.SectionContinueWatching, Config: watching, ItemLimit: 35},
			},
			want: 35,
		},
		{
			name: "continue listening limit is not inherited",
			in: []sections.ResolvedSection{
				{ID: "cl", SectionType: sections.SectionContinueWatching, Config: listening, ItemLimit: 50},
			},
			want: 20,
		},
		{
			name: "unset limit keeps the default",
			in: []sections.ResolvedSection{
				{ID: "cw", SectionType: sections.SectionContinueWatching, Config: watching},
			},
			want: 20,
		},
		{
			name: "no continue row",
			in:   []sections.ResolvedSection{{ID: "recent", SectionType: sections.SectionRecentlyAdded, ItemLimit: 40}},
			want: 20,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, section := range injectNextUpSection(tc.in) {
				if section.ID != "system-next-up" {
					continue
				}
				if section.ItemLimit != tc.want {
					t.Fatalf("next-up item limit = %d, want %d", section.ItemLimit, tc.want)
				}
				return
			}
			t.Fatal("next-up section was not injected")
		})
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

// TestSaveProfileOverridesAdminOnlyRecipeFollowsActingAdmin: a child profile
// on an admin's account is not the admin an admin-only recipe asks for.
func TestSaveProfileOverridesAdminOnlyRecipeFollowsActingAdmin(t *testing.T) {
	h := &SectionHandler{}
	body := []byte(`{"scope":"home","library_id":"","overrides":[{"is_user_added":true,"user_section_type":"admin_curated_list","user_config":{"item_ids":["a"]}}]}`)
	req := httptest.NewRequest(http.MethodPut, "/profile/sections", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{Role: "admin", UserID: 1})
	ctx = apimw.SetProfileID(ctx, "kid")
	ctx = access.SetScope(ctx, access.Scope{UserID: 1, ProfileID: "kid", ProfileVerified: true})
	ctx = apimw.SetActingAdminDecider(ctx, func(_ context.Context, in apimw.ActingAdminRequest) (bool, error) {
		return in.ProfileID == "parent", nil
	})
	rec := httptest.NewRecorder()

	h.HandleSaveProfileOverrides(rec, req.WithContext(ctx))

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

// TestSaveProfileOverridesRejectsOversizedItemLimit: the fetchers size their
// buffers from item_limit, so a profile override above the admin cap is
// refused before the store is touched. The cap itself reaches the store.
func TestSaveProfileOverridesRejectsOversizedItemLimit(t *testing.T) {
	for _, tc := range []struct {
		limit    int
		wantCode int
	}{
		{sections.MaxItemLimit + 1, http.StatusBadRequest},
		{1_000_000_000, http.StatusBadRequest},
		// No store is wired, so a value that passes validation fails there.
		{sections.MaxItemLimit, http.StatusInternalServerError},
	} {
		h := &SectionHandler{}
		body := []byte(fmt.Sprintf(`{"scope":"home","library_id":"","overrides":[{"id":"o1","section_id":"s1","item_limit":%d}]}`, tc.limit))
		req := httptest.NewRequest(http.MethodPut, "/profile/sections", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := apimw.SetClaims(req.Context(), &auth.Claims{Role: "user", UserID: 1})
		ctx = apimw.SetProfileID(ctx, "p1")
		rec := httptest.NewRecorder()

		h.HandleSaveProfileOverrides(rec, req.WithContext(ctx))

		if rec.Code != tc.wantCode {
			t.Fatalf("item_limit %d: status = %d, want %d; body=%s", tc.limit, rec.Code, tc.wantCode, rec.Body.String())
		}
		if tc.wantCode == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "invalid_item_limit") {
			t.Fatalf("item_limit %d: body = %s, want invalid_item_limit", tc.limit, rec.Body.String())
		}
	}
}

// TestListProfileOverridesCapsStoredItemLimit: a row saved before the cap
// lists at the cap, so a client that saves back what it read is not refused.
func TestListProfileOverridesCapsStoredItemLimit(t *testing.T) {
	huge := 1_000_000_000
	store := &sourcePolicyStore{overrides: []userstore.SectionOverride{{ID: "o1", SectionID: "s1", ItemLimit: &huge}}}
	h := &SectionHandler{StoreProvider: sourcePolicyProvider{store: store}}
	q := SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}

	listed, err := h.ListProfileOverrides(t.Context(), q)
	if err != nil {
		t.Fatalf("ListProfileOverrides: %v", err)
	}
	if len(listed) != 1 || listed[0].ItemLimit == nil || *listed[0].ItemLimit != sections.MaxItemLimit {
		t.Fatalf("listed = %+v, want one override with item_limit %d", listed, sections.MaxItemLimit)
	}

	writes := []SectionOverrideWrite{{ID: listed[0].ID, SectionID: listed[0].SectionID, ItemLimit: listed[0].ItemLimit}}
	if err := h.SaveProfileOverrides(t.Context(), q, writes); err != nil {
		t.Fatalf("saving the listed overrides back: %v", err)
	}
}
