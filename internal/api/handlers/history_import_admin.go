package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

// HandleAdminSetSourceToken stores an admin token for a source.
//
// PUT /admin/history-imports/sources/{id}/token
func (h *HistoryImportHandler) HandleAdminSetSourceToken(w http.ResponseWriter, r *http.Request) {
	id, err := parseHistoryImportID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid source ID")
		return
	}
	var req historyimport.SetAdminTokenInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "token is required")
		return
	}
	if err := h.service.SetSourceAdminToken(r.Context(), id, req.Token); err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleAdminClearSourceToken removes the admin token from a source.
//
// DELETE /admin/history-imports/sources/{id}/token
func (h *HistoryImportHandler) HandleAdminClearSourceToken(w http.ResponseWriter, r *http.Request) {
	id, err := parseHistoryImportID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid source ID")
		return
	}
	if err := h.service.ClearSourceAdminToken(r.Context(), id); err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleAdminDiscoverUsers queries the external server for its user list.
//
// GET /admin/history-imports/sources/{id}/users
func (h *HistoryImportHandler) HandleAdminDiscoverUsers(w http.ResponseWriter, r *http.Request) {
	id, err := parseHistoryImportID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid source ID")
		return
	}
	users, err := h.service.DiscoverExternalUsers(r.Context(), id)
	if err != nil {
		slog.ErrorContext(r.Context(), "history import: discover external users failed", "component", "api", "source_id", id, "error", err)
		h.writeDiscoverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (h *HistoryImportHandler) writeDiscoverError(w http.ResponseWriter, err error) {
	if status := historyimport.UpstreamHTTPStatus(err); status > 0 {
		switch {
		case status == 401:
			writeError(w, 401, "unauthorized", "The admin token was rejected. Check that it is a valid admin API key for this server.")
		case status == 403:
			writeError(w, 403, "forbidden", "The admin token does not have permission to list users on this server.")
		case status == 404:
			writeError(w, 502, "bad_gateway", "Server not found at that URL. Check the server URL is correct.")
		case status >= 400 && status < 500:
			writeError(w, 502, "bad_gateway", fmt.Sprintf("The server returned an error (%d). Check the URL and admin token.", status))
		default:
			writeError(w, 502, "bad_gateway", "The server could not be reached. Check the URL is correct and the server is running.")
		}
		return
	}
	if historyimport.IsReachabilityError(err) {
		writeError(w, 502, "bad_gateway", "The server could not be reached. Check the URL is correct and the server is running.")
		return
	}
	h.writeHistoryImportError(w, err)
}

// HandleAdminListMappings returns all user mappings, optionally filtered by source_id.
//
// GET /admin/history-imports/mappings?source_id=N
func (h *HistoryImportHandler) HandleAdminListMappings(w http.ResponseWriter, r *http.Request) {
	sourceIDStr := r.URL.Query().Get("source_id")
	if sourceIDStr == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "source_id is required")
		return
	}
	sourceID, err := strconv.Atoi(sourceIDStr)
	if err != nil || sourceID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid source_id")
		return
	}
	mappings, err := h.service.ListMappings(r.Context(), sourceID)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	if mappings == nil {
		mappings = []historyimport.UserMapping{}
	}
	writeJSON(w, http.StatusOK, mappings)
}

// HandleAdminCreateMapping creates a new user mapping.
//
// POST /admin/history-imports/mappings
func (h *HistoryImportHandler) HandleAdminCreateMapping(w http.ResponseWriter, r *http.Request) {
	var req historyimport.CreateMappingInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	mapping, err := h.service.CreateMapping(r.Context(), req)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, mapping)
}

// HandleAdminUpdateMapping updates the Silo target of a mapping.
//
// PUT /admin/history-imports/mappings/{id}
func (h *HistoryImportHandler) HandleAdminUpdateMapping(w http.ResponseWriter, r *http.Request) {
	id, err := parseMappingID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid mapping ID")
		return
	}
	var req historyimport.UpdateMappingInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	mapping, err := h.service.UpdateMapping(r.Context(), id, req)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mapping)
}

// HandleAdminDeleteMapping removes a user mapping.
//
// DELETE /admin/history-imports/mappings/{id}
func (h *HistoryImportHandler) HandleAdminDeleteMapping(w http.ResponseWriter, r *http.Request) {
	id, err := parseMappingID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid mapping ID")
		return
	}
	if err := h.service.DeleteMapping(r.Context(), id); err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleAdminCreateRun triggers an import for a single mapping.
//
// POST /admin/history-imports/mappings/{id}/run
func (h *HistoryImportHandler) HandleAdminCreateRun(w http.ResponseWriter, r *http.Request) {
	id, err := parseMappingID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid mapping ID")
		return
	}
	run, err := h.service.CreateAdminRun(r.Context(), id)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

// HandleAdminBulkRun triggers imports for all eligible mappings on a source.
//
// POST /admin/history-imports/sources/{id}/bulk-run
func (h *HistoryImportHandler) HandleAdminBulkRun(w http.ResponseWriter, r *http.Request) {
	id, err := parseHistoryImportID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid source ID")
		return
	}
	result, err := h.service.BulkCreateAdminRuns(r.Context(), id)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// HandleAdminListRuns returns import runs across all users.
//
// GET /admin/history-imports/runs?source_id=N&limit=N
func (h *HistoryImportHandler) HandleAdminListRuns(w http.ResponseWriter, r *http.Request) {
	var sourceID *int
	if s := r.URL.Query().Get("source_id"); s != "" {
		id, err := strconv.Atoi(s)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid source_id")
			return
		}
		sourceID = &id
	}
	limit, _ := parsePagination(r)
	runs, err := h.service.ListAdminRuns(r.Context(), sourceID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list history import runs")
		return
	}
	if runs == nil {
		runs = []historyimport.Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// HandleAdminGetRun returns any run by ID.
//
// GET /admin/history-imports/runs/{id}
func (h *HistoryImportHandler) HandleAdminGetRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Run ID is required")
		return
	}
	run, err := h.service.GetAdminRun(r.Context(), runID)
	if err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// HandleAdminCancelRun cancels a queued or running import.
//
// POST /admin/history-imports/runs/{id}/cancel
func (h *HistoryImportHandler) HandleAdminCancelRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Run ID is required")
		return
	}
	if err := h.service.CancelAdminRun(r.Context(), runID); err != nil {
		h.writeHistoryImportError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleAdminPlexLogin exchanges Plex credentials for an auth token.
//
// POST /admin/history-imports/plex/login
func (h *HistoryImportHandler) HandleAdminPlexLogin(w http.ResponseWriter, r *http.Request) {
	var req historyimport.PlexLoginInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "username and password are required")
		return
	}
	token, err := h.service.AuthenticatePlex(r.Context(), req.Username, req.Password)
	if err != nil {
		slog.ErrorContext(r.Context(), "history import: plex login failed", "component", "api", "error", err)
		h.writeDiscoverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, historyimport.PlexLoginResult{Token: token})
}

func parseMappingID(r *http.Request) (int, error) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id <= 0 {
		return 0, errInvalidID
	}
	return id, nil
}

var errInvalidID = fmt.Errorf("invalid id")
