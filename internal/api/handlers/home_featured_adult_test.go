package handlers

import (
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

func sectionContentIDs(section sections.SectionWithItems) []string {
	ids := make([]string, 0, len(section.Items))
	for _, item := range section.Items {
		ids = append(ids, item.ContentID)
	}
	return ids
}

func TestFilterAdultFeaturedSections(t *testing.T) {
	items := []*models.MediaItem{
		{ContentID: "nc17", ContentRating: "NC-17"},
		{ContentID: "r", ContentRating: "R"},
		{ContentID: "fsk18", ContentRating: "FSK 18"},
		{ContentID: "tvma", ContentRating: "TV-MA"},
		{ContentID: "unrated", ContentRating: ""},
		{ContentID: "junk", ContentRating: "not a rating"},
	}
	input := []sections.SectionWithItems{
		{ResolvedSection: sections.ResolvedSection{ID: "featured", Featured: true}, Items: items, TotalCount: len(items)},
		{ResolvedSection: sections.ResolvedSection{ID: "ordinary"}, Items: items, TotalCount: len(items)},
	}

	filtered := filterAdultFeaturedSections(input)

	want := []string{"r", "tvma", "unrated", "junk"}
	got := sectionContentIDs(filtered[0])
	if len(got) != len(want) {
		t.Fatalf("featured items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("featured items = %v, want %v", got, want)
		}
	}
	if got := len(filtered[1].Items); got != len(items) {
		t.Errorf("ordinary section has %d items, want all %d", got, len(items))
	}
	if got := len(input[0].Items); got != len(items) {
		t.Errorf("the input featured section was modified: %d items, want %d", got, len(items))
	}
}

func TestDropEmptyAdultFeaturedSections(t *testing.T) {
	input := []sections.SectionWithItems{
		{ResolvedSection: sections.ResolvedSection{ID: "emptied", Featured: true}, TotalCount: 3},
		{ResolvedSection: sections.ResolvedSection{ID: "naturally-empty", Featured: true}},
		{ResolvedSection: sections.ResolvedSection{ID: "ordinary-empty"}, TotalCount: 3},
		{
			ResolvedSection: sections.ResolvedSection{ID: "non-empty", Featured: true},
			Items:           []*models.MediaItem{{ContentID: "visible"}},
			TotalCount:      3,
		},
	}

	filtered := dropEmptyAdultFeaturedSections(input)

	wantIDs := []string{"naturally-empty", "ordinary-empty", "non-empty"}
	if len(filtered) != len(wantIDs) {
		t.Fatalf("sections = %d, want %d", len(filtered), len(wantIDs))
	}
	for i, want := range wantIDs {
		if got := filtered[i].ID; got != want {
			t.Errorf("filtered[%d].ID = %q, want %q", i, got, want)
		}
	}
}

func TestHomeShowsAdultInFeaturedIsOffUntilTheProfileAsks(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := &SectionHandler{StoreProvider: testUserStoreProvider{store: store}}
	req := httptest.NewRequest(http.MethodGet, "/home/sections", nil)
	ctx := apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1}), "profile-1")

	if handler.homeShowsAdultInFeatured(ctx) {
		t.Fatal("adult titles are shown in Featured by default, want off")
	}
	if (&SectionHandler{}).homeShowsAdultInFeatured(ctx) {
		t.Fatal("adult titles are shown in Featured without a preference store, want off")
	}

	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key:       settingskeys.HomeShowAdultInFeatured,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: "profile-1",
	}, json.RawMessage(`true`)); err != nil {
		t.Fatalf("enable the preference: %v", err)
	}
	if !handler.homeShowsAdultInFeatured(ctx) {
		t.Fatal("the profile's preference did not resolve to on")
	}
}
