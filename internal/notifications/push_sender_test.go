package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplePushDeliverySettings(t *testing.T) {
	ctx := context.Background()
	settings := NewSettings(nil)
	if settings.ApplePushDeliveryEnabled(ctx) {
		t.Fatal("ApplePushDeliveryEnabled must default to false")
	}
	if got := settings.PushRelayURL(ctx); got != DefaultPushRelayURL {
		t.Fatalf("PushRelayURL default = %q, want %q", got, DefaultPushRelayURL)
	}

	settings = NewSettings(mapSettingReader{
		SettingApplePushDeliveryEnabled: "true",
		SettingPushRelayURL:             "https://push.example.test/",
		SettingPushRelayAPIKey:          " relay-key ",
	})
	if !settings.ApplePushDeliveryEnabled(ctx) {
		t.Fatal("ApplePushDeliveryEnabled = false with setting on")
	}
	if got := settings.PushRelayURL(ctx); got != "https://push.example.test" {
		t.Fatalf("PushRelayURL = %q", got)
	}
	if got := settings.PushRelayAPIKey(ctx); got != "relay-key" {
		t.Fatalf("PushRelayAPIKey was not trimmed")
	}
}

func TestPushSenderSendBuildsRelayRequest(t *testing.T) {
	token := strings.Repeat("a", 64)
	var got struct {
		auth           string
		idempotencyKey string
		body           pushRelayAppleRequest
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth = r.Header.Get("Authorization")
		got.idempotencyKey = r.Header.Get("Idempotency-Key")
		if r.URL.Path != relayAppleSendPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, relayAppleSendPath)
		}
		if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(pushRelayAppleResponse{
			RequestID: "relay-request-1",
			APNsID:    "apns-1",
			Status:    "accepted",
		})
	}))
	defer server.Close()

	settings := NewSettings(mapSettingReader{
		SettingPushRelayURL:    server.URL,
		SettingPushRelayAPIKey: "relay-key",
	})
	sender := newPushSender(nil, nil, nil, settings)
	sender.client = server.Client()

	deliveryID := "delivery-1"
	result := sender.send(context.Background(), PushDeliveryAttempt{
		ID:                     "attempt-1",
		NotificationDeliveryID: &deliveryID,
		AttemptNumber:          1,
	}, &PushDevice{
		APNsEnvironment: APNsEnvironmentSandbox,
		APNsTopic:       ApplePushTopicSilo,
		ServerDeviceID:  "server-device-1",
	}, token)

	if !result.OK || result.RelayRequestID != "relay-request-1" {
		t.Fatalf("result = %+v", result)
	}
	if got.auth != "Bearer relay-key" {
		t.Fatalf("Authorization = %q", got.auth)
	}
	if got.idempotencyKey != "attempt-1:2" {
		t.Fatalf("Idempotency-Key = %q", got.idempotencyKey)
	}
	if got.body.Token != token || got.body.Mode != "private_alert" || got.body.DeliveryID != deliveryID {
		t.Fatalf("relay body = %+v", got.body)
	}
	if got.body.CollapseID == nil || *got.body.CollapseID != deliveryID {
		t.Fatalf("collapse_id = %+v, want delivery id", got.body.CollapseID)
	}
}

func TestPushSenderSendMapsRelayTerminalAPNsRejection(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(pushRelayErrorResponse{
			Error: struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				RequestID string `json:"request_id"`
			}{
				Code:      "apns_rejected",
				Message:   "APNs rejected the notification: BadDeviceToken",
				RequestID: "relay-request-2",
			},
		})
	}))
	defer server.Close()

	settings := NewSettings(mapSettingReader{
		SettingPushRelayURL:    server.URL,
		SettingPushRelayAPIKey: "relay-key",
	})
	sender := newPushSender(nil, nil, nil, settings)
	sender.client = server.Client()

	result := sender.send(context.Background(), PushDeliveryAttempt{ID: "attempt-1"}, &PushDevice{
		APNsEnvironment: APNsEnvironmentSandbox,
		APNsTopic:       ApplePushTopicSilo,
		ServerDeviceID:  "server-device-1",
	}, strings.Repeat("a", 64))

	if result.OK || !result.TerminalDevice || result.HTTPStatus != http.StatusUnprocessableEntity {
		t.Fatalf("terminal result = %+v", result)
	}
	if result.UpstreamReason != "apns_rejected" || result.RelayRequestID != "relay-request-2" {
		t.Fatalf("terminal diagnostic = %+v", result)
	}
}

func TestPushSenderSendMapsRelayRetryAfter(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(pushRelayErrorResponse{
			Error: struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				RequestID string `json:"request_id"`
			}{
				Code:      "upstream_rate_limited",
				Message:   "APNs upstream rate limited the request",
				RequestID: "relay-request-3",
			},
		})
	}))
	defer server.Close()

	settings := NewSettings(mapSettingReader{
		SettingPushRelayURL:    server.URL,
		SettingPushRelayAPIKey: "relay-key",
	})
	sender := newPushSender(nil, nil, nil, settings)
	sender.client = server.Client()

	result := sender.send(context.Background(), PushDeliveryAttempt{ID: "attempt-1"}, &PushDevice{
		APNsEnvironment: APNsEnvironmentSandbox,
		APNsTopic:       ApplePushTopicSilo,
		ServerDeviceID:  "server-device-1",
	}, strings.Repeat("a", 64))

	if result.OK || result.TerminalDevice || result.RetryAfter == 0 {
		t.Fatalf("retryable result = %+v", result)
	}
}
