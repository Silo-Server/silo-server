package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type audiobookProgressRequest struct {
	PositionSeconds float64 `json:"position_seconds"`
	MediaFileID     int     `json:"media_file_id,omitempty"`
}

// HandleReportAudiobookProgress serves POST /api/v1/audiobooks/{id}/progress.
// UPSERTs into user_watch_progress for the caller's (user_id, profile_id, content_id).
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

	if h.Items == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "catalog unavailable")
		return
	}
	items, err := h.Items.GetByIDsWithAccess(r.Context(), []string{contentID}, requestAccessFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "check access failed")
		return
	}
	if len(items) == 0 || items[0] == nil || items[0].Type != "audiobook" {
		writeError(w, http.StatusNotFound, "not_found", "audiobook not found")
		return
	}

	durationSeconds, err := h.audiobookProgressDuration(r.Context(), contentID, req.MediaFileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "load files failed")
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

	if err := store.SetProgress(r.Context(), profileID, contentID, req.PositionSeconds, durationSeconds, userstore.ProgressThresholds{}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "save progress failed")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *AudiobookHandler) audiobookProgressDuration(ctx context.Context, contentID string, mediaFileID int) (float64, error) {
	if h == nil || h.Files == nil {
		return 0, nil
	}
	files, err := h.Files.GetByContentID(ctx, contentID)
	if err != nil {
		return 0, err
	}
	return audiobookDurationForProgress(files, mediaFileID), nil
}

func audiobookDurationForProgress(files []*models.MediaFile, mediaFileID int) float64 {
	var total int
	for _, f := range files {
		if f == nil || f.Duration <= 0 {
			continue
		}
		if mediaFileID > 0 && f.ID == mediaFileID {
			return float64(f.Duration)
		}
		total += f.Duration
	}
	if total <= 0 {
		return 0
	}
	return float64(total)
}
