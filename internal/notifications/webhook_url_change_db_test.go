package notifications

import (
	"net/http"
	"sync/atomic"
	"testing"
)

// saveURL replaces the webhook's URL as the owner's save does
// (WebhookService.applyWebhookInput): a new ciphertext, the streaks reset and
// any disable reason cleared, through the repository's guarded update.
func (h *webhookDeliveryHarness) saveURL(id, rawURL string) {
	h.t.Helper()
	ciphertext, err := h.sender.cipher.Encrypt(rawURL, webhookURLAAD(id))
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.sender.webhooks.UpdateGuarded(h.t.Context(), 1, "default", id, func(hook *Webhook) error {
		hook.URLCiphertext = ciphertext
		hook.ConsecutiveFailures = 0
		hook.DisabledReason = nil
		return nil
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *webhookDeliveryHarness) streaks(id string) (failures, rejections int) {
	h.t.Helper()
	if err := h.pool.QueryRow(h.t.Context(),
		`SELECT consecutive_failures, consecutive_rejections FROM notification_webhooks WHERE id = $1`, id).Scan(&failures, &rejections); err != nil {
		h.t.Fatal(err)
	}
	return failures, rejections
}

func (h *webhookDeliveryHarness) outcome(attemptID string) string {
	h.t.Helper()
	var outcome string
	if err := h.pool.QueryRow(h.t.Context(), `SELECT outcome FROM webhook_delivery_attempts WHERE id = $1`, attemptID).Scan(&outcome); err != nil {
		h.t.Fatal(err)
	}
	return outcome
}

// The owner saves a new URL while a delivery to the old one is in flight.
// The old URL's rejection finalizes its attempt but says nothing about the
// new URL, whose streaks the save reset: the new URL is disabled only after
// three rejections of its own (#4598).
func TestWebhookRejectionOfAReplacedURLLeavesTheNewURLsStreaksDB(t *testing.T) {
	var h *webhookDeliveryHarness
	var replace atomic.Bool
	h = newWebhookDeliveryHarness(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		if replace.CompareAndSwap(true, false) {
			h.saveURL("wh", "https://discord.example/api/webhooks/2/y")
		}
		return webhookResponse(http.StatusNotFound, nil), nil
	}))
	ctx := t.Context()
	h.addWebhook("wh")
	h.sender.processAttempt(ctx, h.claimNew("wh"))
	h.sender.processAttempt(ctx, h.claimNew("wh"))

	replace.Store(true)
	inFlight := h.claimNew("wh")
	h.sender.processAttempt(ctx, inFlight)
	if outcome := h.outcome(inFlight.ID); outcome != WebhookOutcomeFailed {
		t.Fatalf("the replaced URL's attempt ended %q, want failed", outcome)
	}
	if failures, rejections := h.streaks("wh"); failures != 0 || rejections != 0 {
		t.Fatalf("after the replaced URL's rejection: consecutive_failures %d, consecutive_rejections %d; want the new URL's streaks still reset", failures, rejections)
	}

	h.sender.processAttempt(ctx, h.claimNew("wh"))
	h.sender.processAttempt(ctx, h.claimNew("wh"))
	if enabled, _ := h.webhook("wh"); !enabled || h.notices() != 0 {
		t.Fatalf("after two rejections from the new URL: enabled %v, %d notices; want it still enabled", enabled, h.notices())
	}
	h.sender.processAttempt(ctx, h.claimNew("wh"))
	if enabled, _ := h.webhook("wh"); enabled || h.notices() != 1 {
		t.Fatalf("after three rejections from the new URL: enabled %v, %d notices; want it disabled with one notice", enabled, h.notices())
	}
}

// A delivery that runs out its retry schedule against the old URL while the
// owner saves a new one doesn't disable the webhook: the failures were the
// old URL's (#4598).
func TestWebhookExhaustedScheduleOfAReplacedURLDoesNotDisableDB(t *testing.T) {
	var h *webhookDeliveryHarness
	h = newWebhookDeliveryHarness(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		h.saveURL("wh", "https://discord.example/api/webhooks/2/y")
		return webhookResponse(http.StatusBadGateway, nil), nil
	}))
	ctx := t.Context()
	h.addWebhook("wh")
	attempt := h.claimNewAt("wh", webhookMaxAttempts-1)
	h.sender.processAttempt(ctx, attempt)
	if outcome := h.outcome(attempt.ID); outcome != WebhookOutcomeFailed {
		t.Fatalf("the replaced URL's last attempt ended %q, want failed", outcome)
	}
	if enabled, failures := h.webhook("wh"); !enabled || failures != 0 || h.notices() != 0 {
		t.Fatalf("after the replaced URL's schedule ran out: enabled %v, consecutive_failures %d, %d notices; want the new URL enabled and untouched", enabled, failures, h.notices())
	}
}
