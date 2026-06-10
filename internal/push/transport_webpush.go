package push

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// WebPushTransport implements Transport for the Web Push protocol (RFC 8291).
type WebPushTransport struct {
	cfg    func(ctx context.Context) WebPushConfig
	client *http.Client
}

// NewWebPushTransport creates a WebPushTransport. cfg is called on each Send
// so credentials can be rotated without restarting.
func NewWebPushTransport(cfg func(ctx context.Context) WebPushConfig) *WebPushTransport {
	return &WebPushTransport{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

func (t *WebPushTransport) Name() string { return TransportWebPush }

func (t *WebPushTransport) Configured() bool {
	return t.cfg(context.Background()).Configured()
}

func (t *WebPushTransport) Send(ctx context.Context, token string, payload Payload) (SendResult, time.Duration, error) {
	cfg := t.cfg(ctx)
	if !cfg.Configured() {
		return ResultSoftFail, 0, nil
	}

	var sub webpush.Subscription
	if err := json.Unmarshal([]byte(token), &sub); err != nil {
		// Malformed subscription JSON is a permanent failure; prune the device.
		return ResultDead, 0, err
	}

	body, _ := json.Marshal(map[string]any{
		"id":       payload.NotificationID,
		"title":    payload.Title,
		"body":     payload.Body,
		"link":     payload.Link,
		"category": payload.Category,
	})

	resp, err := webpush.SendNotificationWithContext(ctx, body, &sub, &webpush.Options{
		Subscriber:      cfg.Subject,
		VAPIDPublicKey:  cfg.VAPIDPublic,
		VAPIDPrivateKey: cfg.VAPIDPrivate,
		TTL:             86400,
		Topic:           webpushTopic(payload.NotificationID), // dedupe: same notification replaces pending
		HTTPClient:      t.client,
	})
	if err != nil {
		return ResultSoftFail, 0, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return ResultSent, 0, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// Subscription is expired or invalid; prune the device.
		return ResultDead, 0, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return ResultSoftFail, parseRetryAfter(resp.Header.Get("Retry-After")), nil
	default:
		return ResultSoftFail, 0, nil
	}
}

// parseRetryAfter parses the Retry-After header value (seconds integer form).
// Returns 0 for empty or unparseable values.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// webpushTopic derives a short (<=32 char) Web Push Topic header value from
// the notification ID so retries replace, rather than stack, pending pushes.
// Uses base-36 encoding for compactness; the result is URL-safe ASCII.
func webpushTopic(notificationID int64) string {
	return "n" + strconv.FormatInt(notificationID, 36)
}
