package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SubtitleAIHandler exposes on-demand AI subtitle translation backed by the
// configured OpenAI-compatible engine. Generated tracks are stored as ordinary
// downloaded subtitles, so they reach every client through the existing
// subtitle pipeline.
type SubtitleAIHandler struct {
	service        *ai.Service
	FileAuthorizer *MediaFileAuthorizer
	// StoreProvider resolves household profiles for the transcription quota
	// exemption check; when nil the whole admin account is exempt.
	StoreProvider userstore.UserStoreProvider
}

// NewSubtitleAIHandler creates a handler backed by the given service.
func NewSubtitleAIHandler(service *ai.Service) *SubtitleAIHandler {
	return &SubtitleAIHandler{service: service}
}

// authorizeMediaFileAccess verifies the caller may access the given media file.
// Shared by the subtitle handlers so authorization stays in one place.
func authorizeMediaFileAccess(w http.ResponseWriter, r *http.Request, authorizer *MediaFileAuthorizer, fileID int) bool {
	if authorizer == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Media file authorization is not configured")
		return false
	}
	if _, err := authorizer.Authorize(r, fileID); err != nil {
		switch {
		case errors.Is(err, catalog.ErrItemNotFound), errors.Is(err, catalog.ErrEpisodeNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize media file")
		}
		return false
	}
	return true
}

type translateSubtitleRequest struct {
	MediaFileID int `json:"media_file_id"`
	// Kind selects the job: "translate" (default), "transcribe", or
	// "transcribe_translate". For ASR kinds, source_index is the audio track
	// index (-1 = default track) and target_language is optional for plain
	// transcribe (acts as a language hint).
	Kind           string  `json:"kind"`
	SourceIndex    int     `json:"source_index"`
	SourceLanguage string  `json:"source_language"`
	TargetLanguage string  `json:"target_language"`
	SessionID      string  `json:"session_id"`
	StartPosition  float64 `json:"start_position"`
}

// HandleStatus reports whether AI subtitle translation / ASR generation are
// available, so the player can show or hide the entry points.
// GET /api/v1/subtitles/ai/status
func (h *SubtitleAIHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":            h.service.Enabled(),
		"transcribe_enabled": h.service.TranscribeEnabled(),
	})
}

// WriteSubtitleAIDisabledStatus answers the AI status capability probe with a
// 200 {"enabled": false} when no AI handler is wired, so the client gets a clean
// negative instead of a 404 (the 2-segment /ai/status path is not shadowed by the
// 1-segment /{media_file_id} route — they never compete in chi's router).
func WriteSubtitleAIDisabledStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "transcribe_enabled": false})
}

