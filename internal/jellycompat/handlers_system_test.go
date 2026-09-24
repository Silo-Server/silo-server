package jellycompat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// TestSystemInfoCastReceiverApplications protects playback preferences, which
// iterate this array even when the client does not support Chromecast.
func TestSystemInfoCastReceiverApplications(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{Config: cfg})
	for _, path := range []string{"/System/Info", "/system/info", "/emby/System/Info", "/jellyfin/System/Info"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if got := string(body["CastReceiverApplications"]); got != "[]" {
				t.Fatalf("CastReceiverApplications = %q, want [] (not missing or null)", got)
			}
			var info publicSystemInfoResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
				t.Fatal(err)
			}
			if info.ID != cfg.JellyfinCompat.ServerID || info.Version != cfg.JellyfinCompat.EmulatedServerVersion || info.ProductName != "Jellyfin Server" || !info.StartupWizardCompleted {
				t.Fatalf("system identity changed: %+v", info)
			}
		})
	}
}
