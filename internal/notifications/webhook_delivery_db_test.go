package notifications

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// roundTripFunc answers webhook requests without a network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func webhookResponse(status int, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(""))}
}

type webhookDeliveryHarness struct {
	t        *testing.T
	pool     *pgxpool.Pool
	sender   *webhookSender
	service  *WebhookService
	disables atomic.Int32
	seq      atomic.Int32
}

// disableCounter counts the sender's auto-disable log records.
type disableCounter struct{ n *atomic.Int32 }

func (c disableCounter) Enabled(context.Context, slog.Level) bool { return true }
func (c disableCounter) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c disableCounter) WithGroup(string) slog.Handler            { return c }
func (c disableCounter) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "webhook auto-disabled" {
		c.n.Add(1)
	}
	return nil
}

func newWebhookDeliveryHarness(t *testing.T, transport http.RoundTripper) *webhookDeliveryHarness {
	t.Helper()
	pool := inboxPageDB(t)
	if _, err := pool.Exec(t.Context(), `
		CREATE TABLE notification_webhooks (LIKE public.notification_webhooks INCLUDING ALL);
		CREATE TABLE webhook_delivery_attempts (LIKE public.webhook_delivery_attempts INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	settings := NewSettings(mapSettingReader{SettingWebhooksEnabled: "true"})
	repo := NewWebhookRepository(pool)
	sender := newWebhookSender(repo, NewDeliveryRepository(pool), cipher, settings)
	sender.client = &http.Client{Transport: transport}
	h := &webhookDeliveryHarness{t: t, pool: pool, sender: sender}
	// Each auto-disable logs once, as its notice is posted.
	sender.logger = slog.New(disableCounter{&h.disables})
	h.service = newWebhookService(repo, cipher, settings, sender)
	return h
}

// addWebhook creates an enabled Discord webhook with the given ID.
func (h *webhookDeliveryHarness) addWebhook(id string) {
	h.t.Helper()
	url, err := h.sender.cipher.Encrypt("https://discord.example/api/webhooks/1/x", webhookURLAAD(id))
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.t.Context(), `
		INSERT INTO notification_webhooks (id, user_id, profile_id, name, type, url_ciphertext, url_host)
		VALUES ($1, 1, 'default', $1, 'discord', $2, 'discord.example')`, id, url); err != nil {
		h.t.Fatal(err)
	}
}

// claimNew queues one delivery for the webhook and claims its attempt, as
// the post-commit dispatcher does.
func (h *webhookDeliveryHarness) claimNew(webhookID string) DeliveryAttempt {
	h.t.Helper()
	n := h.seq.Add(1)
	deliveryID := fmt.Sprintf("%s-d%d", webhookID, n)
	ctx := h.t.Context()
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO notification_deliveries (id, user_id, profile_id, type, reason_flags, status)
		VALUES ($1, 1, 'default', 'request.approved', '{}', 'pending')`, deliveryID); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO webhook_delivery_attempts (id, notification_delivery_id, webhook_id, attempt_number, outcome)
		VALUES ($1, $1, $2, 0, 'pending')`, deliveryID, webhookID); err != nil {
		h.t.Fatal(err)
	}
	claimed, err := h.sender.webhooks.ClaimPendingForDelivery(ctx, deliveryID)
	if err != nil || len(claimed) != 1 {
		h.t.Fatalf("claim %s = %v, %v; want one attempt", deliveryID, claimed, err)
	}
	return claimed[0]
}

func (h *webhookDeliveryHarness) webhook(id string) (enabled bool, failures int) {
	h.t.Helper()
	if err := h.pool.QueryRow(h.t.Context(),
		`SELECT enabled, consecutive_failures FROM notification_webhooks WHERE id = $1`, id).Scan(&enabled, &failures); err != nil {
		h.t.Fatal(err)
	}
	return enabled, failures
}

// Three rejected deliveries in a row disable a webhook. Re-enabling it starts
// the count over, and workers that reach the third rejection together post
// one notice.
func TestWebhookRejectionsAutoDisableDB(t *testing.T) {
	var gate sync.WaitGroup
	h := newWebhookDeliveryHarness(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		gate.Done()
		gate.Wait()
		return webhookResponse(http.StatusNotFound, nil), nil
	}))
	ctx := t.Context()
	h.addWebhook("wh")
	reject := func() {
		t.Helper()
		gate.Add(1)
		h.sender.processAttempt(ctx, h.claimNew("wh"))
	}

	reject()
	reject()
	// Two workers send the third and fourth deliveries at once; both see
	// the streak reach three.
	gate.Add(2)
	third, fourth := h.claimNew("wh"), h.claimNew("wh")
	var both sync.WaitGroup
	for _, attempt := range []DeliveryAttempt{third, fourth} {
		both.Go(func() { h.sender.processAttempt(ctx, attempt) })
	}
	both.Wait()
	if enabled, _ := h.webhook("wh"); enabled {
		t.Fatal("webhook still enabled after three rejected deliveries")
	}
	if got := h.disables.Load(); got != 1 {
		t.Errorf("auto-disables = %d, want 1 (each posts a notice)", got)
	}

	on := true
	if _, err := h.service.UpdateGuarded(ctx, 1, "default", "wh", WebhookInput{Enabled: &on},
		func(int64) error { return nil }); err != nil {
		t.Fatal(err)
	}
	reject()
	if enabled, _ := h.webhook("wh"); !enabled {
		t.Fatal("the first rejection after re-enabling disabled the webhook again")
	}
}

// A destination's Retry-After can lengthen the retry schedule, never shorten
// it.
func TestWebhookRetryAfterIsAMinimumDB(t *testing.T) {
	h := newWebhookDeliveryHarness(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return webhookResponse(http.StatusTooManyRequests, http.Header{"Retry-After": {"1"}}), nil
	}))
	h.addWebhook("wh")
	attempt := h.claimNew("wh")
	h.sender.processAttempt(t.Context(), attempt)

	var outcome string
	var seconds float64
	if err := h.pool.QueryRow(t.Context(), `
		SELECT outcome, EXTRACT(EPOCH FROM next_retry_at - now())::float8
		FROM webhook_delivery_attempts WHERE id = $1`, attempt.ID).Scan(&outcome, &seconds); err != nil {
		t.Fatal(err)
	}
	if wait := time.Duration(seconds * float64(time.Second)); outcome != WebhookOutcomeRetrying || wait < 25*time.Second {
		t.Fatalf("after a 429 with Retry-After: 1, outcome %q retries in %v; want retrying in about 30s", outcome, wait)
	}
}

// A send that outlives its lease is reclaimed and sent again. Only the
// worker holding the current claim records the outcome, so one rejected
// delivery counts once.
func TestWebhookLateFinishAfterReclaimCountsOnceDB(t *testing.T) {
	var h *webhookDeliveryHarness
	var reclaimed []DeliveryAttempt
	var stale DeliveryAttempt
	h = newWebhookDeliveryHarness(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if reclaimed == nil {
			// The first send outlives its lease and the retry sweep
			// reclaims the attempt while it is still in flight.
			if _, err := h.pool.Exec(r.Context(), `UPDATE webhook_delivery_attempts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, stale.ID); err != nil {
				t.Error(err)
			}
			var err error
			if reclaimed, err = h.sender.webhooks.ClaimDue(r.Context(), 10); err != nil || len(reclaimed) != 1 {
				t.Errorf("reclaim = %v, %v; want the expired attempt", reclaimed, err)
			}
		}
		return webhookResponse(http.StatusNotFound, nil), nil
	}))
	ctx := t.Context()
	h.addWebhook("wh")
	stale = h.claimNew("wh")
	h.sender.processAttempt(ctx, stale)
	if len(reclaimed) != 1 {
		t.FailNow()
	}
	var outcome string
	if err := h.pool.QueryRow(ctx, `SELECT outcome FROM webhook_delivery_attempts WHERE id = $1`, stale.ID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if _, failures := h.webhook("wh"); outcome != WebhookOutcomeRetrying || failures != 0 {
		t.Fatalf("after the stale send: outcome %q, consecutive_failures %d; want the reclaimed claim untouched", outcome, failures)
	}
	h.sender.processAttempt(ctx, reclaimed[0])
	if _, failures := h.webhook("wh"); failures != 1 {
		t.Fatalf("consecutive_failures = %d after one rejected delivery, want 1", failures)
	}
}

