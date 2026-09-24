package jellycompat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// TestRouterPreferencesDiscovery verifies authenticated discovery through the
// client path variants and rejects another profile's grouping-options request.
func TestRouterPreferencesDiscovery(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(time.Hour, time.Now)
	const token = "preferences-test-token"
	if err := store.Put(Session{Token: token, StreamAppUserID: 1, ProfileID: "p1", PseudoUserID: PseudoUserID(1, "p1")}); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{Config: cfg, SessionStore: store})
	t.Run("grouping options reject another profile", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/Users/"+PseudoUserID(1, "p2").String()+"/GroupingOptions", nil)
		req.Header.Set("X-Emby-Token", token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "User not found") {
			t.Fatalf("other profile response = %d %s, want user-not-found error", rec.Code, rec.Body.String())
		}
	})
	for _, tc := range []struct {
		path     string
		cultures bool
	}{
		{"/Localization/Cultures", true},
		{"/Localization/cultures", true},
		{"/localization/cultures", true},
		{"/emby/localization/cultures", true},
		{"/jellyfin/LOCALIZATION/CULTURES", true},
		{"/SyncPlay/List", false},
		{"/syncplay/list", false},
		{"/emby/syncplay/list", false},
		{"/jellyfin/SYNCPLAY/LIST", false},
		{"/UserViews/GroupingOptions", false},
		{"/Users/" + PseudoUserID(1, "p1").String() + "/GroupingOptions", false},
		{"/emby/users/" + PseudoUserID(1, "p1").String() + "/groupingoptions", false},
		{"/jellyfin/USERS/" + PseudoUserID(1, "p1").String() + "/GROUPINGOPTIONS", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			for _, authenticated := range []bool{false, true} {
				req := httptest.NewRequest(http.MethodGet, tc.path, nil)
				if authenticated {
					req.Header.Set("X-Emby-Token", token)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if !authenticated {
					if rec.Code != http.StatusUnauthorized {
						t.Errorf("anonymous status = %d, want 401", rec.Code)
					}
					continue
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("authenticated status = %d, want 200: %s", rec.Code, rec.Body.String())
				}
				if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
					t.Errorf("Content-Type = %q, want JSON", rec.Header().Get("Content-Type"))
				}
				if !tc.cultures {
					if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
						t.Errorf("groups = %s, want []", got)
					}
					continue
				}
				var cultures []struct {
					TwoLetterISOLanguageName   string
					ThreeLetterISOLanguageName string
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &cultures); err != nil {
					t.Fatal(err)
				}
				for _, culture := range cultures {
					if culture.TwoLetterISOLanguageName == "en" && culture.ThreeLetterISOLanguageName == "eng" {
						return
					}
				}
				t.Fatal("cultures missing English ISO codes")
			}
		})
	}
}

// TestRouterDiscoveryNamesPreserveDisplayPreferenceIDs protects stored document
// identities from being rewritten when a route gains case-insensitive matching.
func TestRouterDiscoveryNamesPreserveDisplayPreferenceIDs(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	store := newJellycompatUserStore(t)
	sessions := NewSessionStore(time.Hour, time.Now)
	const token = "display-preferences-test-token"
	if err := sessions.Put(Session{Token: token, StreamAppUserID: 1, ProfileID: "profile-1", PseudoUserID: PseudoUserID(1, "profile-1")}); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{Config: cfg, SessionStore: sessions, UserStoreProvider: compatTestUserStoreProvider{store: store}})
	for _, id := range []string{"list", "cultures", "localization", "syncplay", "LiSt"} {
		t.Run(id, func(t *testing.T) {
			key := profilePreferencesID("profile-1", id)
			if err := store.SetJellycompatDisplayPrefs(t.Context(), key, "emby", `{"SortBy":"DateCreated"}`); err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"", "/emby", "/jellyfin"} {
				url := prefix + "/displaypreferences/" + id + "?client=emby"
				req := httptest.NewRequest(http.MethodGet, url, nil)
				req.Header.Set("X-Emby-Token", token)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				var dto displayPreferencesDTO
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s = %d: %s", url, rec.Code, rec.Body.String())
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
					t.Fatal(err)
				}
				if dto.ID != id || dto.SortBy != "DateCreated" {
					t.Fatalf("GET %s lost existing preferences: %+v", url, dto)
				}
				req = httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"SortBy":"DateCreated","SortOrder":"Descending"}`))
				req.Header.Set("X-Emby-Token", token)
				rec = httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusNoContent {
					t.Fatalf("POST %s = %d: %s", url, rec.Code, rec.Body.String())
				}
				stored, err := store.GetJellycompatDisplayPrefs(t.Context(), key, "emby")
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(stored), &dto); err != nil {
					t.Fatal(err)
				}
				if dto.ID != id || dto.SortOrder != "Descending" {
					t.Fatalf("POST %s did not update original document: %+v", url, dto)
				}
			}
		})
	}
}
