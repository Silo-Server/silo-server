package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

func TestAdminApplePushHandlerUnavailableWithoutSystem(t *testing.T) {
	h := NewAdminApplePushHandler(nil, nil)
	rec := httptest.NewRecorder()
	h.HandleTest(rec, httptest.NewRequest(http.MethodPost, "/admin/notifications/push/apple/test", strings.NewReader(`{}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("HandleTest without system = %d, want 503", rec.Code)
	}
}

func TestAdminApplePushHandlerRejectsInvalidJSON(t *testing.T) {
	h := NewAdminApplePushHandler(&notifications.System{}, nil)
	rec := httptest.NewRecorder()
	h.HandleTest(rec, httptest.NewRequest(http.MethodPost, "/admin/notifications/push/apple/test", strings.NewReader(`{`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("HandleTest invalid JSON = %d, want 400", rec.Code)
	}
}

func TestAdminApplePushHandlerRegistersRelayAndStoresKey(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{
		notifications.SettingPushRelayDeploymentID: "01EXISTING",
	}}
	var relayReq map[string]string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/deployments/register" {
			t.Fatalf("relay path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&relayReq); err != nil {
			t.Fatalf("decode relay request: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"request_id":    "relay-request",
			"deployment_id": "01RETURNED",
			"api_key":       "modern.capability.value",
			"key_prefix":    "cap_v1_test",
			"apns_topics":   []string{"org.siloserver.silo"},
			"expires_at":    "2026-08-10T00:00:00Z",
		})
	}))
	t.Cleanup(relay.Close)

	h := NewAdminApplePushHandler(&notifications.System{Settings: notifications.NewSettings(settings)}, settings)
	h.client = relay.Client()

	rec := httptest.NewRecorder()
	h.HandleRegisterRelay(rec, httptest.NewRequest(http.MethodPost, "/admin/notifications/push/relay/register", strings.NewReader(`{
		"relay_url":"`+relay.URL+`"
	}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("http relay URL status = %d, want 400 because HTTPS is required", rec.Code)
	}

	httpsRelay := httptest.NewTLSServer(relay.Config.Handler)
	t.Cleanup(httpsRelay.Close)
	h.client = httpsRelay.Client()
	h.developmentRelayURL = httpsRelay.URL
	rec = httptest.NewRecorder()
	h.HandleRegisterRelay(rec, httptest.NewRequest(http.MethodPost, "/admin/notifications/push/relay/register", strings.NewReader(`{
		"relay_url":"`+httpsRelay.URL+`"
	}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if len(relayReq) != 0 {
		t.Fatalf("relay request = %+v", relayReq)
	}
	if settings.values[notifications.SettingPushRelayURL] != httpsRelay.URL {
		t.Fatalf("stored relay URL = %q", settings.values[notifications.SettingPushRelayURL])
	}
	if settings.values[notifications.SettingPushRelayDeploymentID] != "01RETURNED" {
		t.Fatalf("stored deployment id = %q", settings.values[notifications.SettingPushRelayDeploymentID])
	}
	if settings.values[notifications.SettingPushRelayAPIKey] != "modern.capability.value" {
		t.Fatalf("stored api key = %q", settings.values[notifications.SettingPushRelayAPIKey])
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := body["api_key"]; ok {
		t.Fatal("response leaked raw api_key")
	}
	if body["api_key_configured"] != true || body["deployment_id"] != "01RETURNED" {
		t.Fatalf("response = %+v", body)
	}
}

func TestAdminApplePushHandlerMapsRelayRateLimit(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": map[string]string{"code": "rate_limited", "message": "too many deployment registrations from this network"},
		})
	}))
	t.Cleanup(relay.Close)

	h := NewAdminApplePushHandler(&notifications.System{}, settings)
	h.client = relay.Client()
	h.developmentRelayURL = relay.URL
	rec := httptest.NewRecorder()
	h.HandleRegisterRelay(rec, httptest.NewRequest(http.MethodPost, "/admin/notifications/push/relay/register", strings.NewReader(`{
		"relay_url":"`+relay.URL+`"
	}`)))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d (%s), want 429", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}
	if settings.values[notifications.SettingPushRelayAPIKey] != "" {
		t.Fatal("api key was stored after relay rejected registration")
	}
}

func TestAdminApplePushHandlerRotatesExistingCapability(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{
		notifications.SettingPushRelayDeploymentID: "deployment-existing",
		notifications.SettingPushRelayAPIKey:       "existing.capability.value",
		notifications.SettingPushRelayKeyPrefix:    "cap_v1_existing",
		notifications.SettingPushRelayExpiresAt:    "2026-08-01T00:00:00Z",
	}}
	var idempotencyKey string
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/deployments/rotate" {
			t.Fatalf("relay path = %q, want rotate", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer existing.capability.value" {
			t.Fatalf("Authorization = %q", got)
		}
		idempotencyKey = r.Header.Get("Idempotency-Key")
		writeJSON(w, http.StatusOK, map[string]any{
			"request_id":    "rotate-request",
			"deployment_id": "deployment-existing",
			"api_key":       "rotated.capability.value",
			"key_prefix":    "cap_v1_rotated",
			"expires_at":    "2026-09-01T00:00:00Z",
		})
	}))
	t.Cleanup(relay.Close)
	settings.values[notifications.SettingPushRelayURL] = relay.URL
	h := NewAdminApplePushHandler(&notifications.System{Settings: notifications.NewSettings(settings)}, settings)
	h.client = relay.Client()
	h.developmentRelayURL = relay.URL

	rec := httptest.NewRecorder()
	h.HandleRegisterRelay(rec, httptest.NewRequest(http.MethodPost, "/admin/notifications/push/relay/register", strings.NewReader(`{"relay_url":"`+relay.URL+`"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if idempotencyKey == "" {
		t.Fatal("rotation omitted Idempotency-Key")
	}
	if got := settings.values[notifications.SettingPushRelayAPIKey]; got != "rotated.capability.value" {
		t.Fatalf("stored capability = %q", got)
	}
}
