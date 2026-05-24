package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type audiobookProgressRequest struct {
	PositionSeconds float64 `json:"position_seconds"`
	MediaFileID     int     `json:"media_file_id,omitempty"`
}

// HandleReportAudiobookProgress serves POST /api/v1/audiobooks/{id}/progress.
// UPSERTs into user_watch_progress for the caller's (user_id, profile_id, content_id).
// Duration is unknown at the audiobook level (it lives on the media_file), so we
// pass 0 and let the store compute completion via thresholds against position only.
func (h *AudiobookHandler) HandleReportAudiobookProgress(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}

	var req audiobookProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}
	if req.PositionSeconds < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "position_seconds must be >= 0")
		return
	}

	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if profileID == "" {
		profileID = r.Header.Get("X-Profile-Id")
	}
	if userID == 0 || profileID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	if h.StoreProvider == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "store unavailable")
		return
	}
	store, err := h.StoreProvider.ForUser(r.Context(), userID)
	if err != nil || store == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "store unavailable")
		return
	}

	// Duration is not carried in the progress body; pass 0 so the store
	// uses position-only heuristics. ProgressThresholds zero value means
	// "use defaults" (90% watched, 5% min-resume).
	if err := store.SetProgress(r.Context(), profileID, contentID, req.PositionSeconds, 0, userstore.ProgressThresholds{}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "save progress failed")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
