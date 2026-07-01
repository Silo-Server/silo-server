package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/secret"
)

type handlerPushStore struct {
	got    notifications.ApplePushDeviceRegistration
	calls  int
	device *notifications.PushDevice
	err    error
}

func (f *handlerPushStore) UpsertApple(ctx context.Context, registration notifications.ApplePushDeviceRegistration, cipher *secret.Cipher) (*notifications.PushDevice, error) {
	f.calls++
	f.got = registration
	if f.err != nil {
		return nil, f.err
	}
	if f.device != nil {
		return f.device, nil
	}
	return &notifications.PushDevice{
		ID:             "push-row",
		ServerDeviceID: "server-device",
		Enabled:        true,
		PushMode:       registration.PushMode,
	}, nil
}

func handlerPushCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	return cipher
}

func newApplePushRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/push/apple", strings.NewReader(body))
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 42, Role: "user", TokenType: auth.TokenTypeAccess})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	return req.WithContext(ctx)
}

func TestHandleRegisterApplePushDevice(t *testing.T) {
	store := &handlerPushStore{}
	handler := NewNotificationsHandler(&notifications.System{
		PushDevices: notifications.NewPushDeviceService(store, handlerPushCipher(t)),
	}, nil)

	body := `{
		"device_id":"local-device",
		"apns_token":"` + strings.Repeat("a", 64) + `",
		"apns_environment":"production",
		"apns_topic":"org.siloserver.silo",
		"push_mode":"private_push"
	}`
	rr := httptest.NewRecorder()
	handler.HandleRegisterApplePushDevice(rr, newApplePushRequest(body))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response applePushRegisterResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ID != "push-row" || response.ServerDeviceID != "server-device" || !response.Enabled || response.PushMode != notifications.PushModePrivatePush {
		t.Fatalf("unexpected response: %+v", response)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
	if store.got.UserID != 42 || store.got.ProfileID != "profile-1" || store.got.DeviceID != "local-device" {
		t.Fatalf("unexpected stored registration: %+v", store.got)
	}
}

func TestHandleRegisterApplePushDeviceErrors(t *testing.T) {
	tests := []struct {
		name       string
		system     *notifications.System
		body       string
		wantStatus int
	}{
		{
			name:       "service unavailable",
			system:     &notifications.System{},
			body:       `{}`,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "invalid json",
			system: &notifications.System{
				PushDevices: notifications.NewPushDeviceService(&handlerPushStore{}, handlerPushCipher(t)),
			},
			body:       `{`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid field",
			system: &notifications.System{
				PushDevices: notifications.NewPushDeviceService(&handlerPushStore{}, handlerPushCipher(t)),
			},
			body: `{
				"device_id":"local-device",
				"apns_token":"abcd",
				"apns_environment":"production",
				"apns_topic":"org.siloserver.silo",
				"push_mode":"private_push"
			}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "unsupported topic",
			system: &notifications.System{
				PushDevices: notifications.NewPushDeviceService(&handlerPushStore{}, handlerPushCipher(t)),
			},
			body: `{
				"device_id":"local-device",
				"apns_token":"` + strings.Repeat("a", 64) + `",
				"apns_environment":"production",
				"apns_topic":"com.example.app",
				"push_mode":"private_push"
			}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewNotificationsHandler(tt.system, nil)
			rr := httptest.NewRecorder()
			handler.HandleRegisterApplePushDevice(rr, newApplePushRequest(tt.body))
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}
