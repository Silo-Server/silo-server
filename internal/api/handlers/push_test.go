package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/push"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakePushRegistry struct {
	registerErr error
	revokeErr   error
	enableErr   error
	devices     []push.DeviceInfo

	// capture last RegisterToken call
	lastUserID    int
	lastProfileID string
	lastDeviceID  string
	lastTransport string
	lastToken     string
}

func (f *fakePushRegistry) RegisterToken(_ context.Context, userID int, profileID, deviceID, transport, token string) error {
	f.lastUserID = userID
	f.lastProfileID = profileID
	f.lastDeviceID = deviceID
	f.lastTransport = transport
	f.lastToken = token
	return f.registerErr
}

func (f *fakePushRegistry) RevokeToken(_ context.Context, _ int, _, _ string) error {
	return f.revokeErr
}

func (f *fakePushRegistry) SetDeviceEnabled(_ context.Context, _ int, _ string, _ bool) error {
	return f.enableErr
}

func (f *fakePushRegistry) ListDevices(_ context.Context, _ int) ([]push.DeviceInfo, error) {
	return f.devices, nil
}

type fakePushConfig struct {
	webPush push.WebPushConfig
	status  push.Status
}

func (f *fakePushConfig) WebPush(_ context.Context) push.WebPushConfig { return f.webPush }
func (f *fakePushConfig) Status(_ context.Context) push.Status         { return f.status }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// pushUserRequest builds a request with JWT claims (userID) and optionally
// sets the device-id and profile-id headers.
func pushUserRequest(method, target string, body []byte, userID int, profileID, deviceID string) *http.Request {
	var req *http.Request
	if len(body) > 0 {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: userID, Role: "user"})
	if profileID != "" {
		ctx = apimw.SetProfileID(ctx, profileID)
	}
	req = req.WithContext(ctx)
	if deviceID != "" {
		req.Header.Set(deviceIDHeader, deviceID)
	}
	return req
}

