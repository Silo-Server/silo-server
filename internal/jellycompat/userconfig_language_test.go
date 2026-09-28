package jellycompat

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestConfigurationLanguagePreferencesMatchCultureOptions(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	rec := httptest.NewRecorder()
	h.HandleCultures(rec, httptest.NewRequest("GET", "/", nil))
	var cultures []struct{ ThreeLetterISOLanguageName string }
	if err := json.Unmarshal(rec.Body.Bytes(), &cultures); err != nil {
		t.Fatal(err)
	}
	options := map[string]bool{}
	for _, culture := range cultures {
		options[culture.ThreeLetterISOLanguageName] = true
	}
	for _, code := range []string{"eng", "fra", "deu", "por", "zho"} {
		t.Run(code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			body, _ := json.Marshal(map[string]string{"AudioLanguagePreference": code, "SubtitleLanguagePreference": code})
			h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", string(body), "", "", session))
			if rec.Code != 204 {
				t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
			}
			dto, err := h.resolvedUserDTO(t.Context(), session)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range []string{dto.Configuration.AudioLanguagePreference, dto.Configuration.SubtitleLanguagePreference} {
				if got != code || !options[got] {
					t.Errorf("reloaded language %q does not match selected culture %q", got, code)
				}
			}
		})
	}
}

func TestConfigurationLanguageEchoPreservesNativeVariants(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	keys := []string{settingskeys.PlaybackAudioLanguage, settingskeys.PlaybackSubtitleLanguage}
	tx := store.(userstore.PreferenceSettingsTransactioner)
	if err := tx.WithPreferenceSettingsTransaction(t.Context(), func(writer userstore.PreferenceSettingsWriter) error {
		for _, key := range keys {
			if _, err := writer.UpsertSettingValue(t.Context(), userstore.SettingIdentity{Key: key, Scope: settingscontract.ScopeProfile, ProfileID: session.ProfileID}, json.RawMessage(`"pt-BR"`)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dto, err := h.resolvedUserDTO(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if dto.Configuration.AudioLanguagePreference != "por" || dto.Configuration.SubtitleLanguagePreference != "por" {
		t.Fatalf("configuration = %+v", dto.Configuration)
	}
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ body, want string }{
		{`{"AudioLanguagePreference":"por","SubtitleLanguagePreference":"por","EnableNextEpisodeAutoPlay":false}`, `"pt-BR"`},
		{`{"AudioLanguagePreference":"eng","SubtitleLanguagePreference":"eng"}`, `"en"`},
		{`{"AudioLanguagePreference":null,"SubtitleLanguagePreference":""}`, `null`},
	} {
		rec := httptest.NewRecorder()
		h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", step.body, "", "", session))
		if rec.Code != 204 {
			t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
		}
		values, err := settingsresolve.New(contract).Resolve(t.Context(), store, settingsresolve.Context{ProfileID: session.ProfileID}, keys, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if string(value.Value) != step.want {
				t.Errorf("%s = %s, want %s", value.Key, value.Value, step.want)
			}
		}
	}
}

func TestCompatLanguagePreference(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {"en", "eng"}, {"eng", "eng"}, {"pt-BR", "por"},
		{"zh-Hant", "zho"}, {"und", "und"}, {"und-Latn", "und-Latn"},
		{"x-custom", "x-custom"}, {"not a language", "not a language"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := compatLanguagePreference(tc.input); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
