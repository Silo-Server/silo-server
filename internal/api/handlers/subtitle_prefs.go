package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SubtitlePrefHandler handles per-series subtitle preference endpoints.
type SubtitlePrefHandler struct {
	storeProvider userstore.UserStoreProvider
}

// NewSubtitlePrefHandler creates a new SubtitlePrefHandler.
func NewSubtitlePrefHandler(provider userstore.UserStoreProvider) *SubtitlePrefHandler {
	return &SubtitlePrefHandler{storeProvider: provider}
}

// --- Request/Response types ---

type setSubtitlePrefRequest struct {
	SubtitleLanguage     string                            `json:"subtitle_language"`
	SubtitleTrackIndex   int                               `json:"subtitle_track_index"`
	ExternalSubtitlePath string                            `json:"external_subtitle_path,omitempty"`
	SubtitleMode         string                            `json:"subtitle_mode"`
	TrackSignature       *userstore.SubtitleTrackSignature `json:"track_signature,omitempty"`
	ShowForcedSubtitles  *bool                             `json:"show_forced_subtitles,omitempty"`
}

type subtitlePrefResponse struct {
	ProfileID            string                            `json:"profile_id"`
	SeriesID             string                            `json:"series_id"`
	SubtitleLanguage     string                            `json:"subtitle_language"`
	SubtitleTrackIndex   int                               `json:"subtitle_track_index"`
	ExternalSubtitlePath string                            `json:"external_subtitle_path,omitempty"`
	SubtitleMode         string                            `json:"subtitle_mode"`
	TrackSignature       *userstore.SubtitleTrackSignature `json:"track_signature,omitempty"`
	ShowForcedSubtitles  *bool                             `json:"show_forced_subtitles,omitempty"`
	UpdatedAt            string                            `json:"updated_at"`
}

// --- Handler methods ---

// HandleGetSubtitlePref handles GET /subtitle-prefs/{series_id}.
func (h *SubtitlePrefHandler) HandleGetSubtitlePref(w http.ResponseWriter, r *http.Request) {
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

	pref, err := store.GetSubtitlePreference(r.Context(), profileID, seriesID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get subtitle preference")
		return
	}

	if pref == nil {
		writeError(w, http.StatusNotFound, "not_found", "Subtitle preference not found")
		return
	}

	writeJSON(w, http.StatusOK, toSubtitlePrefResponse(*pref))
}

// HandleSetSubtitlePref handles PUT /subtitle-prefs/{series_id}.
func (h *SubtitlePrefHandler) HandleSetSubtitlePref(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	seriesID := chi.URLParam(r, "series_id")

	if seriesID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID is required")
		return
	}

	var req setSubtitlePrefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	pref := userstore.SubtitlePreference{
		ProfileID:            profileID,
		SeriesID:             seriesID,
		SubtitleLanguage:     req.SubtitleLanguage,
		SubtitleTrackIndex:   req.SubtitleTrackIndex,
		ExternalSubtitlePath: req.ExternalSubtitlePath,
		SubtitleMode:         req.SubtitleMode,
		TrackSignature:       req.TrackSignature,
	}
	if req.ShowForcedSubtitles != nil {
		pref.ShowForcedSubtitles = *req.ShowForcedSubtitles
		pref.HasShowForcedSubtitles = true
	} else {
		// This endpoint replaces the combined subtitle-preference row. Preserve
		// an existing forced-subtitle override when clients update only the track
		// selection; otherwise an omitted optional field silently resets it.
		existing, getErr := store.GetSubtitlePreference(r.Context(), profileID, seriesID)
		if getErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to preserve subtitle preference")
			return
		}
		if existing != nil && existing.HasShowForcedSubtitles {
			pref.ShowForcedSubtitles = existing.ShowForcedSubtitles
			pref.HasShowForcedSubtitles = true
		}
	}

	if err := store.SetSubtitlePreference(r.Context(), pref); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to set subtitle preference")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteSubtitlePref handles DELETE /subtitle-prefs/{series_id}.
func (h *SubtitlePrefHandler) HandleDeleteSubtitlePref(w http.ResponseWriter, r *http.Request) {
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

	if err := store.DeleteSubtitlePreference(r.Context(), profileID, seriesID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete subtitle preference")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Helpers ---

func toSubtitlePrefResponse(p userstore.SubtitlePreference) subtitlePrefResponse {
	resp := subtitlePrefResponse{
		ProfileID:            p.ProfileID,
		SeriesID:             p.SeriesID,
		SubtitleLanguage:     p.SubtitleLanguage,
		SubtitleTrackIndex:   p.SubtitleTrackIndex,
		ExternalSubtitlePath: p.ExternalSubtitlePath,
		SubtitleMode:         p.SubtitleMode,
		TrackSignature:       p.TrackSignature,
		UpdatedAt:            p.UpdatedAt,
	}
	if p.HasShowForcedSubtitles {
		resp.ShowForcedSubtitles = boolPtr(p.ShowForcedSubtitles)
	}
	return resp
}
