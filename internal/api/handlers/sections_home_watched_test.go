package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestFilterWatchedHomeSectionItems(t *testing.T) {
	watched := &models.MediaItem{ContentID: "watched", Title: "Watched"}
	unwatched := &models.MediaItem{ContentID: "unwatched", Title: "Unwatched"}
	states := map[string]*itemUserStateResponse{
		"watched":   {Played: true},
		"unwatched": {Played: false},
	}

	input := []sections.SectionWithItems{
		{
			ResolvedSection: sections.ResolvedSection{ID: "ordinary", SectionType: sections.SectionRecentlyAdded},
			Items:           []*models.MediaItem{watched, unwatched},
			TotalCount:      2,
		},
		{
			ResolvedSection: sections.ResolvedSection{ID: "featured", SectionType: sections.SectionRecentlyAdded, Featured: true},
			Items:           []*models.MediaItem{watched, unwatched},
			TotalCount:      2,
		},
		{
			ResolvedSection: sections.ResolvedSection{ID: "most-watched", SectionType: sections.SectionMostWatched},
			Items:           []*models.MediaItem{watched, unwatched},
			TotalCount:      2,
		},
		{
			ResolvedSection: sections.ResolvedSection{ID: "activity", SectionType: sections.SectionProfileActivityFeed},
			Items:           []*models.MediaItem{watched, unwatched},
			TotalCount:      2,
		},
		{
			ResolvedSection: sections.ResolvedSection{ID: "forgotten", SectionType: sections.SectionForgottenFavorites},
			Items:           []*models.MediaItem{watched, unwatched},
			TotalCount:      2,
		},
	}

	filtered := filterWatchedHomeSectionItems(input, states)
	if got := filtered[0].Items; len(got) != 1 || got[0].ContentID != "unwatched" {
		t.Fatalf("ordinary section items = %#v, want only unwatched", got)
	}
	if filtered[0].TotalCount != 2 {
		t.Errorf("ordinary TotalCount = %d, want original section count 2", filtered[0].TotalCount)
	}
	for _, index := range []int{1, 2, 3, 4} {
		if got := len(filtered[index].Items); got != 2 {
			t.Errorf("exempt section %q has %d items, want 2", filtered[index].ID, got)
		}
	}
	if got := len(input[0].Items); got != 2 {
		t.Errorf("input section mutated to %d items", got)
	}
}

func TestFilterWatchedHomeSectionItemsKeepsUnknownState(t *testing.T) {
	item := &models.MediaItem{ContentID: "unknown", Title: "Unknown"}
	input := []sections.SectionWithItems{{
		ResolvedSection: sections.ResolvedSection{ID: "ordinary", SectionType: sections.SectionRecentlyAdded},
		Items:           []*models.MediaItem{item},
		TotalCount:      1,
	}}

	filtered := filterWatchedHomeSectionItems(input, nil)
	if len(filtered[0].Items) != 1 {
		t.Fatal("item with unavailable user state was hidden")
	}
}

func TestFilterWatchedHomeSectionItemsRefillsDisplayLimit(t *testing.T) {
	items := make([]*models.MediaItem, 0, 23)
	states := make(map[string]*itemUserStateResponse, 23)
	for i := 0; i < 23; i++ {
		id := fmt.Sprintf("item-%02d", i+1)
		items = append(items, &models.MediaItem{ContentID: id})
		states[id] = &itemUserStateResponse{Played: i < 3}
	}
	input := []sections.SectionWithItems{{
		ResolvedSection: sections.ResolvedSection{
			ID:          "ordinary",
			SectionType: sections.SectionRecentlyAdded,
			ItemLimit:   20,
		},
		Items:      items,
		TotalCount: 50,
	}}

	filtered := filterWatchedHomeSectionItems(input, states)
	if got := len(filtered[0].Items); got != 20 {
		t.Fatalf("filtered section has %d items, want configured limit 20", got)
	}
	if got := filtered[0].Items[19].ContentID; got != "item-23" {
		t.Errorf("last displayed item = %q, want refill candidate item-23", got)
	}
	if got := filtered[0].TotalCount; got != 50 {
		t.Errorf("TotalCount = %d, want source count 50", got)
	}
	if got := len(input[0].Items); got != 23 {
		t.Errorf("input section mutated to %d items", got)
	}
}

func TestHomeSectionsForFetchExpandsOnlyFilteredSections(t *testing.T) {
	resolved := []sections.ResolvedSection{
		{ID: "ordinary", SectionType: sections.SectionRecentlyAdded, ItemLimit: 20},
		{ID: "featured", SectionType: sections.SectionRecentlyAdded, Featured: true, ItemLimit: 20},
		{ID: "most-watched", SectionType: sections.SectionMostWatched, ItemLimit: 20},
		{ID: "large", SectionType: sections.SectionRecentlyAdded, ItemLimit: 250},
	}

	unchanged := homeSectionsForFetch(resolved, sectionResponseOptions{})
	if unchanged[0].ItemLimit != 20 {
		t.Fatalf("disabled preference changed limit to %d", unchanged[0].ItemLimit)
	}

	expanded := homeSectionsForFetch(resolved, sectionResponseOptions{hideWatchedHomeItems: true})
	wantLimits := []int{100, 20, 20, 250}
	for i, want := range wantLimits {
		if got := expanded[i].ItemLimit; got != want {
			t.Errorf("section %q fetch limit = %d, want %d", expanded[i].ID, got, want)
		}
	}
	if got := resolved[0].ItemLimit; got != 20 {
		t.Errorf("input limit mutated to %d", got)
	}
}

