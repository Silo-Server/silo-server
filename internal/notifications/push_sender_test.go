package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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

// newRelayTestSender sends through server as the push relay, with an API key.
func newRelayTestSender(server *httptest.Server) *pushSender {
	sender := newPushSender(nil, nil, nil, NewSettings(mapSettingReader{
		SettingPushRelayURL:    server.URL,
		SettingPushRelayAPIKey: "relay-key",
	}))
	sender.client = server.Client()
	sender.developmentRelayURL = server.URL
	return sender
}

// A relay that predates server_id and profile_id rejects them as unexpected
// before recording the idempotency key. The attempt is resent without them
// under the same key, and later sends to that relay leave them out until the
// recheck window passes. Any other unexpected field keeps its ordinary failure.
func TestPushSenderResendsWithoutTargetToARelayThatPredatesIt(t *testing.T) {
	type sent struct {
		key      string
		targeted bool
	}
	var requests []sent
	rejectField := "server_id"
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	sender := newPushSender(nil, nil, nil, NewSettings(mapSettingStore{}))
	sender.now = func() time.Time { return now }
	sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		_, targeted := body["server_id"]
		requests = append(requests, sent{key: req.Header.Get("Idempotency-Key"), targeted: targeted})
		if targeted {
			return relayResponse(http.StatusBadRequest, `{"error":{"code":"unexpected_field","message":"unexpected field: `+rejectField+`"}}`), nil
		}
		return relayResponse(http.StatusOK, `{"request_id":"relay-request","status":"accepted"}`), nil
	})}
	device := &PushDevice{Platform: PushPlatformAndroid, ProfileID: "profile-1", ServerDeviceID: "server-device-1"}
	target := pushRelayTarget{ServerID: "server-1", ProfileID: "profile-1"}
	send := func(attemptID string) pushSendResult {
		requests = nil
		return sender.sendWithCapability(context.Background(), PushDeliveryAttempt{ID: attemptID}, device, strings.Repeat("F", 140), target, DefaultPushRelayURL, "capability")
	}

	if result := send("attempt-1"); !result.OK {
		t.Fatalf("fallback result = %+v", result)
	}
	if want := []sent{{"attempt-1", true}, {"attempt-1", false}}; !slices.Equal(requests, want) {
		t.Fatalf("first send requests = %+v, want %+v", requests, want)
	}
	if result := send("attempt-2"); !result.OK || !slices.Equal(requests, []sent{{"attempt-2", false}}) {
		t.Fatalf("remembered relay: result = %+v, requests = %+v", result, requests)
	}
	now = now.Add(relayTargetRecheck)
	if result := send("attempt-3"); !result.OK || len(requests) != 2 || !requests[0].targeted {
		t.Fatalf("after recheck: result = %+v, requests = %+v", result, requests)
	}

	now = now.Add(relayTargetRecheck)
	rejectField = "collapse_id"
	result := send("attempt-4")
	if result.OK || result.HTTPStatus != http.StatusBadRequest || result.UpstreamReason != "unexpected_field" || len(requests) != 1 {
		t.Fatalf("other unexpected field: result = %+v, requests = %+v", result, requests)
	}
}

