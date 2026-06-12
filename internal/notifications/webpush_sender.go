package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

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
	payload := webPushPayload{
		Title:      "Silo",
		URL:        "/notifications",
		Tag:        row.ID,
		DeliveryID: row.ID,
	}
	switch row.Type {
	case DeliveryTypeEpisodeAvailable:
		if row.SeriesTitle != "" {
			payload.Title = "New episode of " + row.SeriesTitle
		} else {
			payload.Title = "New episode available"
		}
		var code string
		if row.SeasonNumber != nil && row.EpisodeNumber != nil {
			code = fmt.Sprintf("S%dE%d", *row.SeasonNumber, *row.EpisodeNumber)
		}
		switch {
		case code != "" && row.EpisodeTitle != "":
			payload.Body = code + " — " + row.EpisodeTitle
		case code != "":
			payload.Body = code
		default:
			payload.Body = row.EpisodeTitle
		}
		if row.EpisodeID != nil {
			payload.URL = "/item/" + *row.EpisodeID
		}
		payload.Icon = posterURL
	case DeliveryTypeRequestFulfilled:
		if row.SeriesTitle != "" {
			payload.Title = row.SeriesTitle + " is now available"
		} else {
			payload.Title = "Your request is now available"
		}
		payload.Body = "Your media request has arrived in the library."
		if row.SeriesID != nil {
			payload.URL = "/item/" + *row.SeriesID
		}
		payload.Icon = posterURL
	case DeliveryTypeRequestApproved:
		flags := parseRequestFlags(row.ReasonFlags)
		payload.Title = "Your request was approved"
		if flags.Title != "" {
			payload.Title = flags.Title + " was approved"
		}
		payload.Body = "Your media request was approved."
	case DeliveryTypeRequestDeclined:
		flags := parseRequestFlags(row.ReasonFlags)
		payload.Title = "Your request was declined"
		if flags.Title != "" {
			payload.Title = flags.Title + " was declined"
		}
		payload.Body = "Your media request was declined."
		if flags.Reason != "" {
			payload.Body = "Reason: " + flags.Reason
		}
	case DeliveryTypeWebhookAutoDisabled:
		payload.Title = "A webhook stopped working"
		payload.Body = "Open notification settings to fix it."
		payload.URL = "/settings/notifications"
	default:
		// Unknown types render generically; the inbox has the details.
		payload.Title = genericNotificationTitle
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
	sub, err := s.subscriptions.getByIDUnscoped(ctx, attempt.TargetID)
	if err != nil || sub == nil {
		if err == nil {
			_ = s.subscriptions.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
				attempt.AttemptNumber+1, nil, "subscription deleted", nil)
		}
		return
	}
	if !sub.Enabled || !s.settings.WebPushEnabled(ctx) {
		_ = s.subscriptions.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "subscription disabled", nil)
		return
	}
	row, err := s.deliveries.GetRowByID(ctx, attempt.NotificationDeliveryID)
	if err != nil {
		// Transient lookup failure: let the claim lease expire and the retry
		// worker reclaim, instead of permanently failing the delivery.
		if ctx.Err() == nil {
			s.logger.Warn("web push delivery lookup failed",
				"attempt_id", attempt.ID,
				"delivery_id", attempt.NotificationDeliveryID,
				"error", err)
		}
		return
	}
	if row == nil {
		_ = s.subscriptions.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "delivery row missing", nil)
		return
	}
	if row.ProfileID != sub.ProfileID {
		// The endpoint was reassigned to a different profile between enqueue
		// and dispatch; this delivery belongs to the previous owner.
		_ = s.subscriptions.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "subscription reassigned", nil)
		return
	}

	publicKey, privateKey, err := s.service.vapidKeys(ctx)
	if err != nil {
		s.logger.Error("VAPID keys unavailable", "error", err)
		_ = s.subscriptions.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "VAPID keys unavailable", nil)
		return
	}

	posterURL := ""
	if s.payload != nil {
		posterURL = s.payload(ctx, *row).PosterURL
	}
	message, err := buildWebPushPayload(*row, posterURL)
	if err != nil {
		_ = s.subscriptions.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "payload build failed", nil)
		return
	}

	status, retryAfter, sendErr := s.send(ctx, sub, message, publicKey, privateKey)
	attemptNumber := attempt.AttemptNumber + 1

	if sendErr == nil && status >= 200 && status < 300 {
		_ = s.subscriptions.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeDelivered,
			attemptNumber, &status, "", nil)
		_ = s.subscriptions.RecordSuccess(ctx, sub.ID)
		return
	}

	if status == http.StatusNotFound || status == http.StatusGone {
		// The browser unsubscribed or the registration expired: remove the
		// subscription entirely (attempts cascade with it).
		s.logger.Info("web push subscription gone; removing",
			"subscription_id", sub.ID, "status", status)
		_ = s.subscriptions.deleteGone(ctx, sub.ID)
		return
	}

	var statusPtr *int
	if status > 0 {
		statusPtr = &status
	}
	_ = s.subscriptions.RecordFailure(ctx, sub.ID, statusPtr)

	message_ := "push service error"
	if sendErr != nil {
		message_ = classifyWebhookError(sendErr)
	} else if status > 0 {
		message_ = fmt.Sprintf("HTTP %d", status)
	}

	delay, more := webPushRetryDelay(attemptNumber)
	if retryAfter > 0 {
		delay = retryAfter
	}
	if more && (sendErr != nil || retryableHTTPStatus(status)) {
		nextRetry := time.Now().Add(delay)
		_ = s.subscriptions.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeRetrying,
			attemptNumber, statusPtr, message_, &nextRetry)
		return
	}
	_ = s.subscriptions.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
		attemptNumber, statusPtr, message_, nil)
}

