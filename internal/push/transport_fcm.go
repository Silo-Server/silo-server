package push

import (
	"context"
	"strconv"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

// FCMTransport implements Transport for Firebase Cloud Messaging using a
// service-account JSON credential. A new Firebase App and messaging client is
// built per Send call so credentials can be rotated without restarting.
//
// TODO(v2): cache the *firebase.App keyed on the JSON hash to avoid per-call
// initialisation overhead at higher notification volumes.
type FCMTransport struct {
	cfg func(ctx context.Context) FCMConfig
}

// NewFCMTransport creates an FCMTransport. cfg is called on each Send so
// credentials can be rotated without restarting.
func NewFCMTransport(cfg func(ctx context.Context) FCMConfig) *FCMTransport {
	return &FCMTransport{cfg: cfg}
}

func (t *FCMTransport) Name() string     { return TransportFCM }
func (t *FCMTransport) Configured() bool { return t.cfg(context.Background()).Configured() }

func (t *FCMTransport) Send(ctx context.Context, deviceToken string, p Payload) (SendResult, time.Duration, error) {
	cfg := t.cfg(ctx)
	if !cfg.Configured() {
		return ResultSoftFail, 0, nil
	}

	creds, err := google.CredentialsFromJSONWithType(ctx, []byte(cfg.ServiceAccountJSON), google.ServiceAccount, "https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		return ResultSoftFail, 0, err
	}
	app, err := firebase.NewApp(ctx, nil, option.WithCredentials(creds))
	if err != nil {
		return ResultSoftFail, 0, err
	}

	client, err := app.Messaging(ctx)
	if err != nil {
		return ResultSoftFail, 0, err
	}

	collapse := strconv.FormatInt(p.NotificationID, 10)
	_, err = client.Send(ctx, &messaging.Message{
		Token:        deviceToken,
		Notification: &messaging.Notification{Title: p.Title, Body: p.Body},
		Data: map[string]string{
			"notification_id": collapse,
			"link":            p.Link,
			"category":        p.Category,
		},
		Android: &messaging.AndroidConfig{CollapseKey: collapse},
	})

	res, retryAfter := fcmResult(err)
	return res, retryAfter, err
}

// fcmResult maps a Firebase Messaging error to a SendResult and optional
// retry-after duration. This is a pure function so it can be unit-tested
// without network access.
//
// Dead-token classification uses IsUnregistered (stale/deleted token) and
// IsInvalidArgument (malformed token). Rate-limit/service errors are soft
// failures with a 30 s back-off. All other errors are retryable with no delay.
func fcmResult(err error) (SendResult, time.Duration) {
	if err == nil {
		return ResultSent, 0
	}
	if messaging.IsUnregistered(err) || messaging.IsInvalidArgument(err) {
		return ResultDead, 0
	}
	if messaging.IsQuotaExceeded(err) || messaging.IsUnavailable(err) {
		return ResultSoftFail, 30 * time.Second
	}
	return ResultSoftFail, 0
}
