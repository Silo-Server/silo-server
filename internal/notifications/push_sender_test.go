package notifications

import (
	"context"
	"encoding/json"
	"errors"
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
	if !settings.ApplePushDeliveryEnabled(ctx) {
		t.Fatal("ApplePushDeliveryEnabled must default to true")
	}
	if NewSettings(mapSettingReader{SettingApplePushDeliveryEnabled: "false"}).ApplePushDeliveryEnabled(ctx) {
		t.Fatal("ApplePushDeliveryEnabled = true with setting off")
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

func TestPushSenderSendBuildsFcmRelayRequest(t *testing.T) {
	token := strings.Repeat("F", 140)
	var got struct {
		auth           string
		idempotencyKey string
		body           pushRelayFcmRequest
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth = r.Header.Get("Authorization")
		got.idempotencyKey = r.Header.Get("Idempotency-Key")
		if r.URL.Path != relayFcmSendPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, relayFcmSendPath)
		}
		if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"request_id":     "relay-request-fcm-1",
			"fcm_message_id": "fcm-1",
			"status":         "accepted",
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

	deliveryID := "delivery-fcm-1"
	result := sender.send(context.Background(), PushDeliveryAttempt{
		ID:                     "attempt-fcm-1",
		NotificationDeliveryID: &deliveryID,
	}, &PushDevice{
		Platform:       PushPlatformAndroid,
		ServerDeviceID: "server-device-fcm",
	}, token)

	if !result.OK || result.RelayRequestID != "relay-request-fcm-1" {
		t.Fatalf("result = %+v", result)
	}
	if got.auth != "Bearer relay-key" || got.idempotencyKey != "attempt-fcm-1" {
		t.Fatalf("headers = %+v", got)
	}
	if got.body.Token != token || got.body.Mode != "private_alert" || got.body.DeliveryID != deliveryID {
		t.Fatalf("relay body = %+v", got.body)
	}
	if got.body.CollapseID == nil || *got.body.CollapseID != deliveryID {
		t.Fatalf("collapse_id = %+v, want delivery id", got.body.CollapseID)
	}
}

func TestPushSenderSendMapsRelayTerminalFCMRejection(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(pushRelayErrorResponse{
			Error: struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				RequestID string `json:"request_id"`
			}{
				Code:      "fcm_rejected",
				Message:   "FCM rejected the notification: UNREGISTERED",
				RequestID: "relay-request-fcm-2",
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

	result := sender.send(context.Background(), PushDeliveryAttempt{ID: "attempt-fcm-2"}, &PushDevice{
		Platform:       PushPlatformAndroid,
		ServerDeviceID: "server-device-fcm",
	}, strings.Repeat("F", 140))

	if result.OK || !result.TerminalDevice || result.HTTPStatus != http.StatusUnprocessableEntity {
		t.Fatalf("terminal result = %+v", result)
	}
	if result.UpstreamReason != "fcm_rejected" || result.RelayRequestID != "relay-request-fcm-2" {
		t.Fatalf("terminal diagnostic = %+v", result)
	}
}

func TestTerminalFCMDeviceRejectionReasons(t *testing.T) {
	if !terminalFCMDeviceRejection(http.StatusUnprocessableEntity, "fcm_rejected",
		"FCM rejected the notification: UNREGISTERED") {
		t.Fatal("UNREGISTERED was not terminal for the device")
	}
	if terminalFCMDeviceRejection(http.StatusUnprocessableEntity, "fcm_rejected",
		"FCM rejected the notification: INVALID_ARGUMENT") {
		t.Fatal("request-level FCM rejection was terminal for the device")
	}
	if terminalFCMDeviceRejection(http.StatusUnprocessableEntity, "apns_rejected",
		"APNs rejected the notification: Unregistered") {
		t.Fatal("APNs rejection satisfied the FCM matcher")
	}
	if terminalDeviceRejection(PushPlatformAndroid, http.StatusUnprocessableEntity, "fcm_rejected",
		"FCM rejected the notification: UNREGISTERED") !=
		terminalFCMDeviceRejection(http.StatusUnprocessableEntity, "fcm_rejected",
			"FCM rejected the notification: UNREGISTERED") {
		t.Fatal("platform dispatch mismatch")
	}
}

func TestAndroidPushDeliverySettings(t *testing.T) {
	ctx := context.Background()
	settings := NewSettings(nil)
	if !settings.AndroidPushDeliveryEnabled(ctx) {
		t.Fatal("AndroidPushDeliveryEnabled must default to true")
	}
	if NewSettings(mapSettingReader{SettingAndroidPushDeliveryEnabled: "false"}).AndroidPushDeliveryEnabled(ctx) {
		t.Fatal("AndroidPushDeliveryEnabled = true with setting off")
	}
	if got := settings.EnabledPushPlatforms(ctx); len(got) != 2 {
		t.Fatalf("EnabledPushPlatforms = %v, want both platforms by default", got)
	}
	settings = NewSettings(mapSettingReader{
		SettingApplePushDeliveryEnabled:   "false",
		SettingAndroidPushDeliveryEnabled: "false",
	})
	if settings.PushDeliveryEnabled(ctx) {
		t.Fatal("PushDeliveryEnabled must be false with both toggles off")
	}
	if got := settings.EnabledPushPlatforms(ctx); len(got) != 0 {
		t.Fatalf("EnabledPushPlatforms = %v, want empty", got)
	}

	settings = NewSettings(mapSettingReader{
		SettingApplePushDeliveryEnabled:   "false",
		SettingAndroidPushDeliveryEnabled: "true",
	})
	if !settings.AndroidPushDeliveryEnabled(ctx) || !settings.PushDeliveryEnabled(ctx) {
		t.Fatal("android delivery setting did not enable push delivery")
	}
	if got := settings.EnabledPushPlatforms(ctx); len(got) != 1 || got[0] != PushPlatformAndroid {
		t.Fatalf("EnabledPushPlatforms = %v, want [android]", got)
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

func TestPushSenderRegistersRelayOnFirstSendWithoutCredential(t *testing.T) {
	token := strings.Repeat("a", 64)
	var registerCalls int
	var sendAuth []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case relayRegisterPath:
			registerCalls++
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("register Authorization = %q, want none", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"request_id":    "register-request",
				"deployment_id": "deployment-auto",
				"api_key":       "auto-capability",
				"key_prefix":    "cap_v1_auto",
				"expires_at":    "2026-12-01T00:00:00Z",
			})
		case relayAppleSendPath:
			sendAuth = append(sendAuth, r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(pushRelayAppleResponse{
				RequestID: "relay-request-auto",
				APNsID:    "apns-auto",
				Status:    "accepted",
			})
		default:
			t.Fatalf("unexpected relay path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	store := mapSettingStore{SettingPushRelayURL: server.URL}
	sender := newPushSender(nil, nil, nil, NewSettings(store))
	sender.client = server.Client()
	sender.developmentRelayURL = server.URL
	device := &PushDevice{
		APNsEnvironment: APNsEnvironmentSandbox,
		APNsTopic:       ApplePushTopicSilo,
		ServerDeviceID:  "server-device-auto",
	}

	result := sender.send(context.Background(), PushDeliveryAttempt{ID: "attempt-auto-1"}, device, token)
	if !result.OK || result.RelayRequestID != "relay-request-auto" {
		t.Fatalf("result = %+v", result)
	}
	if got := store[SettingPushRelayAPIKey]; got != "auto-capability" {
		t.Fatalf("stored capability = %q", got)
	}
	if got := store[SettingPushRelayDeploymentID]; got != "deployment-auto" {
		t.Fatalf("stored deployment id = %q", got)
	}

	// The persisted credential is reused; no second registration.
	result = sender.send(context.Background(), PushDeliveryAttempt{ID: "attempt-auto-2"}, device, token)
	if !result.OK {
		t.Fatalf("second result = %+v", result)
	}
	if registerCalls != 1 {
		t.Fatalf("register calls = %d, want 1", registerCalls)
	}
	if len(sendAuth) != 2 || sendAuth[0] != "Bearer auto-capability" || sendAuth[1] != "Bearer auto-capability" {
		t.Fatalf("send Authorization headers = %#v", sendAuth)
	}
}

func TestPushSenderDoesNotAutoRegisterAfterAdminClear(t *testing.T) {
	registrations := 0
	store := mapSettingStore{SettingPushRelayReregister: "true"}
	sender := newPushSender(nil, nil, nil, NewSettings(store))
	sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		registrations++
		return relayResponse(http.StatusOK, credentialJSON("deployment-unwanted", "unwanted.capability", time.Now().Add(24*time.Hour))), nil
	})}

	_, err := sender.prepareRelayCredential(context.Background())
	if err == nil || registrations != 0 {
		t.Fatalf("err = %v, registrations = %d; cleared state must block first-use registration", err, registrations)
	}
	if got := store[SettingPushRelayAPIKey]; got != "" {
		t.Fatalf("stored key = %q, want empty", got)
	}
}

type erroringSettingReader struct{ err error }

func (r erroringSettingReader) Get(context.Context, string) (string, error) { return "", r.err }

func TestPushDeliveryEnabledFailsClosedOnReadError(t *testing.T) {
	ctx := context.Background()
	settings := NewSettings(erroringSettingReader{err: errors.New("db down")})
	if settings.ApplePushDeliveryEnabled(ctx) || settings.AndroidPushDeliveryEnabled(ctx) {
		t.Fatal("push delivery must report disabled when the settings row cannot be read")
	}
	if got := settings.EnabledPushPlatforms(ctx); len(got) != 0 {
		t.Fatalf("EnabledPushPlatforms = %v, want empty on read error", got)
	}
}

// rejectingRelay serves a relay whose send endpoint rejects every capability
// except accepted with the given 401 code, and whose register endpoint mints
// minted (accepted when empty).
type rejectingRelay struct {
	t             *testing.T
	accepted      string
	minted        string
	rejectCode    string
	renewCode     string
	registrations int
	sendAuth      []string
}

func (r *rejectingRelay) roundTrip(req *http.Request) (*http.Response, error) {
	switch req.URL.Path {
	case relayAppleSendPath:
		auth := req.Header.Get("Authorization")
		r.sendAuth = append(r.sendAuth, auth)
		if auth == "Bearer "+r.accepted {
			return relayResponse(http.StatusOK, `{"request_id":"relay-request-ok","status":"accepted"}`), nil
		}
		return relayResponse(http.StatusUnauthorized, `{"error":{"code":"`+r.rejectCode+`","message":"rejected"}}`), nil
	case relayRenewPath:
		return relayResponse(http.StatusUnauthorized, `{"error":{"code":"`+r.renewCode+`","message":"rejected"}}`), nil
	case relayRegisterPath:
		r.registrations++
		minted := r.minted
		if minted == "" {
			minted = r.accepted
		}
		return relayResponse(http.StatusOK, credentialJSON("deployment-fresh", minted, time.Now().Add(30*24*time.Hour))), nil
	default:
		r.t.Fatalf("unexpected relay path %q", req.URL.Path)
		return nil, nil
	}
}

func newRejectedCredentialSender(t *testing.T, relay *rejectingRelay) (*pushSender, *atomicRelaySettings) {
	t.Helper()
	store := &atomicRelaySettings{lockedRelaySettings: lockedRelaySettings{values: map[string]string{
		SettingPushRelayURL:          DefaultPushRelayURL,
		SettingPushRelayDeploymentID: "deployment-shared",
		SettingPushRelayAPIKey:       "superseded.capability",
		SettingPushRelayExpiresAt:    time.Now().Add(20 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}}}
	sender := newPushSender(nil, nil, nil, NewSettings(store))
	sender.client = &http.Client{Transport: relayRoundTripFunc(relay.roundTrip)}
	return sender, store
}

func sendTestPush(sender *pushSender, attemptID string) pushSendResult {
	return sender.send(context.Background(), PushDeliveryAttempt{ID: attemptID}, &PushDevice{
		APNsEnvironment: APNsEnvironmentSandbox,
		APNsTopic:       ApplePushTopicSilo,
		ServerDeviceID:  "server-device-rejected",
	}, strings.Repeat("a", 64))
}

func TestPushSenderReplacesRejectedCapabilityAndResends(t *testing.T) {
	// The relay rejects a capability whose generation another holder of the
	// same credential (for example a cloned database) rotated away.
	for _, code := range []string{"unauthorized", "capability_revoked"} {
		t.Run(code, func(t *testing.T) {
			relay := &rejectingRelay{t: t, accepted: "fresh.capability", rejectCode: code}
			sender, store := newRejectedCredentialSender(t, relay)

			result := sendTestPush(sender, "attempt-rejected")
			if !result.OK {
				t.Fatalf("result = %+v, want delivery after replacement", result)
			}
			if relay.registrations != 1 || len(relay.sendAuth) != 2 || relay.sendAuth[1] != "Bearer fresh.capability" {
				t.Fatalf("registrations = %d, send auth = %#v", relay.registrations, relay.sendAuth)
			}
			if store.values[SettingPushRelayAPIKey] != "fresh.capability" || store.values[SettingPushRelayReregister] != "false" {
				t.Fatalf("stored state = %#v", store.values)
			}
		})
	}
}

func TestPushSenderReplacesCapabilityWhoseRenewalIsRejected(t *testing.T) {
	// token_expired past the renewal grace, or a superseded generation, makes
	// renewal impossible; the sender registers instead of failing forever.
	relay := &rejectingRelay{t: t, accepted: "fresh.capability", rejectCode: "token_expired", renewCode: "token_expired"}
	sender, store := newRejectedCredentialSender(t, relay)

	result := sendTestPush(sender, "attempt-expired")
	if !result.OK || relay.registrations != 1 {
		t.Fatalf("result = %+v, registrations = %d", result, relay.registrations)
	}
	if store.values[SettingPushRelayAPIKey] != "fresh.capability" {
		t.Fatalf("stored key = %q", store.values[SettingPushRelayAPIKey])
	}
}

func TestPushSenderParksDeploymentTheRelayDisabled(t *testing.T) {
	relay := &rejectingRelay{t: t, accepted: "never.issued", rejectCode: relayCodeDeploymentDisabled}
	sender, store := newRejectedCredentialSender(t, relay)

	result := sendTestPush(sender, "attempt-disabled")
	if result.OK || result.HTTPStatus != http.StatusUnauthorized || result.UpstreamReason != relayCodeDeploymentDisabled {
		t.Fatalf("result = %+v, want the relay's disabled rejection", result)
	}
	if relay.registrations != 0 {
		t.Fatalf("registrations = %d; a disabled deployment must wait for the administrator", relay.registrations)
	}
	if store.values[SettingPushRelayAPIKey] != "" || store.values[SettingPushRelayReregister] != "true" {
		t.Fatalf("stored state = %#v, want parked", store.values)
	}
	if _, err := sender.prepareRelayCredential(context.Background()); !errors.Is(err, ErrRelayReregistrationRequired) {
		t.Fatalf("prepare after disable err = %v", err)
	}
}

func TestPushSenderRateLimitsCapabilityReplacement(t *testing.T) {
	// A relay that rejects even fresh credentials must not be hammered with
	// registrations.
	relay := &rejectingRelay{t: t, accepted: "never.issued", minted: "also.rejected", rejectCode: "unauthorized"}
	sender, store := newRejectedCredentialSender(t, relay)
	now := time.Now()
	sender.now = func() time.Time { return now }

	first := sendTestPush(sender, "attempt-first")
	second := sendTestPush(sender, "attempt-second")
	if first.OK || second.OK || relay.registrations != 1 {
		t.Fatalf("first = %+v, second = %+v, registrations = %d", first, second, relay.registrations)
	}
	if second.HTTPStatus != http.StatusServiceUnavailable || !retryableHTTPStatus(second.HTTPStatus) {
		t.Fatalf("second = %+v, want a retryable cooldown failure", second)
	}
	if store.values[SettingPushRelayReregister] == "true" {
		t.Fatal("rejection parked the credential")
	}

	now = now.Add(relayReplacementCooldown)
	_ = sendTestPush(sender, "attempt-after-cooldown")
	if relay.registrations != 2 {
		t.Fatalf("registrations after cooldown = %d, want 2", relay.registrations)
	}
}

func TestPushSenderKeepsCredentialRegisteredWhileParking(t *testing.T) {
	// An administrator registers while the relay's disabled response is in
	// flight; parking must not discard the new credential.
	relay := &rejectingRelay{t: t, accepted: "admin.capability", rejectCode: relayCodeDeploymentDisabled}
	sender, store := newRejectedCredentialSender(t, relay)
	store.beforeWrite = func(values map[string]string) {
		values[SettingPushRelayDeploymentID] = "deployment-admin"
		values[SettingPushRelayAPIKey] = "admin.capability"
	}

	result := sendTestPush(sender, "attempt-disabled-race")
	if !result.OK {
		t.Fatalf("result = %+v, want delivery with the administrator's credential", result)
	}
	if relay.registrations != 0 {
		t.Fatalf("registrations = %d", relay.registrations)
	}
	if store.values[SettingPushRelayAPIKey] != "admin.capability" || store.values[SettingPushRelayReregister] == "true" {
		t.Fatalf("stored state = %#v, want the administrator's credential kept", store.values)
	}
}
