package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/oklog/ulid/v2"
)

// webhookRetrySchedule holds the cumulative delay since the first attempt
// (docs/superpowers/plans/notifications/04, "Retry schedule"). Index N is the
// delay before attempt N+1; after the last attempt fails, the webhook is
// auto-disabled.
var webhookRetrySchedule = []time.Duration{
	0,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
	12 * time.Hour,
	18 * time.Hour,
	24 * time.Hour,
}

const webhookMaxAttempts = 10

// webhookRetryDelay returns how long to wait after a retryable failure of
// attempt N (1-based) before the next attempt, or ok=false when the schedule
// is exhausted.
func webhookRetryDelay(completedAttempt int) (time.Duration, bool) {
	if completedAttempt < 1 || completedAttempt >= webhookMaxAttempts {
		return 0, false
	}
	return webhookRetrySchedule[completedAttempt] - webhookRetrySchedule[completedAttempt-1], true
}

// retryableHTTPStatus reports whether an HTTP failure status is worth
// retrying. Non-retryable 4xx responses are deterministic destination-side
// rejections; 408/425/429 are the transient exceptions.
func retryableHTTPStatus(status int) bool {
	if status == 0 || status >= 500 {
		return true
	}
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	default:
		return status < 400
	}
}

const autoDisableConsecutive4xx = 3

// webhookSender owns the actual delivery of claimed attempts. It is shared
// by the post-commit dispatcher and the retry worker so both paths apply the
// same retry, auto-disable, and notification rules.
type webhookSender struct {
	webhooks   *WebhookRepository
	deliveries *DeliveryRepository
	cipher     *secret.Cipher
	settings   *Settings
	client     *http.Client
	// operational posts the auto-disable notice through the system's shared
	// durable dispatch path. Wired by NewSystem after construction.
	operational func(ctx context.Context, delivery Delivery, opts OperationalDispatch) (*InsertedDelivery, error)
	logger      *slog.Logger
}

func newWebhookSender(
	webhooks *WebhookRepository,
	deliveries *DeliveryRepository,
	cipher *secret.Cipher,
	settings *Settings,
) *webhookSender {
	sender := &webhookSender{
		webhooks:   webhooks,
		deliveries: deliveries,
		cipher:     cipher,
		settings:   settings,
		logger:     slog.Default().With("component", "notifications.webhooks"),
	}
	sender.client = newWebhookHTTPClient(func() bool {
		return settings.WebhooksAllowPrivateDestinations(context.Background())
	})
	return sender
}

func webhookURLAAD(id string) string    { return "notification_webhook:" + id + ":url" }
func webhookSecretAAD(id string) string { return "notification_webhook:" + id + ":signing_secret" }

func (s *webhookSender) decryptURL(hook *Webhook) (string, error) {
	return s.cipher.Decrypt(hook.URLCiphertext, webhookURLAAD(hook.ID))
}

func (s *webhookSender) decryptSecret(hook *Webhook) (string, error) {
	if hook.SigningSecretCiphertext == nil {
		return "", fmt.Errorf("webhook has no signing secret")
	}
	return s.cipher.Decrypt(*hook.SigningSecretCiphertext, webhookSecretAAD(hook.ID))
}

// buildPayload renders the type-specific request body and headers.
func (s *webhookSender) buildPayload(hook *Webhook, row DeliveryRow, test bool) (body []byte, headers map[string]string, err error) {
	switch hook.Type {
	case WebhookTypeDiscord:
		body, err = BuildDiscordWebhookPayload(row, test)
		return body, nil, err
	case WebhookTypeGeneric:
		body, err = BuildGenericWebhookPayload(row, hook.ID, test)
		if err != nil {
			return nil, nil, err
		}
		signingSecret, err := s.decryptSecret(hook)
		if err != nil {
			return nil, nil, err
		}
		return body, genericWebhookHeaders(hook.ID, row.ID, signingSecret, time.Now(), body), nil
	default:
		return nil, nil, fmt.Errorf("unknown webhook type %q", hook.Type)
	}
}

// send POSTs one payload to the webhook's destination.
func (s *webhookSender) send(ctx context.Context, hook *Webhook, row DeliveryRow, test bool) webhookSendResult {
	url, err := s.decryptURL(hook)
	if err != nil {
		return webhookSendResult{Message: "webhook URL could not be decrypted"}
	}
	body, headers, err := s.buildPayload(hook, row, test)
	if err != nil {
		return webhookSendResult{Message: "payload build failed"}
	}
	return sendWebhook(ctx, s.client, url, body, headers)
}