func (s *webPushSender) send(ctx context.Context, sub *WebPushSubscription, message []byte, publicKey, privateKey string) (status int, retryAfter time.Duration, err error) {
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

// WebPushDispatcher implements the channel Dispatcher interface: it hands
// delivery IDs to a bounded worker pool that claims and sends the pending
// outbox attempts. A full queue defers to the retry worker's recovery sweep.
type WebPushDispatcher struct {
	sender *webPushSender
	queue  chan string
	logger *slog.Logger
}

func newWebPushDispatcher(sender *webPushSender) *WebPushDispatcher {
	return &WebPushDispatcher{
		sender: sender,
		queue:  make(chan string, webhookDispatchQueue),
		logger: slog.Default().With("component", "notifications.webpush.dispatch"),
	}
}

// Dispatch queues the delivery's web push attempts for immediate send.
func (d *WebPushDispatcher) Dispatch(_ context.Context, delivery DeliveryRow) error {
	if d == nil {
		return nil
	}
	select {
	case d.queue <- delivery.ID:
	default:
		d.logger.Warn("web push dispatch queue full; deferring to retry worker",
			"delivery_id", delivery.ID)
	}
	return nil
}

// Run consumes the dispatch queue and the retry/recovery sweep until ctx is
// canceled.
func (d *WebPushDispatcher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range webhookDispatchWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case deliveryID := <-d.queue:
					d.processDelivery(ctx, deliveryID)
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(webhookRetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if !d.sender.settings.WebPushEnabled(ctx) {
				continue
			}
			for {
				attempts, err := d.sender.subscriptions.ClaimDue(ctx, webhookRetryClaimLimit)
				if err != nil {
					if ctx.Err() == nil {
						d.logger.Warn("web push retry claim failed", "error", err)
					}
					break
				}
				if len(attempts) == 0 {
					break
				}
				for _, attempt := range attempts {
					if ctx.Err() != nil {
						return
					}
					d.sender.processAttempt(ctx, attempt)
				}
			}
		}
	}()
	wg.Wait()
}

func (d *WebPushDispatcher) processDelivery(ctx context.Context, deliveryID string) {
	attempts, err := d.sender.subscriptions.ClaimPendingForDelivery(ctx, deliveryID)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.Warn("web push attempt claim failed", "delivery_id", deliveryID, "error", err)
		}
		return
	}
	for _, attempt := range attempts {
		if ctx.Err() != nil {
			return
		}
		d.sender.processAttempt(ctx, attempt)
	}
}
