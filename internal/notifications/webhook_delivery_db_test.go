package notifications

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	t          *testing.T
	pool       *pgxpool.Pool
	sender     *webhookSender
	service    *WebhookService
	seq        atomic.Int32
	dispatched atomic.Int32
	// failNotice makes the auto-disable notice's inbox insert fail.
	failNotice atomic.Bool
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
	sender.logger = slog.New(slog.DiscardHandler)
	// The auto-disable notice goes into the inbox through the sender's
	// transaction, as the system wires it.
	sender.noticeWriter = func(context.Context) func(context.Context, pgx.Tx, Delivery) (*InsertedDelivery, error) {
		return func(ctx context.Context, tx pgx.Tx, notice Delivery) (*InsertedDelivery, error) {
			if h.failNotice.Load() {
				return nil, errors.New("inbox insert failed")
			}
			inserted, err := NewDeliveryRepository(pool).BulkInsert(ctx, tx, []Delivery{notice})
			if err != nil || len(inserted) == 0 {
				return nil, err
			}
			return &inserted[0], nil
		}
	}
	sender.dispatchNotice = func(context.Context, InsertedDelivery) { h.dispatched.Add(1) }
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
	return h.claimNewAt(webhookID, 0)
}

// claimNewAt is claimNew for an attempt that has already been tried
// tries times.
func (h *webhookDeliveryHarness) claimNewAt(webhookID string, tries int) DeliveryAttempt {
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
		VALUES ($1, $1, $2, $3, 'pending')`, deliveryID, webhookID, tries); err != nil {
		h.t.Fatal(err)
	}
	claimed, err := h.sender.webhooks.ClaimPendingForDelivery(ctx, deliveryID)
	if err != nil || len(claimed) != 1 {
		h.t.Fatalf("claim %s = %v, %v; want one attempt", deliveryID, claimed, err)
	}
	return claimed[0]
}

// notices counts the auto-disable notices in the inbox.
func (h *webhookDeliveryHarness) notices() int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.t.Context(), `SELECT count(*) FROM notification_deliveries WHERE type = $1`,
		DeliveryTypeWebhookAutoDisabled).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
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
	if got, sent := h.notices(), h.dispatched.Load(); got != 1 || sent != 1 {
		t.Errorf("%d notices in the inbox, %d dispatched; want one of each", got, sent)
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

// A reset that cleared only the failure streak, as a build from before the
// rejection streak writes it, also ends the rejection streak.
func TestWebhookRejectionsNeverOutrunFailuresDB(t *testing.T) {
	h := newWebhookDeliveryHarness(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return webhookResponse(http.StatusNotFound, nil), nil
	}))
	ctx := t.Context()
	h.addWebhook("wh")
	if _, err := h.pool.Exec(ctx, `UPDATE notification_webhooks SET consecutive_failures = 0, consecutive_rejections = 2 WHERE id = 'wh'`); err != nil {
		t.Fatal(err)
	}
	h.sender.processAttempt(ctx, h.claimNew("wh"))
	var rejections int
	if err := h.pool.QueryRow(ctx, `SELECT consecutive_rejections FROM notification_webhooks WHERE id = 'wh'`).Scan(&rejections); err != nil {
		t.Fatal(err)
	}
	if enabled, failures := h.webhook("wh"); !enabled || failures != 1 || rejections != 1 {
		t.Fatalf("after one rejection: enabled %v, consecutive_failures %d, consecutive_rejections %d; want enabled with a streak of one",
			enabled, failures, rejections)
	}
}

// The disable and its notice commit with the attempt's outcome and the
// streaks: when the notice can't be written, none of it applies, and the
// attempt runs again once its lease runs out.
func TestWebhookFinishIsAtomicDB(t *testing.T) {
	h := newWebhookDeliveryHarness(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return webhookResponse(http.StatusNotFound, nil), nil
	}))
	ctx := t.Context()
	h.addWebhook("wh")
	h.sender.processAttempt(ctx, h.claimNew("wh"))
	h.sender.processAttempt(ctx, h.claimNew("wh"))

	h.failNotice.Store(true)
	third := h.claimNew("wh")
	h.sender.processAttempt(ctx, third)
	var outcome string
	if err := h.pool.QueryRow(ctx, `SELECT outcome FROM webhook_delivery_attempts WHERE id = $1`, third.ID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if enabled, failures := h.webhook("wh"); !enabled || failures != 2 || outcome != WebhookOutcomeRetrying || h.notices() != 0 {
		t.Fatalf("after a failed notice: enabled %v, consecutive_failures %d, outcome %q, %d notices; want nothing applied",
			enabled, failures, outcome, h.notices())
	}

	h.failNotice.Store(false)
	if _, err := h.pool.Exec(ctx, `UPDATE webhook_delivery_attempts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, third.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := h.sender.webhooks.ClaimDue(ctx, 10)
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("reclaim = %v, %v; want the third attempt", reclaimed, err)
	}
	h.sender.processAttempt(ctx, reclaimed[0])
	if enabled, failures := h.webhook("wh"); enabled || failures != 3 || h.notices() != 1 {
		t.Fatalf("after the retry: enabled %v, consecutive_failures %d, %d notices; want it disabled with one notice",
			enabled, failures, h.notices())
	}
}

// The last scheduled attempt turns the webhook off and records that it did.
// When its owner turned the webhook off during the send, the attempt just
// fails and no notice is posted.
func TestWebhookExhaustedScheduleDisablesOnceDB(t *testing.T) {
	var h *webhookDeliveryHarness
	var ownerOff atomic.Bool
	h = newWebhookDeliveryHarness(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if ownerOff.Load() {
			if _, err := h.pool.Exec(r.Context(), `UPDATE notification_webhooks SET enabled = false WHERE id = 'wh'`); err != nil {
				t.Error(err)
			}
		}
		return webhookResponse(http.StatusBadGateway, nil), nil
	}))
	ctx := t.Context()
	h.addWebhook("wh")
	last := func() string {
		t.Helper()
		attempt := h.claimNewAt("wh", webhookMaxAttempts-1)
		h.sender.processAttempt(ctx, attempt)
		var outcome string
		var tries int
		if err := h.pool.QueryRow(ctx, `SELECT outcome, attempt_number FROM webhook_delivery_attempts WHERE id = $1`, attempt.ID).Scan(&outcome, &tries); err != nil {
			t.Fatal(err)
		}
		if tries != webhookMaxAttempts {
			t.Fatalf("attempt_number = %d after the last scheduled try, want %d", tries, webhookMaxAttempts)
		}
		return outcome
	}

	ownerOff.Store(true)
	if outcome := last(); outcome != WebhookOutcomeFailed || h.notices() != 0 {
		t.Fatalf("owner turned it off mid-send: outcome %q, %d notices; want failed and no notice", outcome, h.notices())
	}
	ownerOff.Store(false)
	if _, err := h.pool.Exec(ctx, `UPDATE notification_webhooks SET enabled = true WHERE id = 'wh'`); err != nil {
		t.Fatal(err)
	}
	if outcome := last(); outcome != WebhookOutcomeAutoDisabled || h.notices() != 1 {
		t.Fatalf("schedule exhausted: outcome %q, %d notices; want auto_disabled with one notice", outcome, h.notices())
	}
	if enabled, _ := h.webhook("wh"); enabled {
		t.Fatal("webhook still enabled after its retry schedule ran out")
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
