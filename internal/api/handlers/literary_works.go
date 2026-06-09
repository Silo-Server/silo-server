package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/literaryworks"
)

type LiteraryWorkService interface {
	GetWork(ctx context.Context, workID string, filter catalog.AccessFilter) (*literaryworks.DetailResponse, error)
}

type LiteraryWorkHandler struct {
	Service LiteraryWorkService
}

func (h *LiteraryWorkHandler) HandleGetWork(w http.ResponseWriter, r *http.Request) {
	workID := strings.TrimSpace(chi.URLParam(r, "work_id"))
	if workID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "work_id is required")
		return
	}
	if h == nil || h.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Literary works are not configured")
		return
	}
	resp, err := h.Service.GetWork(r.Context(), workID, requestAccessFilter(r))
	if err != nil {
		if errors.Is(err, literaryworks.ErrWorkNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Work not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load work")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
