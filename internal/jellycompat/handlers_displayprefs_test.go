package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
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
	stored, err := store.GetJellycompatDisplayPrefs(context.Background(), "usersettings", "emby")
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
