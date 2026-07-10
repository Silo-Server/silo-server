package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mapSettingStore map[string]string

func (m mapSettingStore) Get(_ context.Context, key string) (string, error) {
	return m[key], nil
}

func (m mapSettingStore) Set(_ context.Context, key, value string) error {
	m[key] = value
	return nil
}

func (m mapSettingStore) SetMany(_ context.Context, values map[string]string) error {
	for key, value := range values {
		m[key] = value
	}
	return nil
}

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
	sender.developmentRelayURL = server.URL

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
	if got.idempotencyKey != "attempt-1" {
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
	sender.developmentRelayURL = server.URL

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

func TestTerminalAPNsDeviceRejectionReasons(t *testing.T) {
	for _, reason := range []string{
		"BadDeviceToken",
		"InvalidToken",
		"DeviceTokenNotForTopic",
		"Unregistered",
	} {
		t.Run(reason, func(t *testing.T) {
			message := "APNs rejected the notification: " + reason
			if !terminalAPNsDeviceRejection(http.StatusUnprocessableEntity, "apns_rejected", message) {
				t.Fatalf("reason %q was not terminal for the device", reason)
			}
		})
	}

	if terminalAPNsDeviceRejection(
		http.StatusUnprocessableEntity,
		"apns_rejected",
		"APNs rejected the notification: PayloadTooLarge",
	) {
		t.Fatal("request-level APNs rejection was terminal for the device")
	}
}

func TestPushSenderDoesNotDisableDeviceForRequestLevelAPNsRejection(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(pushRelayErrorResponse{
			Error: struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				RequestID string `json:"request_id"`
			}{
				Code:      "apns_rejected",
				Message:   "APNs rejected the notification: PayloadTooLarge",
				RequestID: "relay-request-request-rejection",
			},
		})
	}))
	defer server.Close()

	sender := newPushSender(nil, nil, nil, NewSettings(mapSettingReader{
		SettingPushRelayURL:    server.URL,
		SettingPushRelayAPIKey: "relay-key",
	}))
	sender.client = server.Client()
	sender.developmentRelayURL = server.URL

	result := sender.send(context.Background(), PushDeliveryAttempt{ID: "attempt-1"}, &PushDevice{
		APNsEnvironment: APNsEnvironmentSandbox,
		APNsTopic:       ApplePushTopicSilo,
		ServerDeviceID:  "server-device-1",
	}, strings.Repeat("a", 64))

	if result.OK || result.TerminalDevice || result.HTTPStatus != http.StatusUnprocessableEntity {
		t.Fatalf("request-level rejection result = %+v", result)
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
	sender.developmentRelayURL = server.URL

	result := sender.send(context.Background(), PushDeliveryAttempt{ID: "attempt-1"}, &PushDevice{
		APNsEnvironment: APNsEnvironmentSandbox,
		APNsTopic:       ApplePushTopicSilo,
		ServerDeviceID:  "server-device-1",
	}, strings.Repeat("a", 64))

	if result.OK || result.TerminalDevice || result.RetryAfter == 0 {
		t.Fatalf("retryable result = %+v", result)
	}
}

func TestPushSenderUsesRelayAwareTimeoutAndRetryHorizon(t *testing.T) {
	sender := newPushSender(nil, nil, nil, NewSettings(mapSettingStore{}))
	if sender.client.Timeout != pushRelayRequestTimeout {
		t.Fatalf("relay client timeout = %s, want %s", sender.client.Timeout, pushRelayRequestTimeout)
	}

	if delay, more := pushRetryDelayWithHint(1, 10*time.Second); !more || delay != 10*time.Second {
		t.Fatalf("short Retry-After delay = %s, more = %v", delay, more)
	}
	if delay, more := pushRetryDelayWithHint(1, 24*time.Hour); !more || delay != pushRelayMaxRetryAfter {
		t.Fatalf("capped Retry-After delay = %s, more = %v", delay, more)
	}
}

