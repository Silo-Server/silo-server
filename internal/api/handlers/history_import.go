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
	"github.com/Silo-Server/silo-server/internal/historyimport"
)

type HistoryImportHandler struct {
	service *historyimport.Service
}

func NewHistoryImportHandler(service *historyimport.Service) *HistoryImportHandler {
	return &HistoryImportHandler{service: service}
}

func (h *HistoryImportHandler) HandleListSources(w http.ResponseWriter, r *http.Request) {
	sources, err := h.service.ListUserSources(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list history import sources")
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

func (h *HistoryImportHandler) HandleLoginConnect(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req historyimport.LoginConnectInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "username and password are required")
		return
	}

	session, err := h.service.LoginConnect(r.Context(), userID, req)
	if err != nil {
		slog.ErrorContext(r.Context(), "history import emby connect login failed", "component", "api", "user_id", userID, "error", err)
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *HistoryImportHandler) HandleCreateRun(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req historyimport.CreateRunInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	run, err := h.service.CreateRun(r.Context(), userID, req)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (h *HistoryImportHandler) HandleListRuns(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	limit, _ := parsePagination(r)
	runs, err := h.service.ListRuns(r.Context(), userID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list history import runs")
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (h *HistoryImportHandler) HandleGetRun(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	runID := chi.URLParam(r, "id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Run ID is required")
		return
	}
	run, err := h.service.GetRun(r.Context(), userID, runID)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *HistoryImportHandler) HandleCreatePlexPin(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	pin, err := h.service.CreatePlexPin(r.Context(), userID)
	if err != nil {
		slog.ErrorContext(r.Context(), "history import plex pin creation failed", "component", "api", "user_id", userID, "error", err)
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pin)
}

func (h *HistoryImportHandler) HandleCheckPlexPin(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	var req historyimport.PlexCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "session_id is required")
		return
	}
	result, err := h.service.CheckPlexPin(r.Context(), userID, req.SessionID)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HistoryImportHandler) HandleAdminListSources(w http.ResponseWriter, r *http.Request) {
	sources, err := h.service.ListAdminSources(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list history import sources")
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

func (h *HistoryImportHandler) HandleAdminCreateSource(w http.ResponseWriter, r *http.Request) {
	var req historyimport.CreateSourceInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	source, err := h.service.CreateSource(r.Context(), req)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, source)
}

func (h *HistoryImportHandler) HandleAdminUpdateSource(w http.ResponseWriter, r *http.Request) {
	id, err := parseHistoryImportID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid source ID")
		return
	}
	var req historyimport.UpdateSourceInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	source, err := h.service.UpdateSource(r.Context(), id, req)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}

func (h *HistoryImportHandler) HandleAdminDeleteSource(w http.ResponseWriter, r *http.Request) {
	id, err := parseHistoryImportID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid source ID")
		return
	}
	if err := h.service.DeleteSource(r.Context(), id); err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HistoryImportHandler) writeHistoryImportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, historyimport.ErrSourceNotFound),
		errors.Is(err, historyimport.ErrRunNotFound),
		errors.Is(err, historyimport.ErrProfileNotFound),
		errors.Is(err, historyimport.ErrConnectSessionNotFound),
		errors.Is(err, historyimport.ErrPlexSessionNotFound),
		errors.Is(err, historyimport.ErrMappingNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, historyimport.ErrConnectSessionExpired),
		errors.Is(err, historyimport.ErrConnectSessionUsed),
		errors.Is(err, historyimport.ErrPlexSessionExpired),
		errors.Is(err, historyimport.ErrPlexSessionUsed),
		errors.Is(err, historyimport.ErrNoAdminToken):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, historyimport.ErrActiveRunExists),
		errors.Is(err, historyimport.ErrMappingDuplicate):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	default:
		if status := historyimport.UpstreamHTTPStatus(err); status > 0 {
			httpStatus, code, message := historyImportUpstreamError(status)
			writeError(w, httpStatus, code, message)
			return
		}
		if err != nil && looksLikeValidationError(err) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		slog.Error("history import: unhandled error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "History import request failed")
	}
}

func historyImportUpstreamError(status int) (int, string, string) {
	switch {
	case status == http.StatusUnauthorized:
		return http.StatusUnauthorized, "unauthorized", "Couldn't connect to that server. Check the URL, username, and password and try again."
	case status >= 400 && status < 500:
		return http.StatusBadRequest, "bad_request", "Couldn't start the import with those server settings."
	default:
		return http.StatusBadGateway, "bad_gateway", "The source server couldn't complete the import right now. Please try again."
	}
}

func parseHistoryImportID(r *http.Request) (int, error) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return id, nil
}

func looksLikeValidationError(err error) bool {
	message := err.Error()
	return message == "profile_id is required" ||
		message == "unsupported source type" ||
		message == "direct server_url imports are no longer supported" ||
		message == "exactly one of connect_session_id or source_id is required" ||
		message == "selected server not found in connect session" ||
		message == "selected server does not expose a usable address" ||
		message == "jellyfin_base_url, jellyfin_username, and jellyfin_password are required" ||
		message == "source_id is required" ||
		message == "username and password are required" ||
		message == "name, source_type, and base_url are required" ||
		message == "plex OAuth not completed yet" ||
		message == "selected Plex server not found in session" ||
		message == "selected Plex server has no usable address" ||
		message == "plex_token is required for predefined Plex sources" ||
		message == "source is not a Plex server" ||
		message == "plex_session_id or source_id is required for Plex imports"
}
