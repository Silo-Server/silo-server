package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
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

// HandleGetAudioPref handles GET /audio-prefs/{series_id}.
func (h *AudioPrefHandler) HandleGetAudioPref(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	seriesID := chi.URLParam(r, "series_id")

	if seriesID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID is required")
		return
	}

	pref, err := h.GetAudioPreference(r.Context(), userID, profileID, seriesID)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toAudioPrefResponse(pref))
}

// GetAudioPreference reads the legacy preference used by v1. A missing
// series preference returns a 404 APIError.
func (h *AudioPrefHandler) GetAudioPreference(ctx context.Context, userID int, profileID, seriesID string) (userstore.AudioPreference, error) {
	var none userstore.AudioPreference
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	pref, err := store.GetAudioPreference(ctx, profileID, seriesID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to get audio preference")
	}
	if pref == nil {
		return none, apiError(http.StatusNotFound, "not_found", "Audio preference not found")
	}
	return *pref, nil
}

// GetAudioPreferenceCanonical keeps the legacy track identity and overlays the
// canonical series language for v2. An absent canonical row clears the language;
// a missing legacy track row remains a 404. v1 keeps its legacy-only read.
func (h *AudioPrefHandler) GetAudioPreferenceCanonical(ctx context.Context, userID int, profileID, seriesID string) (userstore.AudioPreference, error) {
	var none userstore.AudioPreference
	pref, err := h.GetAudioPreference(ctx, userID, profileID, seriesID)
	if err != nil {
		return none, err
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	var language *string
	languageAt, err := canonicalMemberAt(ctx, store, userstore.SettingIdentity{
		Scope: settingscontract.ScopeProfileSeries, ProfileID: profileID, SeriesID: seriesID,
	}, settingskeys.PlaybackAudioLanguage, &language)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to get audio preference")
	}
	pref.AudioLanguage = ""
	if language != nil {
		pref.AudioLanguage = *language
	}
	pref.UpdatedAt = newestUpdatedAt(pref.UpdatedAt, languageAt)
	return pref, nil
}

// HandleSetAudioPref handles PUT /audio-prefs/{series_id}.
func (h *AudioPrefHandler) HandleSetAudioPref(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	seriesID := chi.URLParam(r, "series_id")

	if seriesID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID is required")
		return
	}

	var req setAudioPrefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if err := h.SetAudioPreference(r.Context(), userID, userstore.AudioPreference{
		ProfileID:       profileID,
		SeriesID:        seriesID,
		AudioTrackIndex: req.AudioTrackIndex,
		AudioLanguage:   req.AudioLanguage,
		TrackSignature:  req.TrackSignature,
	}); err != nil {
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
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

// HandleDeleteAudioPref handles DELETE /audio-prefs/{series_id}.
func (h *AudioPrefHandler) HandleDeleteAudioPref(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	seriesID := chi.URLParam(r, "series_id")

	if seriesID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID is required")
		return
	}

	if err := h.DeleteAudioPreference(r.Context(), userID, profileID, seriesID); err != nil {
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
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

func toAudioPrefResponse(p userstore.AudioPreference) audioPrefResponse {
	return audioPrefResponse{
		ProfileID:       p.ProfileID,
		SeriesID:        p.SeriesID,
		AudioTrackIndex: p.AudioTrackIndex,
		AudioLanguage:   p.AudioLanguage,
		TrackSignature:  p.TrackSignature,
		UpdatedAt:       p.UpdatedAt,
	}
}
