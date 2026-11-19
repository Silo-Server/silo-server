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
	for _, id := range []string{"list", "cultures", "LiSt"} {
		t.Run(id, func(t *testing.T) {
			key := profilePreferencesID("profile-1", id)
			if err := store.SetJellycompatDisplayPrefs(t.Context(), key, "emby", `{"SortBy":"DateCreated"}`); err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"", "/emby"} {
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
