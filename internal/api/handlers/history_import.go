package handlers

import (
	"context"
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

func (h *HistoryImportHandler) Service() *historyimport.Service { return h.service }

// ListImportSources is the seam v1 GET /history-imports/sources and v2
// listHistoryImportSources share: the enabled sources a user may import from.
func (h *HistoryImportHandler) ListImportSources(ctx context.Context) ([]historyimport.Source, error) {
	return h.service.ListUserSources(ctx)
}

// LoginEmbyConnect is the seam behind POST /history-imports/emby-connect/login:
// it exchanges Emby Connect credentials for a connect session. A failure is
// the *APIError v1 answers with.
func (h *HistoryImportHandler) LoginEmbyConnect(ctx context.Context, userID int, input historyimport.LoginConnectInput) (*historyimport.ConnectSessionLoginResult, error) {
	session, err := h.service.LoginConnect(ctx, userID, input)
	if err != nil {
		slog.ErrorContext(ctx, "history import emby connect login failed", "component", "api", "user_id", userID, "error", err)
		return nil, historyImportAPIError(err)
	}
	return session, nil
}

// CreateImportRun is the seam behind POST /history-imports/runs: it validates
// the source, resolves credentials, and atomically enqueues durable intent.
func (h *HistoryImportHandler) CreateImportRun(ctx context.Context, userID int, input historyimport.CreateRunInput) (*historyimport.Run, error) {
	run, err := h.service.CreateRun(ctx, userID, input)
	if err != nil {
		return nil, historyImportAPIError(err)
	}
	return run, nil
}

// ListImportRuns is the seam behind v1 GET /history-imports/runs: the newest
// limit runs of the account.
func (h *HistoryImportHandler) ListImportRuns(ctx context.Context, userID, limit int) ([]historyimport.Run, error) {
	return h.service.ListRuns(ctx, userID, limit)
}

// ListImportRunsPage is the keyset page v2 listHistoryImportRuns reads:
// runs strictly older than after in (created_at, id) order, and whether
// more follow.
func (h *HistoryImportHandler) ListImportRunsPage(ctx context.Context, userID int, after *historyimport.RunKey, limit int) ([]historyimport.Run, bool, error) {
	return h.service.ListRunsPage(ctx, userID, after, limit)
}

// GetImportRun is the seam behind GET /history-imports/runs/{id}.
func (h *HistoryImportHandler) GetImportRun(ctx context.Context, userID int, runID string) (*historyimport.Run, error) {
	run, err := h.service.GetRun(ctx, userID, runID)
	if err != nil {
		return nil, historyImportAPIError(err)
	}
	return run, nil
}

// CreatePlexPin is the seam behind POST /history-imports/plex/auth/pin.
func (h *HistoryImportHandler) CreatePlexPin(ctx context.Context, userID int) (*historyimport.PlexPinResponse, error) {
	pin, err := h.service.CreatePlexPin(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "history import plex pin creation failed", "component", "api", "user_id", userID, "error", err)
		return nil, historyImportAPIError(err)
	}
	return pin, nil
}

// CheckPlexPin is the seam behind POST /history-imports/plex/auth/check.
func (h *HistoryImportHandler) CheckPlexPin(ctx context.Context, userID int, sessionID string) (*historyimport.PlexCheckResponse, error) {
	result, err := h.service.CheckPlexPin(ctx, userID, sessionID)
	if err != nil {
		return nil, historyImportAPIError(err)
	}
	return result, nil
}

