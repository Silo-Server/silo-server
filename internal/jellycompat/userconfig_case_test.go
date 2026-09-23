package jellycompat

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
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
			update(map[string]any{"SubtitleMode": "Sometimes"}, 400)
		})
	}
}

// Jellyfin clients may save SubtitleMode OnlyForced; Silo stores it as
// subtitles off with forced subtitles shown and reports it back unchanged.
func TestConfigurationOnlyForcedSubtitleMode(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	update := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", body, "", "", session))
		if rec.Code != 204 {
			t.Fatalf("update %s returned %d %s", body, rec.Code, rec.Body.String())
		}
	}
	canonical := func() (string, bool) {
		t.Helper()
		contract, err := settingscontract.Load()
		if err != nil {
			t.Fatal(err)
		}
		values, err := settingsresolve.New(contract).Resolve(t.Context(), store, settingsresolve.Context{ProfileID: session.ProfileID}, []string{settingskeys.PlaybackSubtitleMode, settingskeys.PlaybackShowForcedSubtitles}, nil)
		if err != nil {
			t.Fatal(err)
		}
		mode, showForced := "", false
		for _, value := range values {
			switch value.Key {
			case settingskeys.PlaybackSubtitleMode:
				_ = json.Unmarshal(value.Value, &mode)
			case settingskeys.PlaybackShowForcedSubtitles:
				_ = json.Unmarshal(value.Value, &showForced)
			}
		}
		return mode, showForced
	}

	update(`{"SubtitleMode":"OnlyForced"}`)
	if mode, showForced := canonical(); mode != "off" || !showForced {
		t.Fatalf("canonical settings = %q/%v, want off/true", mode, showForced)
	}
	dto, err := h.resolvedUserDTO(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if dto.Configuration.SubtitleMode != "OnlyForced" {
		t.Fatalf("SubtitleMode = %q, want OnlyForced", dto.Configuration.SubtitleMode)
	}

	update(`{"SubtitleMode":"None"}`)
	if dto, err = h.resolvedUserDTO(t.Context(), session); err != nil || dto.Configuration.SubtitleMode != "None" {
		t.Fatalf("SubtitleMode after None = %q (%v)", dto.Configuration.SubtitleMode, err)
	}
}

// Jellyfin Web posts the whole configuration from every settings page. A mode
// sent back unchanged must keep native settings that have no Jellyfin mode.
func TestConfigurationUnchangedSubtitleModeKeepsNativeSettings(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	tx, ok := store.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		t.Fatal("test store does not support settings transactions")
	}
	if err := tx.WithPreferenceSettingsTransaction(t.Context(), func(writer userstore.PreferenceSettingsWriter) error {
		for key, value := range map[string]string{settingskeys.PlaybackSubtitleMode: `"auto"`, settingskeys.PlaybackShowForcedSubtitles: `false`} {
			if _, err := writer.UpsertSettingValue(t.Context(), userstore.SettingIdentity{Key: key, Scope: settingscontract.ScopeProfile, ProfileID: session.ProfileID}, json.RawMessage(value)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dto, err := h.resolvedUserDTO(t.Context(), session)
	if err != nil || dto.Configuration.SubtitleMode != "Smart" {
		t.Fatalf("SubtitleMode = %q (%v), want Smart", dto.Configuration.SubtitleMode, err)
	}

	rec := httptest.NewRecorder()
	h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", `{"SubtitleMode":"Smart","EnableNextEpisodeAutoPlay":false}`, "", "", session))
	if rec.Code != 204 {
		t.Fatalf("update returned %d %s", rec.Code, rec.Body.String())
	}
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	values, err := settingsresolve.New(contract).Resolve(t.Context(), store, settingsresolve.Context{ProfileID: session.ProfileID}, []string{settingskeys.PlaybackShowForcedSubtitles}, nil)
	if err != nil || len(values) != 1 || string(values[0].Value) != "false" {
		t.Fatalf("show forced subtitles = %+v (%v), want false", values, err)
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

// Jellyfin 12's AudioLanguagePreference "OriginalLanguage" is stored as the
// contract's original-language tag and reads back unchanged.
func TestConfigurationOriginalLanguageAudioPreference(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	rec := httptest.NewRecorder()
	h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", `{"AudioLanguagePreference":"OriginalLanguage"}`, "", "", session))
	if rec.Code != 204 {
		t.Fatalf("update returned %d %s", rec.Code, rec.Body.String())
	}
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	values, err := settingsresolve.New(contract).Resolve(t.Context(), store, settingsresolve.Context{ProfileID: session.ProfileID}, []string{settingskeys.PlaybackAudioLanguage}, nil)
	if err != nil || len(values) != 1 || string(values[0].Value) != `"x-silo-original"` {
		t.Fatalf("stored audio language = %v (%v), want x-silo-original", values, err)
	}
	dto, err := h.resolvedUserDTO(t.Context(), session)
	if err != nil || dto.Configuration.AudioLanguagePreference != "OriginalLanguage" {
		t.Fatalf("AudioLanguagePreference = %q (%v), want OriginalLanguage", dto.Configuration.AudioLanguagePreference, err)
	}
}
