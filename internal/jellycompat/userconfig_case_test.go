package jellycompat

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestConfigurationFieldCasingUpdatesCanonicalSettings(t *testing.T) {
	for _, casing := range []string{"Pascal", "camel", "mixed"} {
		t.Run(casing, func(t *testing.T) {
			store := newJellycompatUserStore(t)
			h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
			session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
			key := func(s string) string {
				switch casing {
				case "camel":
					return strings.ToLower(s[:1]) + s[1:]
				case "mixed":
					return strings.ToUpper(s)
				}
				return s
			}
			update := func(values map[string]any, status int) {
				t.Helper()
				patch := map[string]any{}
				for k, v := range values {
					patch[key(k)] = v
				}
				body, err := json.Marshal(patch)
				if err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", string(body), "", "", session))
				if rec.Code != status {
					t.Fatalf("update %s returned %d %s", body, rec.Code, rec.Body.String())
				}
			}
			update(map[string]any{"SubtitleMode": "Always", "AudioLanguagePreference": "fr", "SubtitleLanguagePreference": "en", "EnableNextEpisodeAutoPlay": false, "CastReceiverId": "receiver", "HidePlayedInLatest": true}, 204)
			dto, err := h.resolvedUserDTO(t.Context(), session)
			if err != nil {
				t.Fatal(err)
			}
			c := dto.Configuration
			if c.SubtitleMode != "Always" || c.AudioLanguagePreference != "fr" || c.SubtitleLanguagePreference != "en" || c.EnableNextEpisodeAutoPlay || c.CastReceiverID != "receiver" || !c.HidePlayedInLatest {
				t.Fatalf("configuration=%+v", c)
			}
			update(map[string]any{"AudioLanguagePreference": nil, "SubtitleLanguagePreference": nil, "CastReceiverId": nil}, 204)
			dto, err = h.resolvedUserDTO(t.Context(), session)
			if err != nil {
				t.Fatal(err)
			}
			c = dto.Configuration
			if c.AudioLanguagePreference != "" || c.SubtitleLanguagePreference != "" || c.CastReceiverID != "" || c.SubtitleMode != "Always" || c.EnableNextEpisodeAutoPlay || !c.HidePlayedInLatest {
				t.Fatalf("cleared configuration=%+v", c)
			}
			update(map[string]any{"SubtitleMode": nil}, 400)
			update(map[string]any{"EnableNextEpisodeAutoPlay": nil}, 400)
			update(map[string]any{"SubtitleMode": "OnlyForced"}, 400)
		})
	}
}

func TestConfigurationRejectsCaseVariantDuplicateFields(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	for _, body := range []string{`{"SubtitleMode":"Always","subtitleMode":"None"}`, `{"subtitleMode":"None","SubtitleMode":"Always"}`, `{"AudioLanguagePreference":null,"audioLanguagePreference":"fr"}`} {
		rec := httptest.NewRecorder()
		h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", body, "", "", session))
		if rec.Code != 400 {
			t.Fatalf("duplicate response=%d %s", rec.Code, rec.Body.String())
		}
		raw, err := store.GetSetting(t.Context(), configurationKey(session.ProfileID))
		if err != nil || raw != "" {
			t.Fatalf("duplicate persisted configuration=%q error=%v", raw, err)
		}
	}
}
