package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/telemetry"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// webPushRetrySchedule is deliberately shorter than the webhook schedule:
// vendor push services queue undeliverable messages themselves (the TTL
// covers offline devices), so server-side retries only need to ride out
// transient push-service errors.
var webPushRetrySchedule = []time.Duration{
	0,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
}

const (
	webPushMaxAttempts = 5
	webPushTTLSeconds  = 12 * 60 * 60 // push-service queue TTL for offline devices
)

func webPushRetryDelay(completedAttempt int) (time.Duration, bool) {
	if completedAttempt < 1 || completedAttempt >= webPushMaxAttempts {
		return 0, false
	}
	return webPushRetrySchedule[completedAttempt] - webPushRetrySchedule[completedAttempt-1], true
}

// webPushPayload is the JSON the service worker receives. It is encrypted
// end-to-end (RFC 8291): only the subscribed browser can read it, never the
// vendor push service, so full display content is safe to include.
type webPushPayload struct {
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	URL        string `json:"url"`
	Icon       string `json:"icon,omitempty"`
	Tag        string `json:"tag,omitempty"`
	DeliveryID string `json:"delivery_id"`
}

// buildWebPushPayload renders a delivery for the service worker.
func buildWebPushPayload(row DeliveryRow, posterURL string) ([]byte, error) {
	display := BuildNotificationDisplay(row)
	payload := webPushPayload{
		Title:      display.Title,
		Body:       display.Body,
		URL:        display.URL,
		Tag:        row.ID,
		DeliveryID: row.ID,
		Icon:       posterURL,
	}
	return json.Marshal(payload)
}

// webPushSender delivers claimed web push attempts. Shared by the
// post-commit dispatcher and the retry worker.
type webPushSender struct {
	subscriptions *WebPushRepository
	deliveries    *DeliveryRepository
	service       *WebPushService
	settings      *Settings
	client        *http.Client
	payload       func(ctx context.Context, row DeliveryRow) DeliveryRowPayload
	logger        *slog.Logger
}

func newWebPushSender(
	subscriptions *WebPushRepository,
	deliveries *DeliveryRepository,
	service *WebPushService,
	settings *Settings,
) *webPushSender {
	return &webPushSender{
		subscriptions: subscriptions,
		deliveries:    deliveries,
		service:       service,
		settings:      settings,
		// Subscription endpoints are client-supplied URLs the server POSTs
		// to: the SSRF-guarded client applies (vendor push services are
		// public hosts, so legitimate endpoints always pass).
		client: newWebhookHTTPClient(nil),
		logger: slog.Default().With("component", "notifications.webpush"),
	}
}

