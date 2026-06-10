package push

import (
	"context"
	"strconv"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
)

// APNsTransport implements Transport for Apple Push Notification service using
// token-based (p8) authentication. A new token client is built per Send call,
// which is acceptable for v1 notification volumes.
//
// TODO(v2): add Development vs Production toggle to APNsConfig; v1 always uses Production.
type APNsTransport struct {
	cfg func(ctx context.Context) APNsConfig
}

// NewAPNsTransport creates an APNsTransport. cfg is called on each Send so
// credentials can be rotated without restarting.
func NewAPNsTransport(cfg func(ctx context.Context) APNsConfig) *APNsTransport {
	return &APNsTransport{cfg: cfg}
}

func (t *APNsTransport) Name() string     { return TransportAPNs }
func (t *APNsTransport) Configured() bool { return t.cfg(context.Background()).Configured() }

func (t *APNsTransport) Send(ctx context.Context, deviceToken string, p Payload) (SendResult, time.Duration, error) {
	cfg := t.cfg(ctx)
	if !cfg.Configured() {
		return ResultSoftFail, 0, nil
	}

	authKey, err := token.AuthKeyFromBytes([]byte(cfg.P8Key))
	if err != nil {
		return ResultSoftFail, 0, err
	}

	client := apns2.NewTokenClient(&token.Token{
		AuthKey: authKey,
		KeyID:   cfg.KeyID,
		TeamID:  cfg.TeamID,
	}).Production()

	notif := &apns2.Notification{
		DeviceToken: deviceToken,
		Topic:       cfg.BundleID,
		CollapseID:  strconv.FormatInt(p.NotificationID, 10),
		Payload: payload.NewPayload().
			AlertTitle(p.Title).
			AlertBody(p.Body).
			Custom("link", p.Link).
			Custom("category", p.Category).
			Custom("notification_id", p.NotificationID),
	}

	resp, err := client.PushWithContext(ctx, notif)
	if err != nil {
		return ResultSoftFail, 0, err // network/timeout
	}

	res, retryAfter := apnsResult(resp.Reason, resp.StatusCode)
	return res, retryAfter, nil
}

// apnsResult maps an APNs response reason string and HTTP status code to a
// SendResult and optional retry-after duration. This is a pure function so it
// can be unit-tested without network access.
func apnsResult(reason string, status int) (SendResult, time.Duration) {
	switch reason {
	case "":
		if status == 200 {
			return ResultSent, 0
		}
		return ResultSoftFail, 0
	case apns2.ReasonBadDeviceToken, apns2.ReasonUnregistered, apns2.ReasonDeviceTokenNotForTopic:
		return ResultDead, 0
	case apns2.ReasonTooManyRequests:
		return ResultSoftFail, 30 * time.Second
	default:
		return ResultSoftFail, 0
	}
}
