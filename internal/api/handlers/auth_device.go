package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

type deviceStartRequest struct {
	DeviceName     string `json:"device_name"`
	DevicePlatform string `json:"device_platform"`
	ClientPurpose  string `json:"client_purpose,omitempty"`
	Temporary      bool   `json:"temporary,omitempty"`
}

type deviceStartResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	MatchCode               string `json:"match_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresAt               string `json:"expires_at"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	DeviceName              string `json:"device_name"`
	DevicePlatform          string `json:"device_platform"`
	ClientPurpose           string `json:"client_purpose"`
	Temporary               bool   `json:"temporary"`
}

type deviceLookupResponse struct {
	Status         string `json:"status"`
	UserCode       string `json:"user_code,omitempty"`
	MatchCode      string `json:"match_code,omitempty"`
	DeviceName     string `json:"device_name,omitempty"`
	DevicePlatform string `json:"device_platform,omitempty"`
	IPAddressHint  string `json:"ip_address_hint,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	ClientPurpose  string `json:"client_purpose,omitempty"`
	Temporary      bool   `json:"temporary,omitempty"`
}

type devicePollRequest struct {
	DeviceCode string `json:"device_code"`
}

type devicePollResponse struct {
	Status           string    `json:"status"`
	PollAfter        int       `json:"poll_after"`
	AccessToken      string    `json:"access_token,omitempty"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	ExpiresIn        int       `json:"expires_in,omitempty"`
	User             *UserView `json:"user,omitempty"`
	ProfileID        string    `json:"profile_id,omitempty"`
	ProfileToken     string    `json:"profile_token,omitempty"`
	Temporary        bool      `json:"temporary,omitempty"`
	SessionExpiresAt string    `json:"session_expires_at,omitempty"`
}

type deviceDecisionRequest struct {
	Token string `json:"token,omitempty"`
	Code  string `json:"code,omitempty"`
}

// deviceDecisionResponse is the {status} answer of approve, approve-handoff
// and deny; it serializes exactly as the former map[string]string did.
type deviceDecisionResponse struct {
	Status string `json:"status"`
}

type deviceLoginCapabilityResponse struct {
	RemotePlaybackHandoff bool  `json:"remote_playback_handoff"`
	ProtocolVersions      []int `json:"protocol_versions"`
}

func (h *AuthHandler) HandleDeviceCapability(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, deviceLoginCapabilityResponse{
		RemotePlaybackHandoff: true,
		ProtocolVersions:      []int{2},
	})
}

func (h *AuthHandler) HandleDeviceStart(w http.ResponseWriter, r *http.Request) {
	if h.device == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
		return
	}

	var req deviceStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	result, err := h.StartDeviceLogin(r.Context(), auth.DeviceLoginStartInput{
		DeviceName:     req.DeviceName,
		DevicePlatform: req.DevicePlatform,
		IPAddress:      clientip.FromContext(r.Context()),
		UserAgent:      r.UserAgent(),
		BaseURL:        requestBaseURL(r),
		ClientPurpose:  req.ClientPurpose,
		Temporary:      req.Temporary,
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, deviceStartResponse{
		DeviceCode:              result.DeviceCode,
		UserCode:                result.UserCode,
		MatchCode:               result.MatchCode,
		VerificationURI:         result.VerificationURI,
		VerificationURIComplete: result.VerificationURIComplete,
		ExpiresAt:               result.ExpiresAt.UTC().Format(time.RFC3339),
		ExpiresIn:               result.ExpiresIn,
		Interval:                result.Interval,
		DeviceName:              result.DeviceName,
		DevicePlatform:          result.DevicePlatform,
		ClientPurpose:           result.ClientPurpose,
		Temporary:               result.Temporary,
	})
}

