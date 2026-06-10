package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
)

// MetadataAIHandler exposes AI translation of catalog descriptions into the
// localization tables. The admin routes are mounted under the per-item
// metadata curation guard; the on-view route is viewer-facing and enforces
// item access itself.
type MetadataAIHandler struct {
	service *translation.Service
	// ItemAccess authorizes the viewer-facing on-view route; nil disables it.
	ItemAccess *catalog.ItemRepository
}

// NewMetadataAIHandler creates a handler backed by the given service.
func NewMetadataAIHandler(service *translation.Service) *MetadataAIHandler {
	return &MetadataAIHandler{service: service}
}

// HandleStatus reports whether metadata AI translation is available and the
// viewer-facing on-view mode, so the metadata editor and detail pages can
// show or hide their entry points.
// GET /api/v1/metadata/ai/status
func (h *MetadataAIHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": h.service.Enabled(),
		"on_view": h.service.OnViewMode(),
	})
}

// WriteMetadataAIDisabledStatus answers the status probe with a clean negative
// when no metadata AI handler is wired.
func WriteMetadataAIDisabledStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "on_view": "off"})
}

type translateDescriptionRequest struct {
	// TargetLanguage echoes the detail response's pending_translation_language.
	TargetLanguage string `json:"target_language"`
}

// HandleTranslateOnView is the viewer-facing on-demand description
// translation: any profile that can access the item may request its
// descriptions in the language the detail response reported missing. Gated by
// metadata_ai.on_view; duplicate viewers collapse onto one job and recently
// failed targets are not retried (cooldown in the service).
// POST /api/v1/items/{id}/translate-description
func (h *MetadataAIHandler) HandleTranslateOnView(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	var req translateDescriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid request body")
		return
	}
	if req.TargetLanguage == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "target_language is required")
		return
	}

	scope, ok := access.GetScope(r.Context())
	if !ok || h.ItemAccess == nil {
		writeError(w, http.StatusForbidden, "forbidden", "Viewer access is required")
		return
	}
	filter := catalog.AccessFilter{
		AllowedLibraryIDs:  scope.AllowedLibraryIDs,
		DisabledLibraryIDs: scope.DisabledLibraryIDs,
		MaxContentRating:   scope.MaxContentRating,
		UserID:             scope.UserID,
		ProfileID:          scope.ProfileID,
	}
	if err := h.ItemAccess.EnsureAccessible(r.Context(), contentID, filter); err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize item")
		return
	}

	var requestedBy *int
	if userID := apimw.GetUserID(r.Context()); userID != 0 {
		requestedBy = &userID
	}

	job, err := h.service.RequestOnView(r.Context(), contentID, req.TargetLanguage, requestedBy)
	if err != nil {
		switch {
		case errors.Is(err, translation.ErrNotConfigured):
			writeError(w, http.StatusServiceUnavailable, "not_configured",
				"On-view translation is not enabled on this server")
		case errors.Is(err, translation.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		default:
			slog.Error("failed to request on-view translation",
				"content_id", contentID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start translation")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

type translateMetadataRequest struct {
	TargetLanguage  string `json:"target_language"`
	IncludeChildren *bool  `json:"include_children"` // default true
	Force           bool   `json:"force"`
}

// HandleTranslate enqueues a translation job for an item.
// POST /api/v1/admin/items/{id}/metadata-translation
func (h *MetadataAIHandler) HandleTranslate(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	var req translateMetadataRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid request body")
		return
	}
	if req.TargetLanguage == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "target_language is required")
		return
	}
	includeChildren := true
	if req.IncludeChildren != nil {
		includeChildren = *req.IncludeChildren
	}

	var requestedBy *int
	if userID := apimw.GetUserID(r.Context()); userID != 0 {
		requestedBy = &userID
	}

	job, err := h.service.Enqueue(r.Context(), translation.JobRequest{
		TargetKind:      translation.TargetItem,
		ContentID:       contentID,
		TargetLanguage:  req.TargetLanguage,
		IncludeChildren: includeChildren,
		Force:           req.Force,
		RequestedBy:     requestedBy,
	})
	if err != nil {
		switch {
		case errors.Is(err, translation.ErrNotConfigured):
			writeError(w, http.StatusServiceUnavailable, "not_configured",
				"Metadata AI translation is not configured on this server")
		case errors.Is(err, translation.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		default:
			slog.Error("failed to enqueue metadata translation",
				"content_id", contentID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start translation")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// HandleListJobs lists recent translation jobs for an item; the metadata
// editor polls this for progress.
// GET /api/v1/admin/items/{id}/metadata-translation/jobs
func (h *MetadataAIHandler) HandleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.service.ListJobs(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_error", "Failed to list jobs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// HandleCancelJob cancels a job belonging to the item in the URL.
// POST /api/v1/admin/items/{id}/metadata-translation/jobs/{job_id}/cancel
func (h *MetadataAIHandler) HandleCancelJob(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(chi.URLParam(r, "job_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid job ID")
		return
	}
	job, err := h.service.GetJob(r.Context(), jobID)
	if err != nil {
		if errors.Is(err, translation.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load job")
		return
	}
	// The curation guard authorized {id}; the job must belong to it.
	if job.ContentID != chi.URLParam(r, "id") {
		writeError(w, http.StatusNotFound, "not_found", "Job not found")
		return
	}
	if err := h.service.Cancel(r.Context(), jobID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to cancel job")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
