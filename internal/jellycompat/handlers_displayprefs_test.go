package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestDisplayPreferencesPreserveLegacyPrimaryCustomization(t *testing.T) {
	store := newJellycompatUserStore(t)
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "secondary", Name: "Secondary"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetJellycompatDisplayPrefs(t.Context(), "usersettings", "emby", `{"SortBy":"DateCreated","CustomPrefs":{"homesection0":"resume"}}`); err != nil {
		t.Fatal(err)
	}
	handler := NewDisplayPreferencesHandler(compatTestUserStoreProvider{store: store})
	read := func(profileID string) displayPreferencesDTO {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/DisplayPreferences/usersettings?client=emby", nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("displayPreferencesId", "usersettings")
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: profileID})
		rec := httptest.NewRecorder()
		handler.HandleGetDisplayPreferences(rec, req.WithContext(ctx))
		if rec.Code != http.StatusOK {
			t.Fatalf("read status=%d body=%s", rec.Code, rec.Body.String())
		}
		var dto displayPreferencesDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
			t.Fatal(err)
		}
		return dto
	}
	if got := read("profile-1"); got.SortBy != "DateCreated" || got.CustomPrefs["homesection0"] != "resume" {
		t.Fatalf("legacy customization lost: %+v", got)
	}
	if got := read("secondary"); got.SortBy == "DateCreated" {
		t.Fatal("legacy account preferences leaked to another profile")
	}
	if err := store.SetJellycompatDisplayPrefs(t.Context(), profilePreferencesID("profile-1", "usersettings"), "emby", `{"SortBy":"ProductionYear"}`); err != nil {
		t.Fatal(err)
	}
	if got := read("profile-1"); got.SortBy != "ProductionYear" {
		t.Fatalf("scoped customization overwritten: %+v", got)
	}
}

func TestNormalizeSavedCustomPrefsJellyfin12(t *testing.T) {
	prefs := map[string]string{
		"skipForwardLength": "",
		"landing-abc":       "",
		"Landing-def":       "suggestions",
		"homesection0":      "resume",
	}
	normalizeSavedCustomPrefs(prefs)
	if prefs["skipForwardLength"] != "15000" || prefs["skipBackLength"] != "15000" {
		t.Fatalf("skip lengths = %q/%q, want 15000/15000", prefs["skipForwardLength"], prefs["skipBackLength"])
	}
	if _, ok := prefs["landing-abc"]; ok {
		t.Fatal("empty landing preference must be dropped")
	}
	if prefs["Landing-def"] != "suggestions" || prefs["homesection0"] != "resume" {
		t.Fatalf("other preferences changed: %v", prefs)
	}

	kept := map[string]string{"skipForwardLength": "30000", "skipBackLength": "5000"}
	normalizeSavedCustomPrefs(kept)
	if kept["skipForwardLength"] != "30000" || kept["skipBackLength"] != "5000" {
		t.Fatalf("explicit skip lengths overwritten: %v", kept)
	}
}