// processAttempt delivers one claimed attempt and records the outcome:
// delivered, retrying with backoff, failed (non-retryable 4xx or exhausted
// schedule), and the auto-disable transitions.
func (s *webhookSender) processAttempt(ctx context.Context, attempt DeliveryAttempt) {
	hook, err := s.webhooks.getByIDUnscoped(ctx, attempt.TargetID)
	if err != nil || hook == nil {
		// Webhook deleted between enqueue and dispatch: the cascade removes
		// attempts; nothing to do beyond closing this one out if it survived.
		if err == nil {
			_ = s.webhooks.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
				attempt.AttemptNumber+1, nil, "webhook deleted", nil)
		}
		return
	}
	if !hook.Enabled || !s.settings.WebhooksEnabled(ctx) {
		_ = s.webhooks.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "webhook disabled", nil)
		return
	}

	row, err := s.deliveries.GetRowByID(ctx, attempt.NotificationDeliveryID)
	if err != nil {
		// Transient lookup failure: leave the claimed attempt alone so the
		// lease expires and the retry worker reclaims it, instead of
		// permanently failing the delivery over a database blip.
		if ctx.Err() == nil {
			s.logger.Warn("webhook delivery lookup failed",
				"attempt_id", attempt.ID,
				"delivery_id", attempt.NotificationDeliveryID,
				"error", err)
		}
		return
	}
	if row == nil {
		_ = s.webhooks.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, "delivery row missing", nil)
		return
	}

	result := s.send(ctx, hook, *row, false)
	attemptNumber := attempt.AttemptNumber + 1

	if result.OK {
		_ = s.webhooks.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeDelivered,
			attemptNumber, &result.HTTPStatus, "", nil)
		if err := s.webhooks.RecordSuccess(ctx, hook.ID); err != nil {
			s.logger.Warn("webhook success bookkeeping failed", "webhook_id", hook.ID, "error", err)
		}
		return
	}

	var status *int
	if result.HTTPStatus > 0 {
		status = &result.HTTPStatus
	}
	if err := s.webhooks.RecordFailure(ctx, hook.ID, status, result.Message); err != nil {
		s.logger.Warn("webhook failure bookkeeping failed", "webhook_id", hook.ID, "error", err)
	}

	if result.HTTPStatus > 0 && !retryableHTTPStatus(result.HTTPStatus) {
		// Deterministic destination-side rejection: fail this delivery now.
		_ = s.webhooks.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeFailed,
			attemptNumber, status, result.Message, nil)
		s.maybeDisableAfter4xx(ctx, hook, result)
		return
	}

	delay, more := webhookRetryDelay(attemptNumber)
	if result.RetryAfter > 0 {
		delay = result.RetryAfter
	}
	if more {
		nextRetry := time.Now().Add(delay)
		_ = s.webhooks.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeRetrying,
			attemptNumber, status, result.Message, &nextRetry)
		return
	}

	// Retry schedule exhausted (~24h of consecutive failures).
	_ = s.webhooks.FinalizeAttempt(ctx, attempt.ID, WebhookOutcomeAutoDisabled,
		attemptNumber, status, result.Message, nil)
	s.disableWebhook(ctx, hook, result, "Deliveries failed for 24 hours")
}

// maybeDisableAfter4xx applies the 3-consecutive-non-retryable-4xx rule. A
// single 4xx is not proof the webhook is dead (CDN/WAF blips), but three
// consecutive deterministic rejections are.
func (s *webhookSender) maybeDisableAfter4xx(ctx context.Context, hook *Webhook, result webhookSendResult) {
	recent, err := s.webhooks.RecentFinalOutcomes(ctx, hook.ID, autoDisableConsecutive4xx)
	if err != nil {
		s.logger.Warn("webhook 4xx history lookup failed", "webhook_id", hook.ID, "error", err)
		return
	}
	if len(recent) < autoDisableConsecutive4xx {
		return
	}
	for _, attempt := range recent {
		if attempt.Outcome != WebhookOutcomeFailed ||
			attempt.HTTPStatus == nil || retryableHTTPStatus(*attempt.HTTPStatus) {
			return
		}
	}
	s.disableWebhook(ctx, hook, result,
		fmt.Sprintf("%d consecutive deliveries rejected by the destination", autoDisableConsecutive4xx))
}

// disableWebhook auto-disables the webhook and posts the in-app notice so the
// failure is not silent. The notice type is on the webhook deny list, so it
// can never loop back into another webhook attempt.
func (s *webhookSender) disableWebhook(ctx context.Context, hook *Webhook, result webhookSendResult, reason string) {
	fullReason := reason
	if result.Message != "" {
		fullReason = fmt.Sprintf("%s (last error: %s)", reason, result.Message)
	}
	if err := s.webhooks.Disable(ctx, hook.ID, fullReason); err != nil {
		s.logger.Error("webhook auto-disable failed", "webhook_id", hook.ID, "error", err)
		return
	}
	s.logger.Warn("webhook auto-disabled",
		"webhook_id", hook.ID, "url_host", hook.URLHost, "reason", fullReason)

	noticeFlags, err := json.Marshal(map[string]any{
		"webhook_id":          hook.ID,
		"webhook_name":        hook.Name,
		"last_failure_status": result.HTTPStatus,
	})
	if err != nil {
		return
	}
	notice := Delivery{
		ID:          ulid.Make().String(),
		UserID:      hook.UserID,
		ProfileID:   hook.ProfileID,
		Type:        DeliveryTypeWebhookAutoDisabled,
		ReasonFlags: noticeFlags,
	}
	if s.operational == nil {
		return
	}
	// Nil WebhookFilter: the notice must never re-dispatch as a webhook, or a
	// broken webhook would loop forever.
	if _, err := s.operational(ctx, notice, OperationalDispatch{}); err != nil {
		s.logger.Warn("webhook auto-disable notice dispatch failed", "webhook_id", hook.ID, "error", err)
	}
}

// profileRateLimiter is a per-profile sliding-window counter bounding webhook
// deliveries per minute. Over-limit notifications stay in the inbox; webhook
// attempts simply are not enqueued. Per-node state: fanout claims are
// node-exclusive (SKIP LOCKED), so one node owns a given event's enqueue.
type profileRateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
}

func newProfileRateLimiter() *profileRateLimiter {
	return &profileRateLimiter{windows: make(map[string][]time.Time)}
}

// Allow reports whether the profile is under the per-minute limit and counts
// the delivery when it is.
func (l *profileRateLimiter) Allow(profileID string, limit int) bool {
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()

	window := l.windows[profileID]
	kept := window[:0]
	for _, ts := range window {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= limit {
		l.windows[profileID] = kept
		return false
	}
	l.windows[profileID] = append(kept, now)
	return true
}