// pushChiRequest injects a chi URL parameter into the request context.
func pushChiRequest(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// newPushHandler builds a PushHandler backed by the given fakes.
func newPushHandler(reg *fakePushRegistry, cfg *fakePushConfig) *PushHandler {
	return NewPushHandler(reg, cfg)
}

// ---------------------------------------------------------------------------
// HandleRegister
// ---------------------------------------------------------------------------

func TestPushHandleRegister_MissingDeviceID_Returns400(t *testing.T) {
	h := newPushHandler(&fakePushRegistry{}, &fakePushConfig{})
	rec := httptest.NewRecorder()
	body := mustMarshal(t, map[string]string{"transport": "apns", "token": "tok"})
	// no device header
	req := pushUserRequest(http.MethodPut, "/notifications/push/device", body, 1, "prof-1", "")
	h.HandleRegister(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPushHandleRegister_MissingProfileID_Returns400(t *testing.T) {
	h := newPushHandler(&fakePushRegistry{}, &fakePushConfig{})
	rec := httptest.NewRecorder()
	body := mustMarshal(t, map[string]string{"transport": "apns", "token": "tok"})
	// device header set but no profile
	req := pushUserRequest(http.MethodPut, "/notifications/push/device", body, 1, "", "dev-1")
	h.HandleRegister(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPushHandleRegister_BadTransport_Returns400(t *testing.T) {
	h := newPushHandler(&fakePushRegistry{}, &fakePushConfig{})
	rec := httptest.NewRecorder()
	body := mustMarshal(t, map[string]string{"transport": "smoke-signals", "token": "tok"})
	req := pushUserRequest(http.MethodPut, "/notifications/push/device", body, 1, "prof-1", "dev-1")
	h.HandleRegister(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPushHandleRegister_EmptyToken_Returns400(t *testing.T) {
	h := newPushHandler(&fakePushRegistry{}, &fakePushConfig{})
	rec := httptest.NewRecorder()
	body := mustMarshal(t, map[string]string{"transport": "apns", "token": ""})
	req := pushUserRequest(http.MethodPut, "/notifications/push/device", body, 1, "prof-1", "dev-1")
	h.HandleRegister(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPushHandleRegister_HappyPath_Returns204(t *testing.T) {
	reg := &fakePushRegistry{}
	h := newPushHandler(reg, &fakePushConfig{})
	rec := httptest.NewRecorder()
	body := mustMarshal(t, map[string]string{"transport": "fcm", "token": "my-fcm-token"})
	req := pushUserRequest(http.MethodPut, "/notifications/push/device", body, 42, "prof-99", "dev-abc")
	h.HandleRegister(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
	if reg.lastUserID != 42 {
		t.Errorf("userID = %d, want 42", reg.lastUserID)
	}
	if reg.lastProfileID != "prof-99" {
		t.Errorf("profileID = %q, want prof-99", reg.lastProfileID)
	}
	if reg.lastDeviceID != "dev-abc" {
		t.Errorf("deviceID = %q, want dev-abc", reg.lastDeviceID)
	}
	if reg.lastTransport != "fcm" {
		t.Errorf("transport = %q, want fcm", reg.lastTransport)
	}
	if reg.lastToken != "my-fcm-token" {
		t.Errorf("token = %q, want my-fcm-token", reg.lastToken)
	}
}

func TestPushHandleRegister_WebPushTransport_Returns204(t *testing.T) {
	reg := &fakePushRegistry{}
	h := newPushHandler(reg, &fakePushConfig{})
	rec := httptest.NewRecorder()
	body := mustMarshal(t, map[string]string{"transport": "webpush", "token": "{}"})
	req := pushUserRequest(http.MethodPut, "/notifications/push/device", body, 1, "prof-1", "dev-1")
	h.HandleRegister(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// HandleRevoke
// ---------------------------------------------------------------------------

func TestPushHandleRevoke_HappyPath_Returns204(t *testing.T) {
	h := newPushHandler(&fakePushRegistry{}, &fakePushConfig{})
	rec := httptest.NewRecorder()
	req := pushUserRequest(http.MethodDelete, "/notifications/push/device", nil, 1, "prof-1", "dev-1")
	h.HandleRevoke(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestPushHandleRevoke_MissingDevice_Returns400(t *testing.T) {
	h := newPushHandler(&fakePushRegistry{}, &fakePushConfig{})
	rec := httptest.NewRecorder()
	req := pushUserRequest(http.MethodDelete, "/notifications/push/device", nil, 1, "prof-1", "")
	h.HandleRevoke(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// HandleListDevices
// ---------------------------------------------------------------------------

func TestPushHandleListDevices_ReturnsDevicesJSON(t *testing.T) {
	now := time.Now().UTC()
	reg := &fakePushRegistry{
		devices: []push.DeviceInfo{
			{DeviceID: "dev-1", Name: "iPhone", Platform: "ios", Transport: push.TransportAPNs, PushEnabled: true, RegisteredAt: &now},
			{DeviceID: "dev-2", Name: "Chrome", Platform: "web", Transport: push.TransportWebPush, PushEnabled: false},
		},
	}
	h := newPushHandler(reg, &fakePushConfig{})
	rec := httptest.NewRecorder()
	req := pushUserRequest(http.MethodGet, "/notifications/push/devices", nil, 5, "prof-1", "")
	h.HandleListDevices(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Devices []push.DeviceInfo `json:"devices"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Devices) != 2 {
		t.Fatalf("devices count = %d, want 2", len(resp.Devices))
	}
	if resp.Devices[0].DeviceID != "dev-1" {
		t.Errorf("devices[0].DeviceID = %q, want dev-1", resp.Devices[0].DeviceID)
	}
}

func TestPushHandleListDevices_EmptySlice_ReturnsEmptyArray(t *testing.T) {
	reg := &fakePushRegistry{devices: nil}
	h := newPushHandler(reg, &fakePushConfig{})
	rec := httptest.NewRecorder()
	req := pushUserRequest(http.MethodGet, "/notifications/push/devices", nil, 1, "", "")
	h.HandleListDevices(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// Must produce {"devices":[]} not {"devices":null}
	if !containsJSON(body, `"devices":[]`) {
		t.Errorf("expected empty array in response, got: %s", body)
	}
}

// ---------------------------------------------------------------------------
// HandleToggleDevice
// ---------------------------------------------------------------------------

func TestPushHandleToggleDevice_HappyPath_Returns204(t *testing.T) {
	h := newPushHandler(&fakePushRegistry{}, &fakePushConfig{})
	rec := httptest.NewRecorder()
	body := mustMarshal(t, map[string]bool{"enabled": true})
	req := pushUserRequest(http.MethodPut, "/notifications/push/devices/dev-1", body, 1, "", "")
	req = pushChiRequest(req, "device_id", "dev-1")
	h.HandleToggleDevice(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPushHandleToggleDevice_NotFound_Returns404(t *testing.T) {
	reg := &fakePushRegistry{enableErr: push.ErrNotFound}
	h := newPushHandler(reg, &fakePushConfig{})
	rec := httptest.NewRecorder()
	body := mustMarshal(t, map[string]bool{"enabled": false})
	req := pushUserRequest(http.MethodPut, "/notifications/push/devices/dev-x", body, 1, "", "")
	req = pushChiRequest(req, "device_id", "dev-x")
	h.HandleToggleDevice(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPushHandleToggleDevice_WrappedNotFound_Returns404(t *testing.T) {
	reg := &fakePushRegistry{enableErr: errors.Join(errors.New("outer"), push.ErrNotFound)}
	h := newPushHandler(reg, &fakePushConfig{})
	rec := httptest.NewRecorder()
	body := mustMarshal(t, map[string]bool{"enabled": false})
	req := pushUserRequest(http.MethodPut, "/notifications/push/devices/dev-x", body, 1, "", "")
	req = pushChiRequest(req, "device_id", "dev-x")
	h.HandleToggleDevice(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// HandleWebPushKey
// ---------------------------------------------------------------------------

func TestPushHandleWebPushKey_ReturnsConfiguredKey(t *testing.T) {
	cfg := &fakePushConfig{webPush: push.WebPushConfig{VAPIDPublic: "BPublicKey123", VAPIDPrivate: "priv", Subject: "mailto:a@b.com"}}
	h := newPushHandler(&fakePushRegistry{}, cfg)
	rec := httptest.NewRecorder()
	req := pushUserRequest(http.MethodGet, "/notifications/push/webpush-key", nil, 1, "", "")
	h.HandleWebPushKey(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		VAPIDPublicKey string `json:"vapid_public_key"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VAPIDPublicKey != "BPublicKey123" {
		t.Errorf("vapid_public_key = %q, want BPublicKey123", resp.VAPIDPublicKey)
	}
}

func TestPushHandleWebPushKey_Unconfigured_ReturnsEmptyString(t *testing.T) {
	cfg := &fakePushConfig{webPush: push.WebPushConfig{}}
	h := newPushHandler(&fakePushRegistry{}, cfg)
	rec := httptest.NewRecorder()
	req := pushUserRequest(http.MethodGet, "/notifications/push/webpush-key", nil, 0, "", "")
	h.HandleWebPushKey(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		VAPIDPublicKey string `json:"vapid_public_key"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VAPIDPublicKey != "" {
		t.Errorf("vapid_public_key = %q, want empty string", resp.VAPIDPublicKey)
	}
}

// ---------------------------------------------------------------------------
// HandleAdminStatus
// ---------------------------------------------------------------------------

func TestPushHandleAdminStatus_ReturnsBooleans(t *testing.T) {
	cfg := &fakePushConfig{status: push.Status{APNs: true, FCM: false, WebPush: true}}
	h := newPushHandler(&fakePushRegistry{}, cfg)
	rec := httptest.NewRecorder()
	req := pushUserRequest(http.MethodGet, "/admin/push/status", nil, 1, "", "")
	h.HandleAdminStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp push.Status
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.APNs {
		t.Error("apns = false, want true")
	}
	if resp.FCM {
		t.Error("fcm = true, want false")
	}
	if !resp.WebPush {
		t.Error("webpush = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// containsJSON checks that s contains the substring (after stripping whitespace
// equivalents that json.Marshal doesn't emit — good enough for our payloads).
func containsJSON(s, sub string) bool {
	return len(s) > 0 && (len(sub) == 0 || len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
