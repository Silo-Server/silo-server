package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/go-chi/chi/v5"
)

type applePushRegisterRequest struct {
	DeviceID        string `json:"device_id"`
	APNsToken       string `json:"apns_token"`
	APNsEnvironment string `json:"apns_environment"`
	APNsTopic       string `json:"apns_topic"`
	PushMode        string `json:"push_mode"`
}

type applePushRegisterResponse struct {
	ID             string `json:"id"`
	ServerDeviceID string `json:"server_device_id"`
	Enabled        bool   `json:"enabled"`
	PushMode       string `json:"push_mode"`
	// DisplayToken is a long-lived, profile-scoped credential the iOS
	// Notification Service extension presents to the display endpoint. The
	// extension cannot refresh the short-lived access token, so without this
	// every push arriving after access expiry rendered as generic text.
	// Omitted when the server cannot mint one (no JWT auth or session).
	DisplayToken          string `json:"display_token,omitempty"`
	DisplayTokenExpiresAt string `json:"display_token_expires_at,omitempty"`
}

func (h *NotificationsHandler) pushDevices() *notifications.PushDeviceService {
	if h == nil || h.system == nil {
		return nil
	}
	return h.system.PushDevices
}

// HandleRegisterApplePushDevice handles POST /devices/push/apple.
func (h *NotificationsHandler) HandleRegisterApplePushDevice(w http.ResponseWriter, r *http.Request) {
	service := h.pushDevices()
	if service == nil || !service.Available() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Apple push registration is not available")
		return
	}

	var req applePushRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	device, err := service.RegisterApple(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), notifications.ApplePushRegistrationInput{
		DeviceID:        req.DeviceID,
		APNsToken:       req.APNsToken,
		APNsEnvironment: req.APNsEnvironment,
		APNsTopic:       req.APNsTopic,
		PushMode:        req.PushMode,
	})
	if err != nil {
		switch {
		case errors.Is(err, notifications.ErrPushDeviceInvalid):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		case errors.Is(err, notifications.ErrPushDeviceUnsupported):
			writeError(w, http.StatusUnprocessableEntity, "unsupported_push_device", err.Error())
		case errors.Is(err, notifications.ErrPushDeviceUnavailable):
			writeError(w, http.StatusServiceUnavailable, "unavailable", "Apple push registration is not available")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to register Apple push device")
		}
		return
	}

	response := applePushRegisterResponse{
		ID:             device.ID,
		ServerDeviceID: device.ServerDeviceID,
		Enabled:        device.Enabled,
		PushMode:       device.PushMode,
	}
	if claims := apimw.GetClaims(r.Context()); claims != nil && h.displayTokens != nil && claims.SessionID != "" {
		token, expiresAt, err := h.displayTokens.GenerateApplePushDisplayToken(
			claims.UserID, claims.Role, claims.SessionID, apimw.GetProfileID(r.Context()), claims.ImpersonatorUserID,
		)
		if err == nil {
			response.DisplayToken = token
			response.DisplayTokenExpiresAt = expiresAt.UTC().Format(time.RFC3339)
		}
		// A minting failure is not a registration failure: the device is
		// registered and the extension keeps its access-token fallback.
	}
	writeJSON(w, http.StatusOK, response)
}

type pushDeviceRegisterRequest struct {
	Platform string `json:"platform"`
	Token    string `json:"token"`
	DeviceID string `json:"device_id"`
	PushMode string `json:"push_mode"`
}

// HandleRegisterPushDevice handles POST /notifications/push/devices, the
// platform-generic registration used by the Android client. Apple installs
// keep the dedicated /devices/push/apple route because their registration
// carries APNs-specific fields (environment, topic).
func (h *NotificationsHandler) HandleRegisterPushDevice(w http.ResponseWriter, r *http.Request) {
	service := h.pushDevices()
	if service == nil || !service.Available() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Push registration is not available")
		return
	}

	var req pushDeviceRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Platform != notifications.PushPlatformAndroid {
		writeError(w, http.StatusUnprocessableEntity, "unsupported_push_device", "platform is not supported on this endpoint")
		return
	}

	device, err := service.RegisterFCM(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), notifications.FCMPushRegistrationInput{
		DeviceID: req.DeviceID,
		FCMToken: req.Token,
		PushMode: req.PushMode,
	})
	if err != nil {
		switch {
		case errors.Is(err, notifications.ErrPushDeviceInvalid):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		case errors.Is(err, notifications.ErrPushDeviceUnsupported):
			writeError(w, http.StatusUnprocessableEntity, "unsupported_push_device", err.Error())
		case errors.Is(err, notifications.ErrPushDeviceUnavailable):
			writeError(w, http.StatusServiceUnavailable, "unavailable", "Push registration is not available")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to register push device")
		}
		return
	}

	writeJSON(w, http.StatusOK, applePushRegisterResponse{
		ID:             device.ID,
		ServerDeviceID: device.ServerDeviceID,
		Enabled:        device.Enabled,
		PushMode:       device.PushMode,
	})
}

// HandleUnregisterPushDevice handles DELETE /notifications/push/devices/{device_id}.
func (h *NotificationsHandler) HandleUnregisterPushDevice(w http.ResponseWriter, r *http.Request) {
	service := h.pushDevices()
	if service == nil || !service.Available() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Push registration is not available")
		return
	}
	err := service.Unregister(r.Context(), apimw.GetProfileID(r.Context()), chi.URLParam(r, "device_id"))
	if err != nil {
		switch {
		case errors.Is(err, notifications.ErrPushDeviceInvalid):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		case errors.Is(err, notifications.ErrPushDeviceUnavailable):
			writeError(w, http.StatusServiceUnavailable, "unavailable", "Push registration is not available")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to unregister push device")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
