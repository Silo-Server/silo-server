package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
)

// MetadataAIHandler exposes AI translation of catalog descriptions into the
// localization tables. Routes are mounted under the per-item metadata
// curation guard, so {id} is always an item content ID the caller may curate.
type MetadataAIHandler struct {
	service *translation.Service
}

// NewMetadataAIHandler creates a handler backed by the given service.
func NewMetadataAIHandler(service *translation.Service) *MetadataAIHandler {
	return &MetadataAIHandler{service: service}
}

// HandleStatus reports whether metadata AI translation is available, so the
// metadata editor can show or hide the entry point.
// GET /api/v1/metadata/ai/status
func (h *MetadataAIHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": h.service.Enabled()})
}

// WriteMetadataAIDisabledStatus answers the status probe with a clean negative
// when no metadata AI handler is wired.
func WriteMetadataAIDisabledStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
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
