package handlers

import (
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
	Status           string        `json:"status"`
	PollAfter        int           `json:"poll_after"`
	AccessToken      string        `json:"access_token,omitempty"`
	RefreshToken     string        `json:"refresh_token,omitempty"`
	ExpiresIn        int           `json:"expires_in,omitempty"`
	User             *userResponse `json:"user,omitempty"`
	ProfileID        string        `json:"profile_id,omitempty"`
	ProfileToken     string        `json:"profile_token,omitempty"`
	Temporary        bool          `json:"temporary,omitempty"`
	SessionExpiresAt string        `json:"session_expires_at,omitempty"`
}

type deviceDecisionRequest struct {
	Token string `json:"token,omitempty"`
	Code  string `json:"code,omitempty"`
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

	result, err := h.device.Start(r.Context(), auth.DeviceLoginStartInput{
		DeviceName:     req.DeviceName,
		DevicePlatform: req.DevicePlatform,
		IPAddress:      clientip.FromContext(r.Context()),
		UserAgent:      r.UserAgent(),
		BaseURL:        requestBaseURL(r),
		ClientPurpose:  req.ClientPurpose,
		Temporary:      req.Temporary,
	})
	if err != nil {
		if errors.Is(err, auth.ErrDeviceLoginBadPurpose) {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid device login purpose")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start device login")
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

	info, err := h.device.Lookup(r.Context(), auth.DeviceLoginLookupInput{
		BrowserCode: r.URL.Query().Get("token"),
		UserCode:    r.URL.Query().Get("code"),
	})
	if err != nil {
		if errors.Is(err, auth.ErrDeviceLoginNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Device login request not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load device login")
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

	result, err := h.device.Poll(r.Context(), req.DeviceCode)
	if err != nil {
		if errors.Is(err, auth.ErrDeviceLoginNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Device login request not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to poll device login")
		return
	}

	resp := devicePollResponse{
		Status:    result.Status,
		PollAfter: result.PollAfter,
	}
	if result.TokenPair != nil && result.User != nil {
		resp.AccessToken = result.TokenPair.AccessToken
		resp.RefreshToken = result.TokenPair.RefreshToken
		resp.ExpiresIn = result.TokenPair.ExpiresIn
		user := buildUserResponse(result.User, nil, nil)
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

	err := h.device.Approve(r.Context(), auth.DeviceLoginLookupInput{
		BrowserCode: req.Token,
		UserCode:    req.Code,
	}, userID)
	if err != nil {
		h.writeDeviceDecisionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
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

	err := h.device.ApproveRemotePlayback(r.Context(), auth.DeviceLoginLookupInput{
		BrowserCode: req.Token,
		UserCode:    req.Code,
	}, scope.UserID, scope.ProfileID)
	if err != nil {
		h.writeDeviceDecisionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
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

	err := h.device.Deny(r.Context(), auth.DeviceLoginLookupInput{
		BrowserCode: req.Token,
		UserCode:    req.Code,
	})
	if err != nil {
		h.writeDeviceDecisionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "denied"})
}

func (h *AuthHandler) writeDeviceDecisionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrDeviceLoginNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Device login request not found")
	case errors.Is(err, auth.ErrDeviceLoginExpired):
		writeError(w, http.StatusGone, "expired", "Device login request has expired")
	case errors.Is(err, auth.ErrDeviceLoginConsumed):
		writeError(w, http.StatusConflict, "consumed", "Device login request has already been used")
	case errors.Is(err, auth.ErrDeviceLoginDenied):
		writeError(w, http.StatusConflict, "denied", "Device login request has already been denied")
	case errors.Is(err, auth.ErrUserDisabled):
		writeError(w, http.StatusForbidden, "user_disabled", "User account is disabled")
	case errors.Is(err, auth.ErrDeviceLoginPurpose):
		writeError(w, http.StatusConflict, "purpose_mismatch", "Device login purpose does not match this approval route")
	case errors.Is(err, auth.ErrDeviceLoginConflict):
		writeError(w, http.StatusConflict, "approval_conflict", "Device login request was approved by another identity")
	case errors.Is(err, auth.ErrDeviceLoginNoProfile):
		writeError(w, http.StatusNotFound, "profile_not_found", "Profile not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Device login request failed")
	}
}
