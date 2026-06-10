package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	webpush "github.com/SherClockHolmes/webpush-go"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/push"
)

// pushRegistry is the minimal interface PushHandler needs from *push.Store.
type pushRegistry interface {
	RegisterToken(ctx context.Context, userID int, profileID, deviceID, transport, token string) error
	RevokeToken(ctx context.Context, userID int, profileID, deviceID string) error
	SetDeviceEnabled(ctx context.Context, userID int, deviceID string, enabled bool) error
	ListDevices(ctx context.Context, userID int) ([]push.DeviceInfo, error)
}

// pushConfigReader is the minimal interface PushHandler needs from *push.Config.
type pushConfigReader interface {
	WebPush(ctx context.Context) push.WebPushConfig
	Status(ctx context.Context) push.Status
}

// pushNotifier is the minimal interface PushHandler needs to send system notifications.
type pushNotifier interface {
	CreateSystem(ctx context.Context, userID int, typ, title, body string)
}

// PushHandler handles push notification registration, management, and status endpoints.
type PushHandler struct {
	reg      pushRegistry
	config   pushConfigReader
	notifier pushNotifier
}

// NewPushHandler creates a new PushHandler. notifier may be nil (test-push endpoint
// returns 503 when nil).
func NewPushHandler(reg pushRegistry, config pushConfigReader, notifier pushNotifier) *PushHandler {
	return &PushHandler{reg: reg, config: config, notifier: notifier}
}

// registerRequest is the request body for HandleRegister.
type registerRequest struct {
	Transport string `json:"transport"`
	Token     string `json:"token"`
}

// HandleRegister handles PUT /notifications/push/device.
// Requires userID (from JWT claims), X-Silo-Device-Id, and X-Profile-Id headers.
// Body: {"transport": "apns"|"fcm"|"webpush", "token": "..."}
// Returns 204 on success.
func (h *PushHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Authentication required")
		return
	}

	deviceID := strings.TrimSpace(r.Header.Get(deviceIDHeader))
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "X-Silo-Device-Id header is required")
		return
	}

	profileID := strings.TrimSpace(apimw.GetProfileID(r.Context()))
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "X-Profile-Id header is required")
		return
	}

	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	switch req.Transport {
	case push.TransportAPNs, push.TransportFCM, push.TransportWebPush:
		// valid
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "transport must be one of apns, fcm, webpush")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "token must not be empty")
		return
	}

	if err := h.reg.RegisterToken(r.Context(), userID, profileID, deviceID, req.Transport, req.Token); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to register push token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleRevoke handles DELETE /notifications/push/device.
// Requires userID, X-Silo-Device-Id, and X-Profile-Id headers.
// Returns 204 on success.
func (h *PushHandler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Authentication required")
		return
	}

	deviceID := strings.TrimSpace(r.Header.Get(deviceIDHeader))
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "X-Silo-Device-Id header is required")
		return
	}

	profileID := strings.TrimSpace(apimw.GetProfileID(r.Context()))
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "X-Profile-Id header is required")
		return
	}

	if err := h.reg.RevokeToken(r.Context(), userID, profileID, deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to revoke push token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleListDevices handles GET /notifications/push/devices.
// Response: {"devices": [...]}
func (h *PushHandler) HandleListDevices(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())

	devices, err := h.reg.ListDevices(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list devices")
		return
	}
	if devices == nil {
		devices = []push.DeviceInfo{}
	}
	writeJSON(w, http.StatusOK, struct {
		Devices []push.DeviceInfo `json:"devices"`
	}{Devices: devices})
}

// toggleDeviceRequest is the request body for HandleToggleDevice.
type toggleDeviceRequest struct {
	Enabled bool `json:"enabled"`
}

// HandleToggleDevice handles PUT /notifications/push/devices/{device_id}.
// Body: {"enabled": true|false}
// Returns 204 on success, 404 if device not found.
func (h *PushHandler) HandleToggleDevice(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	deviceID := chi.URLParam(r, "device_id")

	var req toggleDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if err := h.reg.SetDeviceEnabled(r.Context(), userID, deviceID, req.Enabled); err != nil {
		if errors.Is(err, push.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleWebPushKey handles GET /notifications/push/webpush-key.
// Response: {"vapid_public_key": "..."} — empty string when not configured.
func (h *PushHandler) HandleWebPushKey(w http.ResponseWriter, r *http.Request) {
	cfg := h.config.WebPush(r.Context())
	writeJSON(w, http.StatusOK, struct {
		VAPIDPublicKey string `json:"vapid_public_key"`
	}{VAPIDPublicKey: cfg.VAPIDPublic})
}

// HandleAdminStatus handles GET /admin/push/status.
// Response: {"apns": bool, "fcm": bool, "webpush": bool}
func (h *PushHandler) HandleAdminStatus(w http.ResponseWriter, r *http.Request) {
	status := h.config.Status(r.Context())
	writeJSON(w, http.StatusOK, status)
}

// HandleGenerateVAPIDKeys handles POST /admin/push/generate-vapid-keys.
// Generates a fresh VAPID key pair and returns it as JSON.
// Response: {"vapid_public": "...", "vapid_private": "..."}
func (h *PushHandler) HandleGenerateVAPIDKeys(w http.ResponseWriter, r *http.Request) {
	// GenerateVAPIDKeys returns (privateKey, publicKey string, err error).
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate keys")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"vapid_public": pub, "vapid_private": priv})
}

// HandleSendTestPush handles POST /admin/push/test.
// Sends a system test push notification to the authenticated admin user.
func (h *PushHandler) HandleSendTestPush(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "notifications unavailable")
		return
	}
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "auth required")
		return
	}
	h.notifier.CreateSystem(r.Context(), userID, "system.test_push", "Test push",
		"If you can see this on a device, push delivery is working.")
	w.WriteHeader(http.StatusAccepted)
}