func TestHomeWatchedCandidateLimitCapsExpandedWindow(t *testing.T) {
	for _, test := range []struct {
		display int
		want    int
	}{
		{display: 0, want: 0},
		{display: 20, want: 100},
		{display: 50, want: 200},
		{display: 250, want: 250},
	} {
		if got := homeWatchedCandidateLimit(test.display); got != test.want {
			t.Errorf("homeWatchedCandidateLimit(%d) = %d, want %d", test.display, got, test.want)
		}
	}
}

func TestDropEmptyWatchedHomeSections(t *testing.T) {
	input := []sections.SectionWithItems{
		{
			ResolvedSection: sections.ResolvedSection{ID: "filtered", SectionType: sections.SectionRecentlyAdded},
			TotalCount:      10,
		},
		{
			ResolvedSection: sections.ResolvedSection{ID: "naturally-empty", SectionType: sections.SectionRecentlyAdded},
		},
		{
			ResolvedSection: sections.ResolvedSection{
				ID:          "featured",
				SectionType: sections.SectionRecentlyAdded,
				Featured:    true,
			},
			TotalCount: 10,
		},
		{
			ResolvedSection: sections.ResolvedSection{ID: "most-watched", SectionType: sections.SectionMostWatched},
			TotalCount:      10,
		},
		{
			ResolvedSection: sections.ResolvedSection{ID: "non-empty", SectionType: sections.SectionRecentlyAdded},
			Items:           []*models.MediaItem{{ContentID: "visible"}},
			TotalCount:      10,
		},
	}

	filtered := dropEmptyWatchedHomeSections(input)
	wantIDs := []string{"naturally-empty", "featured", "most-watched", "non-empty"}
	if len(filtered) != len(wantIDs) {
		t.Fatalf("filtered sections = %d, want %d", len(filtered), len(wantIDs))
	}
	for i, want := range wantIDs {
		if got := filtered[i].ID; got != want {
			t.Errorf("filtered[%d].ID = %q, want %q", i, got, want)
		}
	}
}

func TestWatchedRefillRunsBeforeDiversity(t *testing.T) {
	visible := &models.MediaItem{ContentID: "visible"}
	overflow := &models.MediaItem{ContentID: "overflow"}
	sectionsWithItems := []sections.SectionWithItems{
		{
			ResolvedSection: sections.ResolvedSection{
				ID:          "ordinary",
				SectionType: sections.SectionRecentlyAdded,
				ItemLimit:   1,
			},
			Items: []*models.MediaItem{visible, overflow},
		},
		{
			ResolvedSection: sections.ResolvedSection{
				ID:          "avoid-duplicates",
				SectionType: sections.SectionHiddenGems,
			},
			Items: []*models.MediaItem{overflow},
		},
	}

	filtered := filterWatchedHomeSectionItems(sectionsWithItems, map[string]*itemUserStateResponse{
		"visible":  {Played: false},
		"overflow": {Played: false},
	})
	diverse := applyDiversityFilter(filtered)

	if got := diverse[0].Items; len(got) != 1 || got[0].ContentID != "visible" {
		t.Fatalf("first section items = %#v, want visible refill", got)
	}
	if got := diverse[1].Items; len(got) != 1 || got[0].ContentID != "overflow" {
		t.Fatalf("hidden overflow candidate affected downstream diversity: %#v", got)
	}
}

func TestHomePreferenceFiltersOnlyHomeResponses(t *testing.T) {
	ctx := context.Background()
	store := newPlaybackTestStore(t)
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key:       settingskeys.HomeHideWatchedItems,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: "profile-1",
	}, json.RawMessage(`true`)); err != nil {
		t.Fatalf("enable Home preference: %v", err)
	}
	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
		ProfileID:   "profile-1",
		MediaItemID: "watched",
		Completed:   true,
		Source:      userstore.WatchHistorySourceManual,
	}); err != nil {
		t.Fatalf("mark item watched: %v", err)
	}

	items := []*models.MediaItem{
		{ContentID: "watched", Type: "movie", Title: "Watched"},
		{ContentID: "unwatched", Type: "movie", Title: "Unwatched"},
	}
	sectionItems := []sections.SectionWithItems{{
		ResolvedSection: sections.ResolvedSection{ID: "ordinary", SectionType: sections.SectionRecentlyAdded},
		Items:           items,
		TotalCount:      len(items),
	}}
	handler := &SectionHandler{StoreProvider: testUserStoreProvider{store: store}}
	req := httptest.NewRequest(http.MethodGet, "/home/sections/ordinary/items", nil)
	reqCtx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1})
	req = req.WithContext(apimw.SetProfileID(reqCtx, "profile-1"))

	options := handler.homeSectionResponseOptions(req)
	prepared, options := handler.prepareHomeSectionsResponse(req, sectionItems, options)
	home := handler.buildSectionsResponseWithOptions(req, prepared, options)
	if got := home.Sections[0].Items; len(got) != 1 || got[0].ContentID != "unwatched" {
		t.Fatalf("Home response items = %#v, want only unwatched", got)
	}

	library := handler.buildSectionsResponse(req, sectionItems)
	if got := len(library.Sections[0].Items); got != 2 {
		t.Fatalf("non-Home response has %d items, want 2", got)
	}
}