func TestPushSenderRenewsExpiredCapabilityAndRetriesStableDelivery(t *testing.T) {
	token := strings.Repeat("a", 64)
	var sendKeys []string
	var sendAuth []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case relayAppleSendPath:
			sendKeys = append(sendKeys, r.Header.Get("Idempotency-Key"))
			sendAuth = append(sendAuth, r.Header.Get("Authorization"))
			if len(sendAuth) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(pushRelayErrorResponse{Error: struct {
					Code      string `json:"code"`
					Message   string `json:"message"`
					RequestID string `json:"request_id"`
				}{Code: "token_expired", Message: "relay capability has expired"}})
				return
			}
			_ = json.NewEncoder(w).Encode(pushRelayAppleResponse{
				RequestID: "relay-request-renewed",
				APNsID:    "apns-renewed",
				Status:    "accepted",
			})
		case relayRenewPath:
			if got := r.Header.Get("Authorization"); got != "Bearer expired-capability" {
				t.Fatalf("renew Authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"request_id":    "renew-request",
				"deployment_id": "deployment-renew",
				"api_key":       "renewed-capability",
				"key_prefix":    "cap_v1_renewed",
				"expires_at":    "2026-08-10T00:00:00Z",
			})
		default:
			t.Fatalf("unexpected relay path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	store := mapSettingStore{
		SettingPushRelayURL:          server.URL,
		SettingPushRelayDeploymentID: "deployment-renew",
		SettingPushRelayAPIKey:       "expired-capability",
	}
	sender := newPushSender(nil, nil, nil, NewSettings(store))
	sender.client = server.Client()
	sender.developmentRelayURL = server.URL
	result := sender.send(context.Background(), PushDeliveryAttempt{ID: "attempt-renew"}, &PushDevice{
		APNsEnvironment: APNsEnvironmentSandbox,
		APNsTopic:       ApplePushTopicSilo,
		ServerDeviceID:  "server-device-renew",
	}, token)

	if !result.OK || result.RelayRequestID != "relay-request-renewed" {
		t.Fatalf("result = %+v", result)
	}
	if got := store[SettingPushRelayAPIKey]; got != "renewed-capability" {
		t.Fatalf("stored renewed capability = %q", got)
	}
	if len(sendKeys) != 2 || sendKeys[0] != "attempt-renew" || sendKeys[1] != "attempt-renew" {
		t.Fatalf("send Idempotency-Keys = %#v", sendKeys)
	}
	if len(sendAuth) != 2 || sendAuth[1] != "Bearer renewed-capability" {
		t.Fatalf("send Authorization headers = %#v", sendAuth)
	}
}

func TestPushSenderMapsRelayIdempotencyStatesWithStableKey(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		code      string
		retryable bool
	}{
		{name: "unknown", status: http.StatusConflict, code: "delivery_unknown", retryable: false},
		{name: "in progress", status: http.StatusTooEarly, code: "idempotency_in_progress", retryable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var keys []string
			sender := newPushSender(nil, nil, nil, NewSettings(mapSettingStore{}))
			sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				keys = append(keys, req.Header.Get("Idempotency-Key"))
				return relayResponse(tc.status, `{"error":{"code":"`+tc.code+`","message":"relay state"}}`), nil
			})}
			attempt := PushDeliveryAttempt{ID: "attempt-stable"}
			device := &PushDevice{
				APNsEnvironment: APNsEnvironmentSandbox,
				APNsTopic:       ApplePushTopicSilo,
				ServerDeviceID:  "server-device-stable",
			}
			first := sender.sendWithCapability(context.Background(), attempt, device, strings.Repeat("a", 64), DefaultPushRelayURL, "capability")
			second := sender.sendWithCapability(context.Background(), attempt, device, strings.Repeat("a", 64), DefaultPushRelayURL, "capability")
			if first.HTTPStatus != tc.status || first.UpstreamReason != tc.code {
				t.Fatalf("result = %+v", first)
			}
			if retryableHTTPStatus(first.HTTPStatus) != tc.retryable {
				t.Fatalf("retryable = %v, want %v", retryableHTTPStatus(first.HTTPStatus), tc.retryable)
			}
			if second.UpstreamReason != tc.code || len(keys) != 2 || keys[0] != "attempt-stable" || keys[1] != keys[0] {
				t.Fatalf("retry result = %+v, keys = %#v", second, keys)
			}
		})
	}
}
