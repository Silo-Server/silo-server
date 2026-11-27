package notifications

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pushLeaseHarness is one registered Apple device with a claimed attempt for
// one delivery, and a sender whose relay answers through relay.
type pushLeaseHarness struct {
	pool    *pgxpool.Pool
	repo    *PushDeviceRepository
	sender  *pushSender
	device  *PushDevice
	prefix  string
	account int
	relay   func(*http.Request) (*http.Response, error)
}

func newPushLeaseHarness(t *testing.T) *pushLeaseHarness {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	h := &pushLeaseHarness{pool: pool, repo: NewPushDeviceRepository(pool), prefix: fmt.Sprintf("pushlease-%d", time.Now().UnixNano())}
	account, _ := seedPushScopeAccounts(t, pool, h.prefix)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM notification_deliveries WHERE user_id = $1`, account)
	})
	cipher := testPushCipher(t)
	h.device, err = h.repo.UpsertApple(ctx, ApplePushDeviceRegistration{
		UserID: account, ProfileID: "default", DeviceID: h.prefix + "-phone", APNsToken: strings.Repeat("a", 64),
		APNsEnvironment: APNsEnvironmentSandbox, APNsTopic: ApplePushTopicSilo, PushMode: PushModePrivatePush,
	}, cipher)
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	h.account = account
	settings := NewSettings(mapSettingReader{SettingPushRelayURL: DefaultPushRelayURL, SettingPushRelayAPIKey: "relay-key"})
	h.sender = newPushSender(h.repo, NewDeliveryRepository(pool), cipher, settings)
	h.sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return h.relay(req)
	})}
	return h
}

// claim queues a pending attempt for a delivery of its own, named id, and
// claims it.
func (h *pushLeaseHarness) claim(t *testing.T, id string) PushDeliveryAttempt {
	t.Helper()
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `INSERT INTO notification_deliveries (id, user_id, profile_id, type, reason_flags)
		VALUES ($1, $2, 'default', 'webhook.auto_disabled', '{}')`, id, h.account); err != nil {
		t.Fatalf("seed delivery: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO push_delivery_attempts (id, notification_delivery_id, push_device_id, trigger_type, provider, platform, attempt_number, outcome)
		VALUES ($1, $1, $2, 'delivery', 'silo_relay', 'apple', 0, 'pending')`, id, h.device.ID); err != nil {
		t.Fatalf("queue attempt: %v", err)
	}
	claimed, err := h.repo.ClaimPendingPushForDelivery(ctx, id)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v; want the queued attempt", claimed, err)
	}
	return claimed[0]
}

func (h *pushLeaseHarness) attempt(t *testing.T, id string) (outcome string, attemptNumber int, nextRetry *time.Time) {
	t.Helper()
	if err := h.pool.QueryRow(context.Background(),
		`SELECT outcome, attempt_number, next_retry_at FROM push_delivery_attempts WHERE id = $1`, id).Scan(&outcome, &attemptNumber, &nextRetry); err != nil {
		t.Fatal(err)
	}
	return outcome, attemptNumber, nextRetry
}

// A worker deep into a long claimed batch leaves an attempt it can no longer
// finish inside the lease for the retry sweep, instead of sending it while
// another node may reclaim and send it too (#4593).
func TestPushSkipsSendWithoutEnoughLeaseDB(t *testing.T) {
	h := newPushLeaseHarness(t)
	var sends atomic.Int32
	h.relay = func(*http.Request) (*http.Response, error) {
		sends.Add(1)
		return relayResponse(http.StatusOK, `{"request_id":"relay-request","status":"accepted"}`), nil
	}
	stale := h.claim(t, h.prefix+"-stale")
	stale.claimedAt = stale.claimedAt.Add(-(webhookClaimLease - pushSendBudget + time.Second))
	h.sender.processAttempt(context.Background(), stale)
	if outcome, _, _ := h.attempt(t, stale.ID); sends.Load() != 0 || outcome != PushOutcomeRetrying {
		t.Fatalf("sends = %d, outcome %q; want no send and the claim left to expire", sends.Load(), outcome)
	}

	// With its lease fresh, an attempt is sent and recorded delivered.
	fresh := h.claim(t, h.prefix+"-fresh")
	h.sender.processAttempt(context.Background(), fresh)
	if outcome, _, _ := h.attempt(t, fresh.ID); sends.Load() != 1 || outcome != PushOutcomeDelivered {
		t.Fatalf("sends = %d, outcome %q; want one delivered send", sends.Load(), outcome)
	}
}

// A send that outlives its lease is reclaimed by the retry sweep. Only the
// worker holding the current claim records the outcome, so the stale send's
// result neither overwrites the new claim nor marks the device as failing
// (#4593).
func TestPushLateFinishAfterReclaimLeavesTheNewClaimDB(t *testing.T) {
	h := newPushLeaseHarness(t)
	var reclaimed []PushDeliveryAttempt
	var stale PushDeliveryAttempt
	h.relay = func(r *http.Request) (*http.Response, error) {
		if reclaimed == nil {
			if _, err := h.pool.Exec(r.Context(), `UPDATE push_delivery_attempts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, stale.ID); err != nil {
				t.Error(err)
			}
			var err error
			if reclaimed, err = h.repo.ClaimDuePushAttempts(r.Context(), 10); err != nil || len(reclaimed) != 1 {
				t.Errorf("reclaim = %v, %v; want the expired attempt", reclaimed, err)
			}
		}
		return relayResponse(http.StatusServiceUnavailable, `{"error":"relay_unavailable"}`), nil
	}
	stale = h.claim(t, h.prefix+"-stale")
	if updated := h.sender.processAttempt(context.Background(), stale); updated != nil {
		t.Fatalf("the stale send recorded %+v; want nothing, the attempt was reclaimed", updated)
	}
	if len(reclaimed) != 1 {
		t.FailNow()
	}
	outcome, attemptNumber, nextRetry := h.attempt(t, stale.ID)
	if outcome != PushOutcomeRetrying || attemptNumber != 0 || nextRetry == nil || !nextRetry.Equal(*reclaimed[0].NextRetryAt) {
		t.Fatalf("after the stale send: outcome %q, attempt %d, next retry %v; want the reclaimed claim untouched (%v)",
			outcome, attemptNumber, nextRetry, reclaimed[0].NextRetryAt)
	}
	var failureCode *string
	if err := h.pool.QueryRow(context.Background(), `SELECT last_failure_code FROM push_devices WHERE id = $1`, h.device.ID).Scan(&failureCode); err != nil {
		t.Fatal(err)
	}
	if failureCode != nil {
		t.Fatalf("device last_failure_code = %q after a send whose claim was reclaimed, want none", *failureCode)
	}
}
