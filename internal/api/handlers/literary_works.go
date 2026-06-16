package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/literaryworks"
)

type LiteraryWorkService interface {
	GetWork(ctx context.Context, workID string, filter catalog.AccessFilter) (*literaryworks.DetailResponse, error)
	ListCandidates(ctx context.Context, contentID string, limit int) ([]literaryworks.Candidate, error)
	LinkItems(ctx context.Context, workID string, contentIDs []string) (string, error)
	UnlinkItem(ctx context.Context, workID, contentID string) error
	ConfirmMatch(ctx context.Context, sourceContentID, targetContentID string, userID int) (string, error)
	IgnoreMatch(ctx context.Context, sourceContentID, targetContentID string, userID int) error
}

type LiteraryWorkHandler struct {
	Service LiteraryWorkService
}

type literaryWorkLinkRequest struct {
	WorkID     string   `json:"work_id"`
	ContentIDs []string `json:"content_ids"`
}

type literaryWorkDecisionRequest struct {
	SourceContentID string `json:"source_content_id"`
	TargetContentID string `json:"target_content_id"`
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

func (h *LiteraryWorkHandler) HandleListCandidates(w http.ResponseWriter, r *http.Request) {
	contentID := strings.TrimSpace(chi.URLParam(r, "content_id"))
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id is required")
		return
	}
	if h == nil || h.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Literary works are not configured")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}
	candidates, err := h.Service.ListCandidates(r.Context(), contentID, limit)
	if err != nil {
		if errors.Is(err, literaryworks.ErrWorkNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list work candidates")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": candidates})
}

func (h *LiteraryWorkHandler) HandleLinkItems(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Literary works are not configured")
		return
	}
	var req literaryWorkLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	workID, err := h.Service.LinkItems(r.Context(), req.WorkID, req.ContentIDs)
	if err != nil {
		if errors.Is(err, literaryworks.ErrWorkNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to link work items")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"work_id": workID})
}

func (h *LiteraryWorkHandler) HandleUnlinkItem(w http.ResponseWriter, r *http.Request) {
	workID := strings.TrimSpace(chi.URLParam(r, "work_id"))
	contentID := strings.TrimSpace(chi.URLParam(r, "content_id"))
	if workID == "" || contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "work_id and content_id are required")
		return
	}
	if h == nil || h.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Literary works are not configured")
		return
	}
	if err := h.Service.UnlinkItem(r.Context(), workID, contentID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to unlink work item")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LiteraryWorkHandler) HandleConfirmMatch(w http.ResponseWriter, r *http.Request) {
	h.handleDecision(w, r, true)
}

func (h *LiteraryWorkHandler) HandleIgnoreMatch(w http.ResponseWriter, r *http.Request) {
	h.handleDecision(w, r, false)
}

func (h *LiteraryWorkHandler) handleDecision(w http.ResponseWriter, r *http.Request, confirm bool) {
	if h == nil || h.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Literary works are not configured")
		return
	}
	var req literaryWorkDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	req.SourceContentID = strings.TrimSpace(req.SourceContentID)
	req.TargetContentID = strings.TrimSpace(req.TargetContentID)
	if req.SourceContentID == "" || req.TargetContentID == "" || req.SourceContentID == req.TargetContentID {
		writeError(w, http.StatusBadRequest, "bad_request", "source_content_id and target_content_id are required")
		return
	}
	claims := apimw.GetClaims(r.Context())
	userID := 0
	if claims != nil {
		userID = claims.UserID
	}
	var (
		workID string
		err    error
	)
	if confirm {
		workID, err = h.Service.ConfirmMatch(r.Context(), req.SourceContentID, req.TargetContentID, userID)
	} else {
		err = h.Service.IgnoreMatch(r.Context(), req.SourceContentID, req.TargetContentID, userID)
	}
	if err != nil {
		if errors.Is(err, literaryworks.ErrWorkNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to record work match decision")
		return
	}
	if confirm {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "work_id": workID})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