// A worker deep into a long claimed batch leaves an attempt it can no longer
// finish inside the lease for the retry sweep, instead of sending it while
// another node may reclaim and send it too.
func TestWebhookSkipsSendWithoutEnoughLeaseDB(t *testing.T) {
	var sends atomic.Int32
	h := newWebhookDeliveryHarness(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		sends.Add(1)
		return webhookResponse(http.StatusNoContent, nil), nil
	}))
	h.addWebhook("wh")
	attempt := h.claimNew("wh")
	attempt.claimedAt = attempt.claimedAt.Add(-(webhookClaimLease - webhookSendBudget + time.Second))
	h.sender.processAttempt(t.Context(), attempt)

	var outcome string
	if err := h.pool.QueryRow(t.Context(), `SELECT outcome FROM webhook_delivery_attempts WHERE id = $1`, attempt.ID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 0 || outcome != WebhookOutcomeRetrying {
		t.Fatalf("sends = %d, outcome %q; want no send and the claim left to expire", sends.Load(), outcome)
	}

	// With its lease fresh, the next attempt is sent and recorded delivered.
	fresh := h.claimNew("wh")
	h.sender.processAttempt(t.Context(), fresh)
	if err := h.pool.QueryRow(t.Context(), `SELECT outcome FROM webhook_delivery_attempts WHERE id = $1`, fresh.ID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if enabled, failures := h.webhook("wh"); sends.Load() != 1 || outcome != WebhookOutcomeDelivered || !enabled || failures != 0 {
		t.Fatalf("sends = %d, outcome %q, enabled %v, failures %d; want one delivered send", sends.Load(), outcome, enabled, failures)
	}
}
