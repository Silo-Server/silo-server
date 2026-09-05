package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	compatSubtitleAlways  = "Always"
	compatSubtitleNone    = "None"
	compatSubtitleDefault = "Default"
	compatSubtitleSmart   = "Smart"
	profileSubtitleAuto   = "auto"
)

func (h *AuthHandler) WithUserStore(provider userstore.UserStoreProvider) *AuthHandler {
	h.storeProvider = provider
	return h
}

func configurationKey(profileID string) string { return "jellycompat:configuration:" + profileID }

func (h *AuthHandler) resolvedUserDTO(ctx context.Context, session *Session) (userDTOResponse, error) {
	dto := h.userDTO(session)
	if h.storeProvider == nil {
		return dto, nil
	}
	store, err := h.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return dto, err
	}
	raw, err := store.GetSetting(ctx, configurationKey(session.ProfileID))
	if err != nil {
		return dto, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &dto.Configuration); err != nil {
			return dto, err
		}
	}
	contract, err := settingscontract.Load()
	if err != nil {
		return dto, err
	}
	values, err := settingsresolve.New(contract).Resolve(ctx, store, settingsresolve.Context{ProfileID: session.ProfileID}, []string{settingskeys.PlaybackAudioLanguage, settingskeys.PlaybackSubtitleLanguage, settingskeys.PlaybackSubtitleMode, settingskeys.PlaybackAutoPlayNext}, nil)
	if err != nil {
		return dto, err
	}
	for _, value := range values {
		switch value.Key {
		case settingskeys.PlaybackAudioLanguage:
			dto.Configuration.AudioLanguagePreference = ""
			_ = json.Unmarshal(value.Value, &dto.Configuration.AudioLanguagePreference)
		case settingskeys.PlaybackSubtitleLanguage:
			dto.Configuration.SubtitleLanguagePreference = ""
			_ = json.Unmarshal(value.Value, &dto.Configuration.SubtitleLanguagePreference)
		case settingskeys.PlaybackAutoPlayNext:
			_ = json.Unmarshal(value.Value, &dto.Configuration.EnableNextEpisodeAutoPlay)
		case settingskeys.PlaybackSubtitleMode:
			var mode string
			_ = json.Unmarshal(value.Value, &mode)
			switch mode {
			case "always":
				dto.Configuration.SubtitleMode = compatSubtitleAlways
			case "off":
				dto.Configuration.SubtitleMode = compatSubtitleNone
			default:
				if dto.Configuration.SubtitleMode != compatSubtitleDefault {
					dto.Configuration.SubtitleMode = compatSubtitleSmart
				}
			}
		}
	}
	return dto, nil
}

func (h *AuthHandler) HandleUpdateConfiguration(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, 401, "Unauthorized", "Missing authentication token")
		return
	}
	if id := chi.URLParam(r, "userId"); id != "" && !validatePseudoUser(w, id, session) {
		return
	}
	if !validateOptionalUser(w, r, session) {
		return
	}
	if h.storeProvider == nil {
		writeCompatUpstreamError(w, fmt.Errorf("user store unavailable"))
		return
	}
	var patch map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&patch); err != nil || patch == nil {
		writeError(w, 400, "BadRequest", "Invalid configuration")
		return
	}
	dto, err := h.resolvedUserDTO(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	for field, value := range patch {
		if string(value) == "null" {
			switch field {
			case "AudioLanguagePreference", "SubtitleLanguagePreference", "CastReceiverId":
				// Unmarshalling null into an existing string leaves it unchanged.
				patch[field] = json.RawMessage(`""`)
			default:
				writeError(w, 400, "BadRequest", "Configuration fields cannot be null")
				return
			}
		}
	}
	raw, _ := json.Marshal(patch)
	if err := json.Unmarshal(raw, &dto.Configuration); err != nil {
		writeError(w, 400, "BadRequest", "Invalid configuration")
		return
	}
	values := map[string]json.RawMessage{}
	for field, key := range map[string]string{"AudioLanguagePreference": settingskeys.PlaybackAudioLanguage, "SubtitleLanguagePreference": settingskeys.PlaybackSubtitleLanguage, "EnableNextEpisodeAutoPlay": settingskeys.PlaybackAutoPlayNext, "SubtitleMode": settingskeys.PlaybackSubtitleMode} {
		value, ok := patch[field]
		if !ok {
			continue
		}
		if field == "SubtitleMode" {
			modes := map[string]string{compatSubtitleDefault: profileSubtitleAuto, compatSubtitleSmart: profileSubtitleAuto, compatSubtitleAlways: "always", compatSubtitleNone: "off"}
			mode, ok := modes[dto.Configuration.SubtitleMode]
			if !ok {
				writeError(w, 400, "BadRequest", "Unsupported subtitle mode")
				return
			}
			value, _ = json.Marshal(mode)
		} else if field != "EnableNextEpisodeAutoPlay" {
			var tag string
			if err := json.Unmarshal(value, &tag); err != nil {
				writeError(w, 400, "BadRequest", "Invalid language")
				return
			}
			if tag == "" {
				value = json.RawMessage("null")
			} else {
				normalized, ok := settingscontract.NormalizeLanguageTag(tag)
				if !ok {
					writeError(w, 400, "BadRequest", "Invalid language")
					return
				}
				value, _ = json.Marshal(lang.Canonical(normalized))
			}
		}
		values[key] = value
	}
	store, err := h.storeProvider.ForUser(r.Context(), session.StreamAppUserID)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	tx, ok := store.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		writeCompatUpstreamError(w, fmt.Errorf("settings transactions unavailable"))
		return
	}
	encoded, _ := json.Marshal(dto.Configuration)
	err = tx.WithPreferenceSettingsTransaction(r.Context(), func(writer userstore.PreferenceSettingsWriter) error {
		for key, value := range values {
			if _, err := writer.UpsertSettingValue(r.Context(), userstore.SettingIdentity{Key: key, Scope: settingscontract.ScopeProfile, ProfileID: session.ProfileID}, value); err != nil {
				return err
			}
		}
		return writer.SetSetting(r.Context(), configurationKey(session.ProfileID), string(encoded))
	})
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleCultures exposes the ISO language choices accepted by playback settings.
func (h *AuthHandler) HandleCultures(w http.ResponseWriter, r *http.Request) {
	type culture struct {
		Name                        string
		DisplayName                 string
		TwoLetterISOLanguageName    string
		ThreeLetterISOLanguageName  string
		ThreeLetterISOLanguageNames []string
	}
	cultures := make([]culture, 0)
	for _, base := range language.Supported.BaseLanguages() {
		code := base.String()
		if len(code) != 2 {
			continue
		}
		iso3 := base.ISO3()
		cultures = append(cultures, culture{Name: code, DisplayName: display.English.Languages().Name(base), TwoLetterISOLanguageName: code, ThreeLetterISOLanguageName: iso3, ThreeLetterISOLanguageNames: []string{iso3}})
	}
	writeJSON(w, http.StatusOK, cultures)
}
