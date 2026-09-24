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