func (h *HistoryImportHandler) HandleListSources(w http.ResponseWriter, r *http.Request) {
	sources, err := h.ListImportSources(r.Context())
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

	session, err := h.LoginEmbyConnect(r.Context(), userID, req)
	if err != nil {
		writeAPIError(w, err)
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

	run, err := h.CreateImportRun(r.Context(), userID, req)
	if err != nil {
		writeAPIError(w, err)
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
	runs, err := h.ListImportRuns(r.Context(), userID, limit)
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
	run, err := h.GetImportRun(r.Context(), userID, runID)
	if err != nil {
		writeAPIError(w, err)
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
	pin, err := h.CreatePlexPin(r.Context(), userID)
	if err != nil {
		writeAPIError(w, err)
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
	result, err := h.CheckPlexPin(r.Context(), userID, req.SessionID)
	if err != nil {
		writeAPIError(w, err)
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
	writeAPIError(w, historyImportAPIError(err))
}

// historyImportAPIError maps a history import failure onto the v1 decision
// (status, code, message). Upstream statuses carry the historyimport
// upstream-error code so the v2 listener can tell a rejected source
// credential from a Silo authentication failure.
func historyImportAPIError(err error) *APIError {
	switch {
	case errors.Is(err, historyimport.ErrPersonalAdmissionUncertain):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "dependency_unavailable", Message: historyimport.ErrPersonalAdmissionUncertain.Error(), cause: err}
	case errors.Is(err, historyimport.ErrPersonalCredentialsUnavailable):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "dependency_unavailable", Message: "Personal imports are unavailable. No import was accepted.", cause: err}
	case errors.Is(err, historyimport.ErrPersonalSessionChanged), errors.Is(err, historyimport.ErrRunConfigurationChanged), errors.Is(err, historyimport.ErrSourceDisabled):
		return &APIError{Status: http.StatusConflict, Code: policyErrorConflict, Message: "The import source or login session changed. Review the configuration and authenticate again.", cause: err}

	case errors.Is(err, historyimport.ErrSourceNotFound),
		errors.Is(err, historyimport.ErrRunNotFound),
		errors.Is(err, historyimport.ErrProfileNotFound),
		errors.Is(err, historyimport.ErrConnectSessionNotFound),
		errors.Is(err, historyimport.ErrPlexSessionNotFound),
		errors.Is(err, historyimport.ErrMappingNotFound):
		return &APIError{Status: http.StatusNotFound, Code: policyErrorNotFound, Message: err.Error(), cause: err}
	case errors.Is(err, historyimport.ErrConnectSessionExpired),
		errors.Is(err, historyimport.ErrConnectSessionUsed),
		errors.Is(err, historyimport.ErrPlexSessionExpired),
		errors.Is(err, historyimport.ErrPlexSessionUsed),
		errors.Is(err, historyimport.ErrNoAdminToken):
		return &APIError{Status: http.StatusBadRequest, Code: policyErrorBadRequest, Message: err.Error(), cause: err}
	case errors.Is(err, historyimport.ErrActiveRunExists),
		errors.Is(err, historyimport.ErrMappingDuplicate):
		return &APIError{Status: http.StatusConflict, Code: policyErrorConflict, Message: err.Error(), cause: err}
	default:
		if status := historyimport.UpstreamHTTPStatus(err); status > 0 {
			return HistoryImportUpstreamAPIError(status)
		}
		if err != nil && looksLikeValidationError(err) {
			return &APIError{Status: http.StatusBadRequest, Code: policyErrorBadRequest, Message: err.Error(), cause: err}
		}
		slog.Error("history import: unhandled error", "error", err)
		return &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal, Message: "History import request failed", cause: err}
	}
}

// ErrHistoryImportUpstream is the cause of an *APIError that reports the
// source server's answer rather than Silo's: errors.Is distinguishes it from
// a Silo authentication problem on the same status.
var errHistoryImportUpstream = errors.New("history import: upstream server error")

// IsHistoryImportUpstreamError reports whether err wraps a source server
// failure.
func IsHistoryImportUpstreamError(err error) bool { return errors.Is(err, errHistoryImportUpstream) }

// HistoryImportUpstreamAPIError is the *APIError a source server answering
// with status produces; tests of the v2 mapping build it without a client.
func HistoryImportUpstreamAPIError(status int) *APIError {
	httpStatus, code, message := historyImportUpstreamError(status)
	return &APIError{Status: httpStatus, Code: code, Message: message, cause: errHistoryImportUpstream}
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
