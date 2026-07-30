package handlers

import (
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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	pref, err := store.GetAudioPreference(r.Context(), profileID, seriesID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get audio preference")
		return
	}

	if pref == nil {
		writeError(w, http.StatusNotFound, "not_found", "Audio preference not found")
		return
	}

	writeJSON(w, http.StatusOK, toAudioPrefResponse(*pref))
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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	pref := userstore.AudioPreference{
		ProfileID:       profileID,
		SeriesID:        seriesID,
		AudioTrackIndex: req.AudioTrackIndex,
		AudioLanguage:   req.AudioLanguage,
		TrackSignature:  req.TrackSignature,
	}
	language := req.AudioLanguage
	sync, err := appendStringSync(nil, settingskeys.PlaybackAudioLanguage, &language)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	if err := applyLegacyPreferenceSettingsSync(r.Context(), store, h.EventsHub, userID,
		userstore.SettingIdentity{
			Scope: settingscontract.ScopeProfileSeries, ProfileID: profileID, SeriesID: seriesID,
		}, sync, func(tx userstore.PreferenceSettingsWriter) error {
			return tx.SetAudioPreference(r.Context(), pref)
		}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store audio preference")
		return
	}

	w.WriteHeader(http.StatusNoContent)
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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := applyLegacyPreferenceSettingsSync(r.Context(), store, h.EventsHub, userID,
		userstore.SettingIdentity{
			Scope: settingscontract.ScopeProfileSeries, ProfileID: profileID, SeriesID: seriesID,
		}, []profileSettingSync{{key: settingskeys.PlaybackAudioLanguage}},
		func(tx userstore.PreferenceSettingsWriter) error {
			return tx.DeleteAudioPreference(r.Context(), profileID, seriesID)
		}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete audio preference")
		return
	}

	w.WriteHeader(http.StatusNoContent)
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
