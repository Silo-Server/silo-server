package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/telemetry"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

// webhookRetrySchedule holds the cumulative delay since the first attempt
// (docs/architecture/notifications.md, "Retry schedule and auto-disable").
// Index N is the delay before attempt N+1; after the last attempt fails, the
// webhook is auto-disabled.
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

// exhaustedDisableReason is the auto-disable text when a delivery used up its
// whole retry schedule.
const exhaustedDisableReason = "Deliveries failed for 24 hours"

// webhookSender owns the actual delivery of claimed attempts. It is shared
// by the post-commit dispatcher and the retry worker so both paths apply the
// same retry, auto-disable, and notification rules.
type webhookSender struct {
	webhooks   *WebhookRepository
	deliveries *DeliveryRepository
	cipher     *secret.Cipher
	settings   *Settings
	client     *http.Client
	// noticeWriter, called before the finish transaction begins, returns
	// the writer that inserts the auto-disable notice through that
	// transaction; dispatchNotice sends it to live clients after the commit.
	// Wired by NewSystem after construction.
	noticeWriter   func(ctx context.Context) func(ctx context.Context, tx pgx.Tx, notice Delivery) (*InsertedDelivery, error)
	dispatchNotice func(ctx context.Context, row InsertedDelivery)
	// posterURL picks the artwork URL Discord embeds may carry (admin poster
	// mode + provider-CDN/presign resolution). Wired by NewSystem after
	// construction; nil renders embeds without images.
	posterURL func(ctx context.Context, posterPath, posterSourcePath string) string
	logger    *slog.Logger
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
func (s *webhookSender) send(ctx context.Context, hook *Webhook, row DeliveryRow, test bool) (result webhookSendResult) {
	ctx, finishObservation := telemetry.StartDependency(ctx, "notifications", "worker", "webhook")
	defer func() { finishObservation(deliveryObservationError(ctx, result.OK)) }()
	url, err := s.decryptURL(hook)
	if err != nil {
		return webhookSendResult{Message: "webhook URL could not be decrypted"}
	}
	if row.PosterPath == "" && isRequestLifecycleType(row.Type) {
		flags := parseRequestFlags(row.ReasonFlags)
		row.PosterPath = flags.PosterPath
	}
	if hook.Type == WebhookTypeDiscord && s.posterURL != nil {
		// Artwork has its own share of webhookSendBudget, so a slow lookup
		// can't push the send past its claim's lease. Without it, the embed
		// goes out with no image.
		posterCtx, cancel := context.WithTimeout(ctx, webhookArtworkTimeout)
		row.PosterURL = s.posterURL(posterCtx, row.PosterPath, row.PosterSourcePath)
		cancel()
	}
	body, headers, err := s.buildPayload(hook, row, test)
	if err != nil {
		return webhookSendResult{Message: "payload build failed"}
	}
	return sendWebhook(ctx, s.client, url, body, headers)
}

// webhookSendBudget is how much of its lease a claimed attempt must have left
// to be sent: resolving artwork (webhookArtworkTimeout), then the POST under
// webhookRequestTimeout, plus slack for the work around them. An attempt with
// less is left for the retry sweep to reclaim, because a send that outlives
// its lease is sent again by whoever reclaims it.
const (
	webhookArtworkTimeout = 10 * time.Second
	webhookSendBudget     = webhookArtworkTimeout + webhookRequestTimeout + 5*time.Second
)

// processAttempt delivers one claimed attempt and records the outcome:
// delivered, retrying with backoff, failed (non-retryable 4xx or exhausted
// schedule), and the auto-disable transitions.
func (s *webhookSender) processAttempt(ctx context.Context, attempt DeliveryAttempt) {
	closeOut := func(message string) {
		_, _ = s.webhooks.FinalizeAttempt(ctx, s.webhooks.pool, attempt, WebhookOutcomeFailed,
			attempt.AttemptNumber+1, nil, message, nil)
	}
	hook, err := s.webhooks.getByIDUnscoped(ctx, attempt.TargetID)
	if err != nil || hook == nil {
		// Webhook deleted between enqueue and dispatch: the cascade removes
		// attempts; nothing to do beyond closing this one out if it survived.
		if err == nil {
			closeOut("webhook deleted")
		}
		return
	}
	if !hook.Enabled || !s.settings.WebhooksEnabled(ctx) {
		closeOut("webhook disabled")
		return
	}

	row, err := s.deliveries.GetRowByID(ctx, attempt.NotificationDeliveryID)
	if err != nil {
		// Transient lookup failure: leave the claimed attempt alone so the
		// lease expires and the retry worker reclaims it, instead of
		// permanently failing the delivery over a database blip.
		if ctx.Err() == nil {
			s.logger.WarnContext(ctx, "webhook delivery lookup failed",
				"attempt_id", attempt.ID,
				"delivery_id", attempt.NotificationDeliveryID,
				"error", err)
		}
		return
	}
	if row == nil {
		closeOut("delivery row missing")
		return
	}
	if row.UserID != hook.UserID || row.ProfileID != hook.ProfileID {
		// Queued for another account's profile with the same ID by a build
		// that matched targets on profile ID alone.
		closeOut("webhook belongs to another profile")
		return
	}
	if time.Since(attempt.claimedAt) > webhookClaimLease-webhookSendBudget {
		// Too little lease left to finish the send (a long claimed batch):
		// the retry sweep reclaims the attempt once the lease runs out.
		return
	}

	result := s.send(ctx, hook, *row, false)
	f := webhookFinish{attemptNumber: attempt.AttemptNumber + 1}
	switch delay, more := webhookRetryDelay(f.attemptNumber); {
	case result.OK:
		f.outcome = WebhookOutcomeDelivered
	case result.HTTPStatus > 0 && !retryableHTTPStatus(result.HTTPStatus):
		// Deterministic destination-side rejection: fail this delivery now.
		// A single 4xx is not proof the webhook is dead (CDN/WAF blips), but
		// three consecutive rejected deliveries are.
		f.outcome, f.rejected = WebhookOutcomeFailed, true
	case more:
		// Retry-After can lengthen the wait but never shorten the schedule:
		// a destination answering 429 with a few seconds would otherwise use
		// up every attempt in minutes.
		next := time.Now().Add(max(delay, result.RetryAfter))
		f.outcome, f.next = WebhookOutcomeRetrying, &next
	default:
		// Retry schedule exhausted (~24h of consecutive failures). The
		// attempt records auto_disabled only if it turns the webhook off.
		f.outcome, f.disableReason = WebhookOutcomeFailed, exhaustedDisableReason
	}
	if result.HTTPStatus > 0 {
		f.status = &result.HTTPStatus
	}
	if !result.OK {
		f.message = result.Message
	}

	notice, reason, err := s.commitFinish(ctx, attempt, hook, f)
	if err != nil {
		// Nothing was written: the lease expires and the retry worker
		// reclaims the attempt.
		if ctx.Err() == nil {
			s.logger.WarnContext(ctx, "webhook attempt finalize failed", "attempt_id", attempt.ID, "error", err)
		}
		return
	}
	if reason != "" {
		s.logger.WarnContext(ctx, "webhook auto-disabled",
			"webhook_id", hook.ID, "url_host", hook.URLHost, "reason", reason)
	}
	if notice != nil && s.dispatchNotice != nil {
		s.dispatchNotice(ctx, *notice)
	}
}

// webhookFinish is how one send of a claimed attempt ended.
type webhookFinish struct {
	outcome       string
	attemptNumber int
	status        *int
	message       string
	next          *time.Time
	rejected      bool   // a non-retryable 4xx
	disableReason string // set when the outcome alone disables the webhook
}

// commitFinish records a send's outcome, the webhook's streaks, any
// auto-disable, and its notice in one transaction. It writes nothing when the
// attempt no longer holds its claim. It returns the notice to dispatch and
// the disable reason when this call disabled the webhook.
func (s *webhookSender) commitFinish(ctx context.Context, attempt DeliveryAttempt, hook *Webhook, f webhookFinish) (*InsertedDelivery, string, error) {
	mayDisable := f.rejected || f.disableReason != ""
	// Everything the notice needs from outside the transaction is read
	// before it begins: a transaction waiting on a second connection from an
	// exhausted pool would never disable anything.
	var insertNotice func(context.Context, pgx.Tx, Delivery) (*InsertedDelivery, error)
	if mayDisable && s.noticeWriter != nil {
		insertNotice = s.noticeWriter(ctx)
	}
	tx, err := s.webhooks.pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// A disable writes a notice into the profile's inbox, so its locks come
	// first, in the order the other inbox writers take them (see
	// pluginDestinationSender.commitFinish).
	if mayDisable {
		if err := lockInboxProfiles(ctx, tx, []string{hook.ProfileID}); err != nil {
			return nil, "", err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR KEY SHARE`, hook.UserID); err != nil {
			return nil, "", err
		}
	}
	if exists, err := s.webhooks.lockForFinish(ctx, tx, hook.ID); err != nil || !exists {
		return nil, "", err // a deleted webhook took its attempts with it
	}

	reason := f.disableReason
	if f.outcome == WebhookOutcomeDelivered {
		err = s.webhooks.RecordSuccess(ctx, tx, hook.ID)
	} else {
		var rejections int
		rejections, err = s.webhooks.RecordFailure(ctx, tx, hook.ID, f.status, f.message, f.rejected)
		if f.rejected && rejections >= autoDisableConsecutive4xx {
			reason = fmt.Sprintf("%d consecutive deliveries rejected by the destination", autoDisableConsecutive4xx)
		}
	}
	if err != nil {
		return nil, "", err
	}
	disabled := false
	if reason != "" {
		if f.message != "" {
			reason = fmt.Sprintf("%s (last error: %s)", reason, f.message)
		}
		if disabled, err = s.webhooks.Disable(ctx, tx, hook.ID, reason); err != nil {
			return nil, "", err
		}
		if disabled && f.disableReason != "" {
			f.outcome = WebhookOutcomeAutoDisabled
		}
	}
	applied, err := s.webhooks.FinalizeAttempt(ctx, tx, attempt, f.outcome, f.attemptNumber, f.status, f.message, f.next)
	if err != nil || !applied {
		return nil, "", err
	}
	var notice *InsertedDelivery
	if disabled && insertNotice != nil {
		if notice, err = insertNotice(ctx, tx, webhookAutoDisableNotice(hook, f.status)); err != nil {
			return nil, "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	if !disabled {
		reason = ""
	}
	return notice, reason, nil
}

// webhookAutoDisableNotice is the in-app notice that tells the owner a
// webhook was turned off. Its type is on the webhook deny list, so it can
// never loop back into another webhook attempt.
func webhookAutoDisableNotice(hook *Webhook, status *int) Delivery {
	lastStatus := 0
	if status != nil {
		lastStatus = *status
	}
	flags, _ := json.Marshal(struct {
		WebhookID         string `json:"webhook_id"`
		WebhookName       string `json:"webhook_name"`
		LastFailureStatus int    `json:"last_failure_status"`
	}{hook.ID, hook.Name, lastStatus})
	return Delivery{
		ID:          ulid.Make().String(),
		UserID:      hook.UserID,
		ProfileID:   hook.ProfileID,
		Type:        DeliveryTypeWebhookAutoDisabled,
		ReasonFlags: flags,
	}
}

// profileRateLimiter is a per-profile sliding-window counter bounding webhook
// deliveries per minute. Over-limit notifications stay in the inbox; webhook
// attempts simply are not enqueued. Per-node state: fanout claims are
// node-exclusive (SKIP LOCKED), so one node owns a given event's enqueue.
type profileRateLimiter struct {
	mu      sync.Mutex
	windows map[profileKey][]time.Time
}

func newProfileRateLimiter() *profileRateLimiter {
	return &profileRateLimiter{windows: make(map[profileKey][]time.Time)}
}

// Allow reports whether the profile is under the per-minute limit and counts
// the delivery when it is.
func (l *profileRateLimiter) Allow(profile profileKey, limit int) bool {
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()

	window := l.windows[profile]
	kept := window[:0]
	for _, ts := range window {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= limit {
		l.windows[profile] = kept
		return false
	}
	l.windows[profile] = append(kept, now)
	return true
}
