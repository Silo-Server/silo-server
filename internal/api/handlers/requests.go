package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

type RequestService interface {
	Search(ctx context.Context, viewer mediarequests.Viewer, query string, mediaType mediarequests.MediaType, page int) (*mediarequests.MediaPage, error)
	Discover(ctx context.Context, viewer mediarequests.Viewer, section string, page int) (*mediarequests.DiscoverySection, error)
	DiscoverAll(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverySection, error)
	GetDetail(ctx context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) (*mediarequests.MediaDetail, error)
	CreateRequest(ctx context.Context, viewer mediarequests.Viewer, input mediarequests.CreateRequestInput) (*mediarequests.Request, error)
	ListMine(ctx context.Context, viewer mediarequests.Viewer, filter mediarequests.ListFilter) ([]*mediarequests.Request, error)
	ListAdmin(ctx context.Context, viewer mediarequests.Viewer, filter mediarequests.ListFilter) ([]*mediarequests.Request, error)
	GetRequest(ctx context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error)
	Approve(ctx context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error)
	Decline(ctx context.Context, viewer mediarequests.Viewer, id, reason string) (*mediarequests.Request, error)
	Retry(ctx context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error)
	GetSettings(ctx context.Context, viewer mediarequests.Viewer) (mediarequests.Settings, error)
	UpdateSettings(ctx context.Context, viewer mediarequests.Viewer, settings mediarequests.Settings) (mediarequests.Settings, error)
	GetUserLimit(ctx context.Context, viewer mediarequests.Viewer, userID int) (*mediarequests.UserLimit, error)
	UpsertUserLimit(ctx context.Context, viewer mediarequests.Viewer, limit mediarequests.UserLimit) (*mediarequests.UserLimit, error)
	ListIntegrations(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.Integration, error)
	UpsertIntegration(ctx context.Context, viewer mediarequests.Viewer, integration mediarequests.Integration) (*mediarequests.Integration, error)
	LoadIntegrationOptions(ctx context.Context, viewer mediarequests.Viewer, integration mediarequests.Integration) (*mediarequests.IntegrationOptions, error)
}

type RequestsHandler struct {
	service RequestService
}

func NewRequestsHandler(service RequestService) *RequestsHandler {
	return &RequestsHandler{service: service}
}

func (h *RequestsHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	page, ok := parsePositiveIntQuery(w, r, "page", 1)
	if !ok {
		return
	}
	result, err := h.service.Search(
		r.Context(),
		viewer,
		r.URL.Query().Get("q"),
		mediarequests.MediaType(r.URL.Query().Get("media_type")),
		page,
	)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *RequestsHandler) HandleDiscover(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	sections, err := h.service.DiscoverAll(r.Context(), viewer)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Sections []mediarequests.DiscoverySection `json:"sections"`
	}{Sections: sections})
}

func (h *RequestsHandler) HandleDiscoverSection(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	page, ok := parsePositiveIntQuery(w, r, "page", 1)
	if !ok {
		return
	}
	section, err := h.service.Discover(r.Context(), viewer, chi.URLParam(r, "section"), page)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, section)
}

func (h *RequestsHandler) HandleGetDetail(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	mediaType := mediarequests.MediaType(strings.TrimSpace(chi.URLParam(r, "media_type")))
	tmdbID, err := strconv.Atoi(strings.TrimSpace(chi.URLParam(r, "tmdb_id")))
	if err != nil || tmdbID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid tmdb id")
		return
	}
	detail, err := h.service.GetDetail(r.Context(), viewer, mediaType, tmdbID)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *RequestsHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	var input mediarequests.CreateRequestInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	req, err := h.service.CreateRequest(r.Context(), viewer, input)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

func (h *RequestsHandler) HandleListMine(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	requests, err := h.service.ListMine(r.Context(), viewer, parseRequestListFilter(r))
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Requests []*mediarequests.Request `json:"requests"`
	}{Requests: requests})
}

func (h *RequestsHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	req, err := h.service.GetRequest(r.Context(), viewer, chi.URLParam(r, "id"))
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *RequestsHandler) HandleAdminList(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	requests, err := h.service.ListAdmin(r.Context(), viewer, parseRequestListFilter(r))
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Requests []*mediarequests.Request `json:"requests"`
	}{Requests: requests})
}

func (h *RequestsHandler) HandleApprove(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	req, err := h.service.Approve(r.Context(), viewer, chi.URLParam(r, "id"))
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *RequestsHandler) HandleDecline(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	req, err := h.service.Decline(r.Context(), viewer, chi.URLParam(r, "id"), body.Reason)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *RequestsHandler) HandleRetry(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	req, err := h.service.Retry(r.Context(), viewer, chi.URLParam(r, "id"))
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *RequestsHandler) HandleGetSettings(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	settings, err := h.service.GetSettings(r.Context(), viewer)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *RequestsHandler) HandleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	var settings mediarequests.Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	updated, err := h.service.UpdateSettings(r.Context(), viewer, settings)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *RequestsHandler) HandleListIntegrations(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	integrations, err := h.service.ListIntegrations(r.Context(), viewer)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Integrations []requestIntegrationResponse `json:"integrations"`
	}{Integrations: toIntegrationResponses(integrations)})
}

