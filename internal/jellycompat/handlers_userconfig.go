package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// compatAudioOriginalLanguage is Jellyfin 12's AudioLanguagePreference for
// "each item's original language", stored as playback.OriginalLanguageTag.
const compatAudioOriginalLanguage = "OriginalLanguage"

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
	values, err := settingsresolve.New(contract).Resolve(ctx, store, settingsresolve.Context{ProfileID: session.ProfileID}, []string{settingskeys.PlaybackAudioLanguage, settingskeys.PlaybackSubtitleLanguage, settingskeys.PlaybackSubtitleMode, settingskeys.PlaybackShowForcedSubtitles, settingskeys.PlaybackAutoPlayNext}, nil)
	if err != nil {
		return dto, err
	}
	savedSubtitleMode := dto.Configuration.SubtitleMode
	nativeSubtitleMode, subtitleModeSet, showForced := "", false, true
	for _, value := range values {
		switch value.Key {
		case settingskeys.PlaybackAudioLanguage:
			dto.Configuration.AudioLanguagePreference = ""
			_ = json.Unmarshal(value.Value, &dto.Configuration.AudioLanguagePreference)
			if playback.IsOriginalLanguagePreference(dto.Configuration.AudioLanguagePreference) {
				dto.Configuration.AudioLanguagePreference = compatAudioOriginalLanguage
			} else {
				dto.Configuration.AudioLanguagePreference = compatLanguagePreference(dto.Configuration.AudioLanguagePreference)
			}
		case settingskeys.PlaybackSubtitleLanguage:
			dto.Configuration.SubtitleLanguagePreference = ""
			_ = json.Unmarshal(value.Value, &dto.Configuration.SubtitleLanguagePreference)
			dto.Configuration.SubtitleLanguagePreference = compatLanguagePreference(dto.Configuration.SubtitleLanguagePreference)
		case settingskeys.PlaybackAutoPlayNext:
			_ = json.Unmarshal(value.Value, &dto.Configuration.EnableNextEpisodeAutoPlay)
		case settingskeys.PlaybackSubtitleMode:
			_ = json.Unmarshal(value.Value, &nativeSubtitleMode)
			subtitleModeSet = value.Source != settingscontract.ScopeDefault
		case settingskeys.PlaybackShowForcedSubtitles:
			_ = json.Unmarshal(value.Value, &showForced)
		}
	}
	dto.Configuration.SubtitleMode = compatJellyfinSubtitleMode(nativeSubtitleMode, subtitleModeSet, showForced, savedSubtitleMode)
	return dto, nil
}

// compatLanguagePreference converts recognized languages to ISO three-letter codes.
// Keep unrecognized tags intact and never infer a language for an undefined tag.
func compatLanguagePreference(value string) string {
	tag, err := language.Parse(value)
	if value == "" || err != nil {
		return value
	}
	base, _, _ := tag.Raw()
	undefined, _, _ := language.Und.Raw()
	if base == undefined {
		return value
	}
	return base.ISO3()
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
	// The DTO decoder accepts case-insensitive JSON keys. Normalize the patch
	// too so null validation and canonical settings apply the same fields.
	// Reject case variants of one key rather than selecting a random map entry.
	normalized := make(map[string]json.RawMessage, len(patch))
	for field, value := range patch {
		field = strings.ToLower(field)
		if _, exists := normalized[field]; exists {
			writeError(w, 400, "BadRequest", "Duplicate configuration field")
			return
		}
		normalized[field] = value
	}
	patch = normalized
	dto, err := h.resolvedUserDTO(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	for field, value := range patch {
		if string(value) == "null" {
			switch field {
			case "audiolanguagepreference", "subtitlelanguagepreference", "castreceiverid":
				// Unmarshalling null into an existing string leaves it unchanged.
				patch[field] = json.RawMessage(`""`)
			default:
				writeError(w, 400, "BadRequest", "Configuration fields cannot be null")
				return
			}
		}
	}
	// Jellyfin Web saves the whole configuration from every settings page, so
	// an unchanged SubtitleMode must not rewrite the canonical settings: native
	// "auto" with forced subtitles hidden reads as Smart, which writes back as
	// auto with forced subtitles shown.
	currentSubtitleMode := dto.Configuration.SubtitleMode
	currentLanguages := map[string]string{
		"audiolanguagepreference":    dto.Configuration.AudioLanguagePreference,
		"subtitlelanguagepreference": dto.Configuration.SubtitleLanguagePreference,
	}
	raw, _ := json.Marshal(patch)
	if err := json.Unmarshal(raw, &dto.Configuration); err != nil {
		writeError(w, 400, "BadRequest", "Invalid configuration")
		return
	}
	values := map[string]json.RawMessage{}
	for field, key := range map[string]string{"audiolanguagepreference": settingskeys.PlaybackAudioLanguage, "subtitlelanguagepreference": settingskeys.PlaybackSubtitleLanguage, "enablenextepisodeautoplay": settingskeys.PlaybackAutoPlayNext, "subtitlemode": settingskeys.PlaybackSubtitleMode} {
		value, ok := patch[field]
		if !ok {
			continue
		}
		if field == "subtitlemode" {
			// Each Jellyfin mode sets both canonical settings, so the mode reads
			// back unchanged and native clients see the same behavior.
			mode, showForced, ok := compatNativeSubtitleSettings(dto.Configuration.SubtitleMode)
			if !ok {
				writeError(w, 400, "BadRequest", "Unsupported subtitle mode")
				return
			}
			if dto.Configuration.SubtitleMode == currentSubtitleMode {
				continue
			}
			value, _ = json.Marshal(mode)
			values[settingskeys.PlaybackShowForcedSubtitles], _ = json.Marshal(showForced)
		} else if field != "enablenextepisodeautoplay" {
			var tag string
			if err := json.Unmarshal(value, &tag); err != nil {
				writeError(w, 400, "BadRequest", "Invalid language")
				return
			}
			// Echoing a base-language choice must not flatten a native region or
			// script preference when Jellyfin Web saves an unrelated setting.
			if tag != "" && tag == currentLanguages[field] {
				continue
			}
			if tag == "" {
				value = json.RawMessage("null")
			} else if field == "audiolanguagepreference" && strings.EqualFold(strings.TrimSpace(tag), compatAudioOriginalLanguage) {
				value, _ = json.Marshal(playback.OriginalLanguageTag)
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