// HandleTranslate enqueues a translation job. POST /api/v1/subtitles/ai/translate
func (h *SubtitleAIHandler) HandleTranslate(w http.ResponseWriter, r *http.Request) {
	var req translateSubtitleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid request body")
		return
	}
	if req.MediaFileID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "media_file_id is required")
		return
	}
	kind := ai.JobKind(req.Kind)
	if kind == "" {
		kind = ai.JobKindTranslate
	}
	// A plain transcribe produces a track in the spoken language, so the
	// target is optional; every other kind needs one.
	if req.TargetLanguage == "" && kind != ai.JobKindTranscribe {
		writeError(w, http.StatusBadRequest, "bad_request", "target_language is required")
		return
	}

	if !authorizeMediaFileAccess(w, r, h.FileAuthorizer, req.MediaFileID) {
		return
	}

	var requestedBy *int
	if userID := apimw.GetUserID(r.Context()); userID != 0 {
		requestedBy = &userID
	}

	job, err := h.service.Enqueue(r.Context(), ai.JobRequest{
		MediaFileID:    req.MediaFileID,
		Kind:           kind,
		SourceIndex:    req.SourceIndex,
		SourceLanguage: req.SourceLanguage,
		TargetLanguage: req.TargetLanguage,
		RequestedBy:    requestedBy,
		QuotaExempt:    h.quotaExempt(r),
		SessionID:      req.SessionID,
		StartPosition:  req.StartPosition,
	})
	if err != nil {
		switch {
		case errors.Is(err, ai.ErrEngineNotConfigured):
			writeError(w, http.StatusServiceUnavailable, "not_configured",
				"AI subtitle translation is not configured on this server")
		case errors.Is(err, ai.ErrQuotaExceeded):
			writeError(w, http.StatusTooManyRequests, "quota_exceeded", quotaExceededMessage(err))
		case errors.Is(err, ai.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		default:
			slog.ErrorContext(r.Context(), "failed to enqueue subtitle translation", "component", "api",
				"media_file_id", req.MediaFileID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start translation")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// quotaExempt reports whether the caller is exempt from the transcription
// quota: an admin account acting as itself (no profile context) or through
// its primary (household parent) profile. Other profiles on an admin account
// — e.g. a kid's profile — stay subject to the quota even though the account
// holds the admin role. X-Profile-Id is client-asserted, so this is a
// budgeting gate, not a security boundary; the quota itself is account-scoped.
func (h *SubtitleAIHandler) quotaExempt(r *http.Request) bool {
	ctx := r.Context()
	if !apimw.IsAdmin(ctx) {
		return false
	}
	profileID := apimw.GetProfileID(ctx)
	if profileID == "" {
		profileID = r.Header.Get("X-Profile-Id")
	}
	if profileID == "" || h.StoreProvider == nil {
		return true
	}
	store, err := h.StoreProvider.ForUser(ctx, apimw.GetUserID(ctx))
	if err != nil {
		return false // fail closed: the quota still applies
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil || profile == nil {
		return false
	}
	return profile.IsPrimary
}

// quotaExceededMessage turns a quota error into a user-facing message with the
// limit and window when available.
func quotaExceededMessage(err error) string {
	var qe *ai.QuotaExceededError
	if errors.As(err, &qe) {
		return fmt.Sprintf(
			"You've reached your transcription limit (%d per %s). Try again later or ask an admin to raise it.",
			qe.Limit, qe.Period)
	}
	return "You've reached your transcription limit. Try again later or ask an admin to raise it."
}

// HandleQuota reports the calling user's transcription quota usage, so the
// player can show how many jobs they have left before they start one.
// GET /api/v1/subtitles/ai/quota
func (h *SubtitleAIHandler) HandleQuota(w http.ResponseWriter, r *http.Request) {
	quota, err := h.service.TranscribeQuota(r.Context(), apimw.GetUserID(r.Context()), h.quotaExempt(r))
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to load transcription quota", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load transcription quota")
		return
	}
	writeJSON(w, http.StatusOK, quota)
}

// HandleGetJob returns a job's current state. GET /api/v1/subtitles/ai/jobs/{job_id}
func (h *SubtitleAIHandler) HandleGetJob(w http.ResponseWriter, r *http.Request) {
	job, ok := h.loadAuthorizedJob(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

// HandleCancelJob cancels a job. POST /api/v1/subtitles/ai/jobs/{job_id}/cancel
func (h *SubtitleAIHandler) HandleCancelJob(w http.ResponseWriter, r *http.Request) {
	job, ok := h.loadAuthorizedJob(w, r)
	if !ok {
		return
	}
	if err := h.service.Cancel(r.Context(), job.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to cancel job")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleListJobs lists recent jobs for a media file.
// GET /api/v1/subtitles/ai/jobs?media_file_id=N
func (h *SubtitleAIHandler) HandleListJobs(w http.ResponseWriter, r *http.Request) {
	mediaFileID, err := strconv.Atoi(r.URL.Query().Get("media_file_id"))
	if err != nil || mediaFileID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid or missing media_file_id")
		return
	}
	if !authorizeMediaFileAccess(w, r, h.FileAuthorizer, mediaFileID) {
		return
	}
	jobs, err := h.service.ListJobs(r.Context(), mediaFileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_error", "Failed to list jobs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// loadAuthorizedJob parses the job_id param, loads the job, and authorizes
// access against its media file. It writes the error response on failure.
func (h *SubtitleAIHandler) loadAuthorizedJob(w http.ResponseWriter, r *http.Request) (*ai.Job, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "job_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid job ID")
		return nil, false
	}
	job, err := h.service.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, ai.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Job not found")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load job")
		return nil, false
	}
	if !authorizeMediaFileAccess(w, r, h.FileAuthorizer, job.MediaFileID) {
		return nil, false
	}
	return job, true
}