func (h *RequestsHandler) HandleUpdateIntegrations(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	var body struct {
		Integrations []mediarequests.Integration `json:"integrations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	updated := make([]mediarequests.Integration, 0, len(body.Integrations))
	for _, integration := range body.Integrations {
		result, err := h.service.UpsertIntegration(r.Context(), viewer, integration)
		if err != nil {
			writeRequestServiceError(w, err)
			return
		}
		updated = append(updated, *result)
	}
	writeJSON(w, http.StatusOK, struct {
		Integrations []requestIntegrationResponse `json:"integrations"`
	}{Integrations: toIntegrationResponses(updated)})
}

func (h *RequestsHandler) HandleLoadIntegrationOptions(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	var integration mediarequests.Integration
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&integration); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
			return
		}
	}
	integration.Kind = chi.URLParam(r, "kind")
	options, err := h.service.LoadIntegrationOptions(r.Context(), viewer, integration)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, options)
}

func (h *RequestsHandler) HandleGetUserLimit(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	userID, ok := parsePositivePathInt(w, r, "user_id")
	if !ok {
		return
	}
	limit, err := h.service.GetUserLimit(r.Context(), viewer, userID)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, limit)
}

func (h *RequestsHandler) HandleUpdateUserLimit(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, false)
	if !ok {
		return
	}
	userID, ok := parsePositivePathInt(w, r, "user_id")
	if !ok {
		return
	}
	var limit mediarequests.UserLimit
	if err := json.NewDecoder(r.Body).Decode(&limit); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	limit.UserID = userID
	updated, err := h.service.UpsertUserLimit(r.Context(), viewer, limit)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func requestViewer(w http.ResponseWriter, r *http.Request, requireProfile bool) (mediarequests.Viewer, bool) {
	claims := apimw.GetClaims(r.Context())
	if claims == nil || claims.UserID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return mediarequests.Viewer{}, false
	}
	profileID := strings.TrimSpace(apimw.GetProfileID(r.Context()))
	if requireProfile && profileID == "" {
		writeError(w, http.StatusBadRequest, "profile_required", "Profile is required")
		return mediarequests.Viewer{}, false
	}
	return mediarequests.Viewer{
		UserID:    claims.UserID,
		ProfileID: profileID,
		IsAdmin:   claims.Role == "admin",
	}, true
}

func parseRequestListFilter(r *http.Request) mediarequests.ListFilter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	return mediarequests.ListFilter{
		Status:  mediarequests.Status(strings.TrimSpace(q.Get("status"))),
		Outcome: mediarequests.Outcome(strings.TrimSpace(q.Get("outcome"))),
		Limit:   limit,
		Offset:  offset,
	}
}

func parsePositiveIntQuery(w http.ResponseWriter, r *http.Request, key string, fallback int) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid "+key)
		return 0, false
	}
	return value, true
}

func parsePositivePathInt(w http.ResponseWriter, r *http.Request, key string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(chi.URLParam(r, key)))
	if err != nil || value <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid "+key)
		return 0, false
	}
	return value, true
}

type requestIntegrationResponse struct {
	Kind             string         `json:"kind"`
	Enabled          bool           `json:"enabled"`
	BaseURL          string         `json:"base_url"`
	HasAPIKey        bool           `json:"has_api_key"`
	RootFolder       string         `json:"root_folder"`
	QualityProfileID *int           `json:"quality_profile_id,omitempty"`
	Tags             []int          `json:"tags"`
	Options          map[string]any `json:"options"`
	LastCheckAt      *time.Time     `json:"last_check_at,omitempty"`
	LastCheckStatus  string         `json:"last_check_status,omitempty"`
	LastCheckError   string         `json:"last_check_error,omitempty"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

func toIntegrationResponses(integrations []mediarequests.Integration) []requestIntegrationResponse {
	out := make([]requestIntegrationResponse, 0, len(integrations))
	for _, integration := range integrations {
		out = append(out, requestIntegrationResponse{
			Kind:             integration.Kind,
			Enabled:          integration.Enabled,
			BaseURL:          integration.BaseURL,
			HasAPIKey:        strings.TrimSpace(integration.APIKeyRef) != "",
			RootFolder:       integration.RootFolder,
			QualityProfileID: integration.QualityProfileID,
			Tags:             integration.Tags,
			Options:          integration.Options,
			LastCheckAt:      integration.LastCheckAt,
			LastCheckStatus:  integration.LastCheckStatus,
			LastCheckError:   integration.LastCheckError,
			UpdatedAt:        integration.UpdatedAt,
		})
	}
	return out
}

func writeRequestServiceError(w http.ResponseWriter, err error) {
	var quota mediarequests.QuotaError
	switch {
	case errors.As(err, &quota):
		writeJSON(w, http.StatusTooManyRequests, struct {
			Error      string `json:"error"`
			Message    string `json:"message"`
			Used       int    `json:"used"`
			Limit      int    `json:"limit"`
			WindowDays int    `json:"window_days"`
		}{
			Error:      "quota_exceeded",
			Message:    "Request quota exceeded",
			Used:       quota.Used,
			Limit:      quota.Limit,
			WindowDays: quota.WindowDays,
		})
	case errors.Is(err, mediarequests.ErrInvalidInput), errors.Is(err, mediarequests.ErrInvalidMediaType):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, mediarequests.ErrRequestsDisabled):
		writeError(w, http.StatusForbidden, "requests_disabled", "Requests are disabled")
	case errors.Is(err, mediarequests.ErrUserBlocked):
		writeError(w, http.StatusForbidden, "requesting_blocked", "User is blocked from requesting")
	case errors.Is(err, mediarequests.ErrAlreadyAvailable):
		writeError(w, http.StatusConflict, "already_available", "Media is already available")
	case errors.Is(err, mediarequests.ErrAlreadyRequested):
		writeError(w, http.StatusConflict, "already_requested", "Media is already requested")
	case errors.Is(err, mediarequests.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Request access denied")
	case errors.Is(err, mediarequests.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Request not found")
	case errors.Is(err, mediarequests.ErrInvalidState):
		writeError(w, http.StatusConflict, "invalid_state", "Request is not in a valid state for this action")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Request operation failed")
	}
}
