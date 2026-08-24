package handlers

import (
	"context"
	"encoding/json"
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

	home := handler.buildHomeSectionsResponse(req, sectionItems)
	if got := home.Sections[0].Items; len(got) != 1 || got[0].ContentID != "unwatched" {
		t.Fatalf("Home response items = %#v, want only unwatched", got)
	}

	library := handler.buildSectionsResponse(req, sectionItems)
	if got := len(library.Sections[0].Items); got != 2 {
		t.Fatalf("non-Home response has %d items, want 2", got)
	}
}
