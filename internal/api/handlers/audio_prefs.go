package handlers

import (
	"context"
	"errors"
	"net/http"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// AudioPrefHandler handles per-series audio preference endpoints. Concrete
// track identity remains in the specialized table; the language is mirrored
// to the canonical profile_series row consumed by playback.
type AudioPrefHandler struct {
	storeProvider userstore.UserStoreProvider
	EventsHub     *evt.Hub
}

// NewAudioPrefHandler creates a new AudioPrefHandler.
func NewAudioPrefHandler(provider userstore.UserStoreProvider) *AudioPrefHandler {
	return &AudioPrefHandler{storeProvider: provider}
}

// --- Request/Response types ---

type setAudioPrefRequest struct {
	AudioTrackIndex int                            `json:"audio_track_index"`
	AudioLanguage   string                         `json:"audio_language"`
	TrackSignature  *userstore.AudioTrackSignature `json:"track_signature,omitempty"`
}

type audioPrefResponse struct {
	ProfileID       string                         `json:"profile_id"`
	SeriesID        string                         `json:"series_id"`
	AudioTrackIndex int                            `json:"audio_track_index"`
	AudioLanguage   string                         `json:"audio_language"`
	TrackSignature  *userstore.AudioTrackSignature `json:"track_signature,omitempty"`
	UpdatedAt       string                         `json:"updated_at"`
}

// --- Handler methods ---

// GetAudioPreferenceCanonical keeps the legacy track identity and overlays the
// canonical series language for v2. An absent canonical row clears the language;
// a missing legacy track row remains a 404. v1 keeps its legacy-only read.
func (h *AudioPrefHandler) GetAudioPreferenceCanonical(ctx context.Context, userID int, profileID, seriesID string) (userstore.AudioPreference, error) {
	var pref userstore.AudioPreference
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return pref, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	snapshots, ok := store.(userstore.PreferenceSettingsSnapshotter)
	if !ok {
		return pref, apiError(http.StatusInternalServerError, "internal_error", "Preference snapshots unavailable")
	}
	err = snapshots.WithPreferenceSettingsSnapshot(ctx, func(reader userstore.PreferenceSettingsReader) error {
		legacy, err := reader.GetAudioPreference(ctx, profileID, seriesID)
		if err != nil {
			return err
		}
		if legacy == nil {
			return apiError(http.StatusNotFound, "not_found", "Audio preference not found")
		}
		pref = *legacy
		var language *string
		languageAt, err := canonicalMemberAt(ctx, reader, userstore.SettingIdentity{
			Scope: settingscontract.ScopeProfileSeries, ProfileID: profileID, SeriesID: seriesID,
		}, settingskeys.PlaybackAudioLanguage, &language)
		if err != nil {
			return apiError(http.StatusInternalServerError, "internal_error", "Failed to get audio preference")
		}
		pref.AudioLanguage = ""
		if language != nil {
			pref.AudioLanguage = *language
		}
		pref.UpdatedAt = newestUpdatedAt(pref.UpdatedAt, languageAt)

		return nil
	})
	if err != nil {
		if _, ok := errors.AsType[*APIError](err); ok {
			return userstore.AudioPreference{}, err
		}
		return userstore.AudioPreference{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to get audio preference")
	}
	return pref, nil
}

// SetAudioPreference is the write v1 PUT /audio-prefs/{series_id} and v2
// updateAudioPreference share: the legacy row and the canonical
// profile_series audio-language row commit together, and a
// user_settings.changed event follows each canonical row that moved. A
// failure is an *APIError; a language the canonical contract refuses is a
// 400 bad_request naming audio_language.
func (h *AudioPrefHandler) SetAudioPreference(ctx context.Context, userID int, pref userstore.AudioPreference) error {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	language := pref.AudioLanguage
	sync, err := appendStringSync(nil, settingskeys.PlaybackAudioLanguage, &language)
	if err != nil {
		return fieldError("audio_language", err.Error())
	}
	if err := applyLegacyPreferenceSettingsSync(ctx, store, h.EventsHub, userID,
		userstore.SettingIdentity{
			Scope: settingscontract.ScopeProfileSeries, ProfileID: pref.ProfileID, SeriesID: pref.SeriesID,
		}, sync, func(tx userstore.PreferenceSettingsWriter) error {
			return tx.SetAudioPreference(ctx, pref)
		}); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to store audio preference")
	}
	return nil
}

// DeleteAudioPreference is the delete v1 DELETE /audio-prefs/{series_id} and
// v2 deleteAudioPreference share. Deleting a preference that does not exist
// succeeds. A failure is an *APIError.
func (h *AudioPrefHandler) DeleteAudioPreference(ctx context.Context, userID int, profileID, seriesID string) error {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := applyLegacyPreferenceSettingsSync(ctx, store, h.EventsHub, userID,
		userstore.SettingIdentity{
			Scope: settingscontract.ScopeProfileSeries, ProfileID: profileID, SeriesID: seriesID,
		}, []profileSettingSync{{key: settingskeys.PlaybackAudioLanguage}},
		func(tx userstore.PreferenceSettingsWriter) error {
			return tx.DeleteAudioPreference(ctx, profileID, seriesID)
		}); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete audio preference")
	}
	return nil
}

// --- Helpers ---