func (h *AuthHandler) HandleDeviceLookup(w http.ResponseWriter, r *http.Request) {
	if h.device == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
		return
	}

	info, err := h.LookupDeviceLogin(r.Context(), auth.DeviceLoginLookupInput{
		BrowserCode: r.URL.Query().Get("token"),
		UserCode:    r.URL.Query().Get("code"),
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}

	response := deviceLookupResponse{
		Status:         info.Status,
		UserCode:       info.UserCode,
		MatchCode:      info.MatchCode,
		DeviceName:     info.DeviceName,
		DevicePlatform: info.DevicePlatform,
		IPAddressHint:  info.IPAddressHint,
		ClientPurpose:  info.ClientPurpose,
		Temporary:      info.Temporary,
	}
	if !info.ExpiresAt.IsZero() {
		response.ExpiresAt = info.ExpiresAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *AuthHandler) HandleDevicePoll(w http.ResponseWriter, r *http.Request) {
	if h.device == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
		return
	}

	var req devicePollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.DeviceCode == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "device_code is required")
		return
	}

	result, err := h.PollDeviceLogin(r.Context(), req.DeviceCode)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	resp := devicePollResponse{
		Status:    result.Status,
		PollAfter: result.PollAfter,
	}
	if result.Tokens != nil {
		resp.AccessToken = result.Tokens.AccessToken
		resp.RefreshToken = result.Tokens.RefreshToken
		resp.ExpiresIn = result.Tokens.ExpiresIn
		user := result.Tokens.User
		resp.User = &user
		if result.Temporary {
			resp.ProfileID = result.ProfileID
			resp.ProfileToken = result.ProfileToken
			resp.Temporary = true
			if !result.SessionExpiresAt.IsZero() {
				resp.SessionExpiresAt = result.SessionExpiresAt.UTC().Format(time.RFC3339)
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// DeviceLoginPollView is the poll outcome the v1 and v2 handlers both render.
// Tokens is set only once the request was approved and this poll consumed it.
type DeviceLoginPollView struct {
	Status           string
	PollAfter        int
	Tokens           *TokenPairView
	ProfileID        string
	ProfileToken     string
	Temporary        bool
	SessionExpiresAt time.Time
}

// TokenPairView is the credential response shape login, setup, signup and
// device poll share: the token pair plus the account it authenticates.
type TokenPairView struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	User         UserView
}

// DeviceLoginConfigured reports whether device pairing is wired; the v2
// capability document reports not_configured otherwise.
func (h *AuthHandler) DeviceLoginConfigured() bool { return h.device != nil }

// StartDeviceLogin opens a pairing request. v1 POST /auth/device/start and
// v2 startDeviceLogin both call it; a failure is an *APIError.
func (h *AuthHandler) StartDeviceLogin(ctx context.Context, input auth.DeviceLoginStartInput) (*auth.DeviceLoginStartResult, error) {
	if h.device == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	result, err := h.device.Start(ctx, input)
	if err != nil {
		if errors.Is(err, auth.ErrDeviceLoginBadPurpose) {
			return nil, fieldError("client_purpose", "Invalid device login purpose")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to start device login")
	}
	return result, nil
}

// LookupDeviceLogin describes a pairing request to the approving browser.
// v1 GET /auth/device and v2 getDeviceLogin both call it.
func (h *AuthHandler) LookupDeviceLogin(ctx context.Context, input auth.DeviceLoginLookupInput) (*auth.DeviceLoginInfo, error) {
	if h.device == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	info, err := h.device.Lookup(ctx, input)
	if err != nil {
		if errors.Is(err, auth.ErrDeviceLoginNotFound) {
			return nil, apiError(http.StatusNotFound, "not_found", "Device login request not found")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load device login")
	}
	return info, nil
}

// PollDeviceLogin advances the pairing state machine for the waiting device.
// v1 POST /auth/device/poll and v2 pollDeviceLogin both call it.
func (h *AuthHandler) PollDeviceLogin(ctx context.Context, deviceCode string) (*DeviceLoginPollView, error) {
	if h.device == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	result, err := h.device.Poll(ctx, deviceCode)
	if err != nil {
		if errors.Is(err, auth.ErrDeviceLoginNotFound) {
			return nil, apiError(http.StatusNotFound, "not_found", "Device login request not found")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to poll device login")
	}
	view := &DeviceLoginPollView{Status: result.Status, PollAfter: result.PollAfter}
	if result.TokenPair != nil && result.User != nil {
		view.Tokens = &TokenPairView{
			AccessToken:  result.TokenPair.AccessToken,
			RefreshToken: result.TokenPair.RefreshToken,
			ExpiresIn:    result.TokenPair.ExpiresIn,
			User:         buildUserResponse(result.User, effectiveDownloadAllowed(ctx, result.User, h.accessGroups), nil, nil),
		}
		if result.Temporary {
			view.ProfileID = result.ProfileID
			view.ProfileToken = result.ProfileToken
			view.Temporary = true
			view.SessionExpiresAt = result.SessionExpiresAt
		}
	}
	return view, nil
}

// DeviceLoginDecision is the shared outcome of approve, approve-handoff and
// deny: the state the request is now in.
type DeviceLoginDecision struct {
	Status string
}

// ApproveDeviceLogin approves a login-purpose pairing request as the caller's
// account. v1 POST /auth/device/approve and v2 approveDeviceLogin both call it.
func (h *AuthHandler) ApproveDeviceLogin(ctx context.Context, input auth.DeviceLoginLookupInput, userID int) (DeviceLoginDecision, error) {
	if h.device == nil {
		return DeviceLoginDecision{}, apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	if userID == 0 {
		return DeviceLoginDecision{}, apiError(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	if err := h.device.Approve(ctx, input, userID); err != nil {
		return DeviceLoginDecision{}, deviceDecisionError(err)
	}
	return DeviceLoginDecision{Status: auth.DeviceLoginStatusApproved}, nil
}

// ApproveDeviceHandoff approves a remote-playback pairing request for the
// caller's verified profile. v1 POST /auth/device/approve-handoff and v2
// approveDeviceHandoff both call it.
func (h *AuthHandler) ApproveDeviceHandoff(ctx context.Context, input auth.DeviceLoginLookupInput, userID int, profileID string) (DeviceLoginDecision, error) {
	if h.device == nil {
		return DeviceLoginDecision{}, apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	if userID == 0 || profileID == "" {
		return DeviceLoginDecision{}, apiError(http.StatusForbidden, "profile_required", "An active verified profile is required")
	}
	if err := h.device.ApproveRemotePlayback(ctx, input, userID, profileID); err != nil {
		return DeviceLoginDecision{}, deviceDecisionError(err)
	}
	return DeviceLoginDecision{Status: auth.DeviceLoginStatusApproved}, nil
}

// DenyDeviceLogin refuses a pairing request. v1 POST /auth/device/deny and
// v2 denyDeviceLogin both call it.
func (h *AuthHandler) DenyDeviceLogin(ctx context.Context, input auth.DeviceLoginLookupInput, userID int) (DeviceLoginDecision, error) {
	if h.device == nil {
		return DeviceLoginDecision{}, apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	if userID == 0 {
		return DeviceLoginDecision{}, apiError(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	if err := h.device.Deny(ctx, input); err != nil {
		return DeviceLoginDecision{}, deviceDecisionError(err)
	}
	return DeviceLoginDecision{Status: auth.DeviceLoginStatusDenied}, nil
}

// deviceDecisionError maps a device-login service failure onto the v1
// status and code; the cause is kept so a caller can still branch on it.
func deviceDecisionError(err error) *APIError {
	var out *APIError
	switch {
	case errors.Is(err, auth.ErrDeviceLoginNotFound):
		out = apiError(http.StatusNotFound, "not_found", "Device login request not found")
	case errors.Is(err, auth.ErrDeviceLoginExpired):
		out = apiError(http.StatusGone, "expired", "Device login request has expired")
	case errors.Is(err, auth.ErrDeviceLoginConsumed):
		out = apiError(http.StatusConflict, "consumed", "Device login request has already been used")
	case errors.Is(err, auth.ErrDeviceLoginDenied):
		out = apiError(http.StatusConflict, "denied", "Device login request has already been denied")
	case errors.Is(err, auth.ErrUserDisabled):
		out = apiError(http.StatusForbidden, "user_disabled", "User account is disabled")
	case errors.Is(err, auth.ErrDeviceLoginPurpose):
		out = apiError(http.StatusConflict, "purpose_mismatch", "Device login purpose does not match this approval route")
	case errors.Is(err, auth.ErrDeviceLoginConflict):
		out = apiError(http.StatusConflict, "approval_conflict", "Device login request was approved by another identity")
	case errors.Is(err, auth.ErrDeviceLoginNoProfile):
		out = apiError(http.StatusNotFound, "profile_not_found", "Profile not found")
	default:
		out = apiError(http.StatusInternalServerError, "internal_error", "Device login request failed")
	}
	out.cause = err
	return out
}

func (h *AuthHandler) HandleDeviceApprove(w http.ResponseWriter, r *http.Request) {
	if h.device == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
		return
	}

	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req deviceDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	decision, err := h.ApproveDeviceLogin(r.Context(), auth.DeviceLoginLookupInput{
		BrowserCode: req.Token,
		UserCode:    req.Code,
	}, userID)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, deviceDecisionResponse(decision))
}

func (h *AuthHandler) HandleDeviceApproveHandoff(w http.ResponseWriter, r *http.Request) {
	if h.device == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
		return
	}

	scope, ok := access.GetScope(r.Context())
	if !ok || scope.UserID == 0 || scope.ProfileID == "" {
		writeError(w, http.StatusForbidden, "profile_required", "An active verified profile is required")
		return
	}

	var req deviceDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	decision, err := h.ApproveDeviceHandoff(r.Context(), auth.DeviceLoginLookupInput{
		BrowserCode: req.Token,
		UserCode:    req.Code,
	}, scope.UserID, scope.ProfileID)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, deviceDecisionResponse(decision))
}

func (h *AuthHandler) HandleDeviceDeny(w http.ResponseWriter, r *http.Request) {
	if h.device == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
		return
	}

	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req deviceDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	decision, err := h.DenyDeviceLogin(r.Context(), auth.DeviceLoginLookupInput{
		BrowserCode: req.Token,
		UserCode:    req.Code,
	}, userID)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, deviceDecisionResponse(decision))
}
