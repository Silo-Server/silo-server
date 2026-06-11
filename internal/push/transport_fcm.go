package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/oauth2/google"
)

// fcmScope is the OAuth2 scope required to send FCM HTTP v1 messages.
const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// FCM HTTP v1 FcmError error codes used for delivery-result classification.
const (
	fcmErrUnregistered    = "UNREGISTERED"
	fcmErrInvalidArgument = "INVALID_ARGUMENT"
	fcmErrQuotaExceeded   = "QUOTA_EXCEEDED"
	fcmErrUnavailable     = "UNAVAILABLE"
)

// FCMTransport implements Transport for Firebase Cloud Messaging via the FCM
// HTTP v1 REST API. Credentials (a service-account JSON) are read on each Send
// so they can be rotated without restarting.
//
// TODO(v2): cache the parsed credentials / oauth2 TokenSource keyed on the JSON
// hash to avoid re-parsing per call at higher notification volumes (the
// TokenSource itself already caches and refreshes the access token).
type FCMTransport struct {
	cfg    func(ctx context.Context) FCMConfig
	client *http.Client
}

// NewFCMTransport creates an FCMTransport. cfg is called on each Send so
// credentials can be rotated without restarting.
func NewFCMTransport(cfg func(ctx context.Context) FCMConfig) *FCMTransport {
	return &FCMTransport{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

func (t *FCMTransport) Name() string     { return TransportFCM }
func (t *FCMTransport) Configured() bool { return t.cfg(context.Background()).Configured() }

func (t *FCMTransport) Send(ctx context.Context, deviceToken string, p Payload) (SendResult, time.Duration, error) {
	cfg := t.cfg(ctx)
	if !cfg.Configured() {
		return ResultSoftFail, 0, nil
	}

	creds, err := google.CredentialsFromJSONWithType(ctx, []byte(cfg.ServiceAccountJSON), google.ServiceAccount, fcmScope)
	if err != nil {
		return ResultSoftFail, 0, err
	}

	// The project_id is a standard field of the service-account JSON and is
	// required to build the v1 endpoint.
	var sa struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal([]byte(cfg.ServiceAccountJSON), &sa); err != nil {
		return ResultSoftFail, 0, err
	}
	if sa.ProjectID == "" {
		return ResultSoftFail, 0, fmt.Errorf("fcm: service-account JSON missing project_id")
	}

	tok, err := creds.TokenSource.Token()
	if err != nil {
		return ResultSoftFail, 0, err
	}

	collapse := strconv.FormatInt(p.NotificationID, 10)
	body, err := json.Marshal(fcmMessage{
		Message: fcmInnerMessage{
			Token:        deviceToken,
			Notification: fcmNotification{Title: p.Title, Body: p.Body},
			Data: map[string]string{
				"notification_id": collapse,
				"link":            p.Link,
				"category":        p.Category,
			},
			Android: fcmAndroidConfig{CollapseKey: collapse},
		},
	})
	if err != nil {
		return ResultSoftFail, 0, err
	}

	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", sa.ProjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ResultSoftFail, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return ResultSoftFail, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		res, retryAfter := fcmResult(resp.StatusCode, "")
		return res, retryAfter, nil
	}

	// Non-2xx: parse the v1 error body for the FCM error code. Any read/parse
	// failure leaves errorCode empty, which classification handles by HTTP status.
	respBody, _ := io.ReadAll(resp.Body)
	errorCode := parseFCMErrorCode(respBody)
	res, retryAfter := fcmResult(resp.StatusCode, errorCode)
	return res, retryAfter, fmt.Errorf("fcm: http %d code=%q body=%s", resp.StatusCode, errorCode, respBody)
}

// FCM HTTP v1 request body types.
type fcmMessage struct {
	Message fcmInnerMessage `json:"message"`
}
type fcmInnerMessage struct {
	Token        string            `json:"token"`
	Notification fcmNotification   `json:"notification"`
	Data         map[string]string `json:"data"`
	Android      fcmAndroidConfig  `json:"android"`
}
type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}
type fcmAndroidConfig struct {
	CollapseKey string `json:"collapse_key"`
}

// parseFCMErrorCode extracts the FcmError errorCode (e.g. "UNREGISTERED") from a
// v1 error response body. Returns "" if absent or unparseable.
//
// Shape: {"error":{"status":"...","details":[
//
//	{"@type":".../google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}
func parseFCMErrorCode(body []byte) string {
	var parsed struct {
		Error struct {
			Details []struct {
				Type      string `json:"@type"`
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	for _, d := range parsed.Error.Details {
		if d.ErrorCode != "" {
			return d.ErrorCode
		}
	}
	return ""
}

// fcmResult maps an FCM HTTP v1 response (status code + parsed FcmError
// errorCode) to a SendResult and optional retry-after duration. This is a pure
// function so it can be unit-tested without network access.
//
// Dead-token classification uses UNREGISTERED (stale/deleted token) and
// INVALID_ARGUMENT (malformed token), or HTTP 404. Rate-limit/service errors
// (QUOTA_EXCEEDED / UNAVAILABLE, or HTTP 429/503) are soft failures with a 30 s
// back-off. All other non-2xx responses are retryable with no delay.
func fcmResult(status int, errorCode string) (SendResult, time.Duration) {
	if status >= 200 && status < 300 {
		return ResultSent, 0
	}
	switch errorCode {
	case fcmErrUnregistered, fcmErrInvalidArgument:
		return ResultDead, 0
	case fcmErrQuotaExceeded, fcmErrUnavailable:
		return ResultSoftFail, 30 * time.Second
	}
	switch status {
	case http.StatusNotFound:
		return ResultDead, 0
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return ResultSoftFail, 30 * time.Second
	}
	return ResultSoftFail, 0
}
