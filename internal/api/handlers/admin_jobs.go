package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

const adminJobDownloadExpiry = 15 * time.Minute

type AdminJobArtifactStore interface {
	Bucket() string
	PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error)
}

type AdminJobsHandler struct {
	repo  *adminjob.Repository
	store AdminJobArtifactStore
}

func NewAdminJobsHandler(repo *adminjob.Repository, store AdminJobArtifactStore) *AdminJobsHandler {
	return &AdminJobsHandler{repo: repo, store: store}
}

type adminJobResponse struct {
	ID                string          `json:"id"`
	JobType           string          `json:"job_type"`
	Status            string          `json:"status"`
	CreatedByUserID   int             `json:"created_by_user_id"`
	RequestPayload    json.RawMessage `json:"request_payload"`
	ResultPayload     json.RawMessage `json:"result_payload"`
	Message           string          `json:"message"`
	ErrorMessage      string          `json:"error_message,omitempty"`
	ProgressCurrent   int             `json:"progress_current"`
	ProgressTotal     int             `json:"progress_total"`
	ArtifactSizeBytes int64           `json:"artifact_size_bytes"`
	PublicURL         string          `json:"public_url,omitempty"`
	RequestedAt       time.Time       `json:"requested_at"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
	HeartbeatAt       *time.Time      `json:"heartbeat_at,omitempty"`
	ExpiresAt         *time.Time      `json:"expires_at,omitempty"`
	PublishedAt       *time.Time      `json:"published_at,omitempty"`
	DownloadURL       string          `json:"download_url,omitempty"`
	DownloadExpiresAt *time.Time      `json:"download_expires_at,omitempty"`
}

type listAdminJobsResponse struct {
	Jobs []adminJobResponse `json:"jobs"`
}

type adminJobConflictResponse struct {
	Error       string            `json:"error"`
	Message     string            `json:"message"`
	ActiveJobID string            `json:"active_job_id,omitempty"`
	ActiveJob   *adminJobResponse `json:"active_job,omitempty"`
}

func (h *AdminJobsHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid limit")
			return
		}
		limit = value
	}

	jobs, err := h.repo.List(r.Context(), adminjob.ListJobsOptions{
		JobType: r.URL.Query().Get("job_type"),
		Limit:   limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list admin jobs")
		return
	}

	respJobs := make([]adminJobResponse, 0, len(jobs))
	for _, job := range jobs {
		respJobs = append(respJobs, adminJobToResponse(r, job, h.store))
	}

	writeJSON(w, http.StatusOK, listAdminJobsResponse{Jobs: respJobs})
}

func (h *AdminJobsHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Job ID is required")
		return
	}

	job, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, adminjob.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load admin job")
		return
	}

	claims := apimw.GetClaims(r.Context())
	if !canReadAdminJob(claims, job) {
		writeError(w, http.StatusForbidden, "forbidden", "Admin access required")
		return
	}

	writeJSON(w, http.StatusOK, adminJobToResponseForClaims(r, job, h.store, claims))
}

func adminJobToResponse(r *http.Request, job *models.AdminJob, store AdminJobArtifactStore) adminJobResponse {
	resp := adminJobResponse{
		ID:                job.ID,
		JobType:           job.JobType,
		Status:            job.Status,
		CreatedByUserID:   job.CreatedByUserID,
		RequestPayload:    ensureJSONPayload(job.RequestPayload),
		ResultPayload:     ensureJSONPayload(job.ResultPayload),
		Message:           job.Message,
		ErrorMessage:      job.ErrorMessage,
		ProgressCurrent:   job.ProgressCurrent,
		ProgressTotal:     job.ProgressTotal,
		ArtifactSizeBytes: job.ArtifactSizeBytes,
		PublicURL:         job.PublicURL,
		RequestedAt:       job.RequestedAt,
		StartedAt:         job.StartedAt,
		CompletedAt:       job.CompletedAt,
		HeartbeatAt:       job.HeartbeatAt,
		ExpiresAt:         job.ExpiresAt,
		PublishedAt:       job.PublishedAt,
	}

	if store != nil && job.Status == adminjob.StatusCompleted && job.ArtifactBucket != "" && job.ArtifactKey != "" {
		expiresAt := time.Now().UTC().Add(adminJobDownloadExpiry)
		url, err := store.PresignGetURL(r.Context(), job.ArtifactBucket, job.ArtifactKey, adminJobDownloadExpiry)
		if err == nil {
			resp.DownloadURL = url
			resp.DownloadExpiresAt = &expiresAt
		}
	}

	return resp
}

func adminJobToResponseForClaims(
	r *http.Request,
	job *models.AdminJob,
	store AdminJobArtifactStore,
	claims *auth.Claims,
) adminJobResponse {
	response := adminJobToResponse(r, job, store)
	sanitizeAdminJobResponseForClaims(&response, claims)
	return response
}

func sanitizeAdminJobResponseForClaims(response *adminJobResponse, claims *auth.Claims) {
	if response == nil || (claims != nil && claims.Role == "admin") {
		return
	}
	response.RequestPayload = json.RawMessage(`{}`)
	response.ResultPayload = sanitizeNonAdminAdminJobResultPayload(response.JobType, response.ResultPayload)
	response.PublicURL = ""
	response.DownloadURL = ""
	response.DownloadExpiresAt = nil
}

func sanitizeNonAdminAdminJobResultPayload(jobType string, payload json.RawMessage) json.RawMessage {
	if jobType != adminjob.JobTypeItemRefresh {
		return json.RawMessage(`{}`)
	}

	var raw map[string]json.RawMessage
	if len(payload) == 0 || json.Unmarshal(payload, &raw) != nil {
		return json.RawMessage(`{}`)
	}

	safe := make(map[string]json.RawMessage)
	copyJSONFields(safe, raw,
		"requested_content_id",
		"refresh_content_id",
		"detail_content_id",
		"matched_files",
	)
	if scanPayload, ok := raw["scan_result"]; ok {
		if scanSummary := sanitizeScanResultPayload(scanPayload); len(scanSummary) > 0 {
			safe["scan_result"] = scanSummary
		}
	}
	if len(safe) == 0 {
		return json.RawMessage(`{}`)
	}
	data, err := json.Marshal(safe)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}

func sanitizeScanResultPayload(payload json.RawMessage) json.RawMessage {
	var raw map[string]json.RawMessage
	if len(payload) == 0 || json.Unmarshal(payload, &raw) != nil {
		return nil
	}
	safe := make(map[string]json.RawMessage)
	copyJSONFields(safe, raw,
		"New",
		"Updated",
		"Unchanged",
		"Missing",
		"FilesDeleted",
		"MembershipsRemoved",
		"ItemsDeleted",
		"Errors",
		"EmptyRootGuarded",
	)
	if len(safe) == 0 {
		return nil
	}
	data, err := json.Marshal(safe)
	if err != nil {
		return nil
	}
	return data
}

func copyJSONFields(dst, src map[string]json.RawMessage, keys ...string) {
	for _, key := range keys {
		if value, ok := src[key]; ok && len(value) > 0 {
			dst[key] = value
		}
	}
}

func writeAdminJobConflict(w http.ResponseWriter, message string, job *models.AdminJob, handler *AdminJobsHandler, r *http.Request) {
	resp := adminJobConflictResponse{
		Error:   "conflict",
		Message: message,
	}
	if job != nil {
		resp.ActiveJobID = job.ID
		response := adminJobToResponse(r, job, handler.store)
		resp.ActiveJob = &response
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(resp)
}

func ensureJSONPayload(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return json.RawMessage(`{}`)
	}
	return data
}

func currentAdminUserID(r *http.Request) int {
	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		return 0
	}
	return claims.UserID
}

func canReadAdminJob(claims *auth.Claims, job *models.AdminJob) bool {
	if claims == nil || job == nil {
		return false
	}
	if claims.Role == "admin" {
		return true
	}
	return job.JobType == adminjob.JobTypeItemRefresh && job.CreatedByUserID == claims.UserID
}
