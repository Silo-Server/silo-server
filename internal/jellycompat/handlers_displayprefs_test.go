package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestDefaultDisplayPreferencesIncludesRequiredImageDimensions(t *testing.T) {
	dto := defaultDisplayPreferences("default", "Wholphin")

	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal default display preferences: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal default display preferences: %v", err)
	}

	if _, ok := raw["PrimaryImageHeight"]; !ok {
		t.Fatal("PrimaryImageHeight missing from display preferences JSON")
	}
	if _, ok := raw["PrimaryImageWidth"]; !ok {
		t.Fatal("PrimaryImageWidth missing from display preferences JSON")
	}
}

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

// TestDisplayPreferencesRoundTripUsesDedicatedTable drives the handlers over a
// real store: an update persists to jellycompat_displayprefs — not to the
// user_settings table the blobs used to ride — and a subsequent get serves it
// back.
func TestDisplayPreferencesRoundTripUsesDedicatedTable(t *testing.T) {
	store := newJellycompatUserStore(t)
	handler := NewDisplayPreferencesHandler(compatTestUserStoreProvider{store: store})

	newRequest := func(method, target, body string) *http.Request {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("displayPreferencesId", "usersettings")
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile-1"})
		return req.WithContext(ctx)
	}

	rec := httptest.NewRecorder()
	handler.HandleUpdateDisplayPreferences(rec, newRequest(http.MethodPost,
		"/DisplayPreferences/usersettings?client=emby",
		`{"SortBy":"DateCreated","SortOrder":"Descending","CustomPrefs":{"homesection0":"resume"}}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("update status = %d body=%s", rec.Code, rec.Body.String())
	}

	// The blob lands in the dedicated table under (id, client)...
	stored, err := store.GetJellycompatDisplayPrefs(t.Context(), profilePreferencesID("profile-1", "usersettings"), "emby")
	if err != nil || stored == "" {
		t.Fatalf("dedicated table holds (%q, %v), want the stored blob", stored, err)
	}
	// ...and nowhere in the legacy settings table.
	entries, err := store.ListSettings(context.Background())
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Key, "jellycompat:") {
			t.Errorf("user_settings still carries %s", entry.Key)
		}
	}

	rec = httptest.NewRecorder()
	handler.HandleGetDisplayPreferences(rec, newRequest(http.MethodGet,
		"/DisplayPreferences/usersettings?client=emby", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", rec.Code, rec.Body.String())
	}
	var dto displayPreferencesDTO
	if err := json.NewDecoder(rec.Body).Decode(&dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.SortBy != "DateCreated" || dto.SortOrder != "Descending" ||
		dto.CustomPrefs["homesection0"] != "resume" {
		t.Fatalf("round-trip lost data: %+v", dto)
	}

	// A different client for the same id keeps its own document.
	rec = httptest.NewRecorder()
	handler.HandleGetDisplayPreferences(rec, newRequest(http.MethodGet,
		"/DisplayPreferences/usersettings?client=jellyfin-web", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("other-client get status = %d", rec.Code)
	}
	var other displayPreferencesDTO
	if err := json.NewDecoder(rec.Body).Decode(&other); err != nil {
		t.Fatalf("decode other client: %v", err)
	}
	if other.SortBy == "DateCreated" {
		t.Fatal("another client's read returned the emby document")
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

// Jellyfin Web posts the whole usersettings document back. Reads must carry
// Jellyfin's 10 s/30 s skip defaults so an unrelated save keeps them instead
// of storing the 15 s write fallback.
func TestDisplayPreferencesReadThenSaveKeepsSkipDefaults(t *testing.T) {
	store := newJellycompatUserStore(t)
	handler := NewDisplayPreferencesHandler(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}

	rec := httptest.NewRecorder()
	handler.HandleGetDisplayPreferences(rec, viewerRequest("GET", "/?client=emby", "", "displayPreferencesId", "usersettings", session))
	var dto displayPreferencesDTO
	if err := json.NewDecoder(rec.Body).Decode(&dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.CustomPrefs["skipBackLength"] != "10000" || dto.CustomPrefs["skipForwardLength"] != "30000" {
		t.Fatalf("read defaults = %v", dto.CustomPrefs)
	}

	dto.CustomPrefs["appTheme"] = "dark"
	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	handler.HandleUpdateDisplayPreferences(rec, viewerRequest("POST", "/?client=emby", string(body), "displayPreferencesId", "usersettings", session))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("update status = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handler.HandleGetDisplayPreferences(rec, viewerRequest("GET", "/?client=emby", "", "displayPreferencesId", "usersettings", session))
	dto = displayPreferencesDTO{}
	if err := json.NewDecoder(rec.Body).Decode(&dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.CustomPrefs["skipBackLength"] != "10000" || dto.CustomPrefs["skipForwardLength"] != "30000" || dto.CustomPrefs["appTheme"] != "dark" {
		t.Fatalf("round trip = %v", dto.CustomPrefs)
	}
}