// processAttempt sends one claimed attempt and records the outcome. Expired
// or revoked subscriptions (404/410 from the push service) are deleted —
// that is the protocol's unsubscribe signal, not a failure to retry.
func (s *webPushSender) processAttempt(ctx context.Context, attempt DeliveryAttempt) {
	sub, loginLive, err := s.subscriptions.getByIDUnscoped(ctx, attempt.TargetID)
	if err != nil || sub == nil {
		if err == nil {
			_, _ = s.subscriptions.FinalizeAttempt(ctx, attempt, WebhookOutcomeFailed,
				attempt.AttemptNumber+1, nil, "subscription deleted", nil)
		}
		return
	}
	if !sub.Enabled || !s.settings.WebPushEnabled(ctx) {
		_, _ = s.subscriptions.FinalizeAttempt(ctx, attempt, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "subscription disabled", nil)
		return
	}
	// The attempt may have been queued while the login was live; retries run for
	// half an hour after that.
	if !loginLive {
		_, _ = s.subscriptions.FinalizeAttempt(ctx, attempt, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "login session ended", nil)
		return
	}
	row, err := s.deliveries.GetRowByID(ctx, attempt.NotificationDeliveryID)
	if err != nil {
		// Transient lookup failure: let the claim lease expire and the retry
		// worker reclaim, instead of permanently failing the delivery.
		if ctx.Err() == nil {
			s.logger.WarnContext(ctx, "web push delivery lookup failed",
				"attempt_id", attempt.ID,
				"delivery_id", attempt.NotificationDeliveryID,
				"error", err)
		}
		return
	}
	if row == nil {
		_, _ = s.subscriptions.FinalizeAttempt(ctx, attempt, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "delivery row missing", nil)
		return
	}
	if row.UserID != sub.UserID || row.ProfileID != sub.ProfileID {
		// The endpoint was reassigned to a different profile, possibly one
		// with the same ID on another account, between enqueue and dispatch;
		// this delivery belongs to the previous owner.
		_, _ = s.subscriptions.FinalizeAttempt(ctx, attempt, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "subscription reassigned", nil)
		return
	}

	publicKey, privateKey, err := s.service.vapidKeys(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "VAPID keys unavailable", "error", err)
		_, _ = s.subscriptions.FinalizeAttempt(ctx, attempt, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "VAPID keys unavailable", nil)
		return
	}

	posterURL := ""
	if s.payload != nil {
		posterURL = s.payload(ctx, *row).PosterURL
	}
	message, err := buildWebPushPayload(*row, posterURL)
	if err != nil {
		_, _ = s.subscriptions.FinalizeAttempt(ctx, attempt, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "payload build failed", nil)
		return
	}

	if time.Since(attempt.claimedAt) > webhookClaimLease-webhookSendBudget {
		// Too little lease left to finish the send (a long claimed batch):
		// the retry sweep reclaims the attempt once the lease runs out.
		return
	}
	status, retryAfter, sendErr := s.send(ctx, sub, message, publicKey, privateKey)
	attemptNumber := attempt.AttemptNumber + 1

	if sendErr == nil && status >= 200 && status < 300 {
		// A reclaimed attempt's result is the new claim's to record.
		if applied, _ := s.subscriptions.FinalizeAttempt(ctx, attempt, WebhookOutcomeDelivered,
			attemptNumber, &status, "", nil); applied {
			_ = s.subscriptions.RecordSuccess(ctx, sub.ID)
		}
		return
	}

	if status == http.StatusNotFound || status == http.StatusGone {
		// The browser unsubscribed or the registration expired: remove the
		// subscription entirely (attempts cascade with it).
		s.logger.InfoContext(ctx, "web push subscription gone; removing",
			"subscription_id", sub.ID, "status", status)
		_ = s.subscriptions.deleteGone(ctx, sub.ID)
		return
	}

	var statusPtr *int
	if status > 0 {
		statusPtr = &status
	}
	message_ := "push service error"
	if sendErr != nil {
		message_ = classifyWebhookError(sendErr)
	} else if status > 0 {
		message_ = fmt.Sprintf("HTTP %d", status)
	}

	delay, more := webPushRetryDelay(attemptNumber)
	if more && (sendErr != nil || retryableHTTPStatus(status)) {
		// Retry-After can lengthen the wait but never shorten the schedule.
		nextRetry := time.Now().Add(max(delay, retryAfter))
		if applied, _ := s.subscriptions.FinalizeAttempt(ctx, attempt, WebhookOutcomeRetrying,
			attemptNumber, statusPtr, message_, &nextRetry); applied {
			_ = s.subscriptions.RecordFailure(ctx, sub.ID, statusPtr)
		}
		return
	}
	// Only the worker that still held the claim counts the failure.
	if applied, _ := s.subscriptions.FinalizeAttempt(ctx, attempt, WebhookOutcomeFailed,
		attemptNumber, statusPtr, message_, nil); applied {
		_ = s.subscriptions.RecordFailure(ctx, sub.ID, statusPtr)
	}
}

func (s *webPushSender) send(ctx context.Context, sub *WebPushSubscription, message []byte, publicKey, privateKey string) (status int, retryAfter time.Duration, err error) {
	ctx, finishObservation := telemetry.StartDependency(ctx, "notifications", "worker", "web_push")
	defer func() { finishObservation(deliveryObservationError(ctx, err == nil && status >= 200 && status < 300)) }()
	resp, err := webpush.SendNotificationWithContext(ctx, message, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient:      s.client,
		Subscriber:      "https://github.com/Silo-Server/silo-server",
		TTL:             webPushTTLSeconds,
		Urgency:         webpush.UrgencyNormal,
		VAPIDPublicKey:  publicKey,
		VAPIDPrivateKey: privateKey,
	})
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	return resp.StatusCode, retryAfter, nil
}

// WebPushDispatcher implements the channel Dispatcher interface on top of the
// shared channelDispatcher core, with the retry/recovery sweep integrated.
type WebPushDispatcher struct {
	core channelDispatcher[DeliveryAttempt]
}

func newWebPushDispatcher(sender *webPushSender) *WebPushDispatcher {
	return &WebPushDispatcher{core: channelDispatcher[DeliveryAttempt]{
		channel:      "web push",
		queue:        make(chan string, webhookDispatchQueue),
		logger:       slog.Default().With("component", "notifications.webpush.dispatch"),
		claimPending: sender.subscriptions.ClaimPendingForDelivery,
		process:      sender.processAttempt,
		enabled:      sender.settings.WebPushEnabled,
		claimDue:     sender.subscriptions.ClaimDue,
		claimLimit:   webhookRetryClaimLimit,
	}}
}

// Dispatch queues the delivery's web push attempts for immediate send.
func (d *WebPushDispatcher) Dispatch(_ context.Context, delivery DeliveryRow) error {
	if d == nil {
		return nil
	}
	d.core.dispatch(delivery.ID)
	return nil
}

// Run consumes the dispatch queue and the retry/recovery sweep until ctx is
// canceled.
func (d *WebPushDispatcher) Run(ctx context.Context) {
	d.core.run(ctx)
}