func TestPushSenderRetriesAnAttemptWithTheTargetChoiceTheRelayRecorded(t *testing.T) {
	type sent struct {
		key      string
		targeted bool
	}
	var requests []sent
	recorded := map[string]bool{}
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	sender := newPushSender(nil, nil, nil, NewSettings(mapSettingStore{}))
	sender.now = func() time.Time { return now }
	sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		key := req.Header.Get("Idempotency-Key")
		_, targeted := body["server_id"]
		requests = append(requests, sent{key: key, targeted: targeted})
		if first, ok := recorded[key]; ok && first != targeted {
			return relayResponse(http.StatusUnprocessableEntity, `{"error":{"code":"idempotency_key_reuse","message":"Idempotency-Key was reused with a different payload"}}`), nil
		}
		recorded[key] = targeted
		return relayResponse(http.StatusOK, `{"request_id":"relay-request","status":"accepted"}`), nil
	})}
	device := &PushDevice{Platform: PushPlatformApple, ProfileID: "profile-1", ServerDeviceID: "server-device-1"}
	target := pushRelayTarget{ServerID: "server-1", ProfileID: "profile-1"}
	send := func(attemptID string, target pushRelayTarget) pushSendResult {
		requests = nil
		return sender.sendWithCapability(context.Background(), PushDeliveryAttempt{ID: attemptID}, device, strings.Repeat("a", 64), target, DefaultPushRelayURL, "capability")
	}

	// First sent without the fields, by an earlier server version or while the
	// relay was remembered as lacking them; retried with them.
	recorded["attempt-1"] = false
	if result := send("attempt-1", target); !result.OK || !slices.Equal(requests, []sent{{"attempt-1", true}, {"attempt-1", false}}) {
		t.Fatalf("recorded without target: result = %+v, requests = %+v", result, requests)
	}

	// First sent with them; retried while this process remembers the relay as
	// lacking them.
	recorded["attempt-2"] = true
	sender.markRelayLacksTarget(DefaultPushRelayURL)
	if result := send("attempt-2", target); !result.OK || !slices.Equal(requests, []sent{{"attempt-2", false}, {"attempt-2", true}}) {
		t.Fatalf("recorded with target: result = %+v, requests = %+v", result, requests)
	}

	// Without a target of its own the server has no other choice to offer.
	// send keeps such an attempt retryable when only the identity lookup failed.
	recorded["attempt-3"] = true
	result := send("attempt-3", pushRelayTarget{})
	if result.OK || result.HTTPStatus != http.StatusUnprocessableEntity || result.UpstreamReason != relayCodeIdempotencyKeyReuse || len(requests) != 1 {
		t.Fatalf("no target: result = %+v, requests = %+v", result, requests)
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
			first := sender.sendWithCapability(context.Background(), attempt, device, strings.Repeat("a", 64), pushRelayTarget{}, DefaultPushRelayURL, "capability")
			second := sender.sendWithCapability(context.Background(), attempt, device, strings.Repeat("a", 64), pushRelayTarget{}, DefaultPushRelayURL, "capability")
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

func TestPushSenderRetriesLaterWhenAFailedIdentityLookupMismatchesTheRecordedAttempt(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"idempotency_key_reuse","message":"Idempotency-Key was reused with a different payload"}}`))
	}))
	defer server.Close()
	sender := newRelayTestSender(server)
	device := &PushDevice{Platform: PushPlatformAndroid, ProfileID: "profile-1", ServerDeviceID: "server-device-1"}
	send := func() pushSendResult {
		return sender.send(context.Background(), PushDeliveryAttempt{ID: "attempt-1"}, device, strings.Repeat("a", 64))
	}

	sender.serverID = func(context.Context) (string, error) { return "", errors.New("settings store down") }
	if result := send(); result.HTTPStatus != http.StatusServiceUnavailable || result.UpstreamReason != "server_identity_unavailable" || !retryableHTTPStatus(result.HTTPStatus) {
		t.Fatalf("identity lookup failed: result = %+v, want a retryable server_identity_unavailable", result)
	}

	sender.serverID = nil
	if result := send(); result.HTTPStatus != http.StatusUnprocessableEntity || result.UpstreamReason != relayCodeIdempotencyKeyReuse {
		t.Fatalf("no identity configured: result = %+v, want the relay's 422", result)
	}
}

// A relay Retry-After lengthens the wait before the next attempt but never
// shortens the schedule: a 429 asking for a second would otherwise use up
// every attempt within a few sweeps. The hint stays inside the relay's
// idempotency window.
func TestPushRetryDelayTreatsRetryAfterAsAMinimum(t *testing.T) {
	for _, tc := range []struct {
		retryAfter time.Duration
		want       time.Duration
	}{
		{0, 30 * time.Second},
		{time.Second, 30 * time.Second},
		{10 * time.Minute, 10 * time.Minute},
		{48 * time.Hour, pushRelayMaxRetryAfter},
	} {
		if got, more := pushRetryDelayWithHint(1, tc.retryAfter); got != tc.want || !more {
			t.Errorf("delay after attempt 1 with Retry-After %s = %s (more %t), want %s", tc.retryAfter, got, more, tc.want)
		}
	}
}
