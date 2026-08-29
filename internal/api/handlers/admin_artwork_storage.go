package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type artworkStorageJobRepository interface {
	Create(ctx context.Context, input adminjob.CreateJobInput) (*models.AdminJob, error)
}

type artworkStorageAccountant interface {
	Accounting(ctx context.Context) (metadata.ArtworkStorageAccounting, error)
}

type artworkStorageRebuilder interface {
	RebuildEmpty(ctx context.Context) (metadata.ArtworkStorageAccounting, error)
}

type AdminArtworkStorageHandler struct {
	accounting artworkStorageAccountant
	rebuilder  artworkStorageRebuilder
	jobs       artworkStorageJobRepository
}

func NewAdminArtworkStorageHandler(accounting artworkStorageAccountant, jobs artworkStorageJobRepository) *AdminArtworkStorageHandler {
	if accounting == nil || jobs == nil {
		return nil
	}
	handler := &AdminArtworkStorageHandler{accounting: accounting, jobs: jobs}
	handler.rebuilder, _ = accounting.(artworkStorageRebuilder)
	return handler
}

func (h *AdminArtworkStorageHandler) HandleRebuild(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.rebuilder == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Artwork store rebuild is not configured")
		return
	}
	state, err := h.rebuilder.RebuildEmpty(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, artworkstore.ErrRebuildUnsupported):
			writeError(w, http.StatusUnprocessableEntity, "unsupported_backend", "Artwork store rebuild is supported only for local storage")
		case errors.Is(err, artworkstore.ErrStoreNotEmpty):
			writeError(w, http.StatusConflict, "store_not_empty", "Artwork store rebuild requires an empty local root")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to rebuild artwork storage")
		}
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (h *AdminArtworkStorageHandler) HandleStorage(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.accounting == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Artwork storage accounting is not configured")
		return
	}
	accounting, err := h.accounting.Accounting(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load artwork storage accounting")
		return
	}
	writeJSON(w, http.StatusOK, accounting)
}

func (h *AdminArtworkStorageHandler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	h.createJob(w, r, adminjob.CreateJobInput{
		JobType:          adminjob.JobTypeArtworkStorageRefresh,
		CreatedByUserID:  currentAdminUserID(r),
		RequestPayload:   map[string]any{},
		Message:          "Queued artwork storage refresh",
		ResumeCheckpoint: true,
	}, "An artwork storage refresh or purge is already queued or running")
}

func (h *AdminArtworkStorageHandler) HandleImport(w http.ResponseWriter, r *http.Request) {
	h.createJob(w, r, adminjob.CreateJobInput{
		JobType: adminjob.JobTypeArtworkStorageImport, CreatedByUserID: currentAdminUserID(r),
		RequestPayload: map[string]any{}, Message: "Queued portable artwork store import", ResumeCheckpoint: true,
	}, "An artwork storage job is already queued or running")
}

func (h *AdminArtworkStorageHandler) HandlePurge(w http.ResponseWriter, r *http.Request) {
	var req adminjob.ArtworkPurgeRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid artwork purge request")
		return
	}
	if err := (&req).Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	h.createJob(w, r, adminjob.CreateJobInput{
		JobType:          adminjob.JobTypeArtworkPurge,
		CreatedByUserID:  currentAdminUserID(r),
		RequestPayload:   req,
		DryRun:           req.DryRun,
		Message:          "Queued artwork storage purge",
		ResumeCheckpoint: true,
	}, "An artwork storage refresh or purge is already queued or running")
}

func (h *AdminArtworkStorageHandler) createJob(
	w http.ResponseWriter,
	r *http.Request,
	input adminjob.CreateJobInput,
	conflictMessage string,
) {
	if h == nil || h.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Artwork storage jobs are not configured")
		return
	}
	job, err := h.jobs.Create(r.Context(), input)
	if err != nil {
		var conflict *adminjob.ActiveJobConflictError
		if errors.As(err, &conflict) {
			writeAdminJobConflict(w, conflictMessage, conflict.Job, NewAdminJobsHandler(nil, nil), r)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to queue artwork storage job")
		return
	}
	writeJSON(w, http.StatusAccepted, adminJobToResponse(r, job, nil))
}
