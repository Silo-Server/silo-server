package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/profileedits"
)

type relayedWake struct {
	path, idempotencyKey string
	body                 map[string]any
}

// profileSetWakeSystem is one API server's notification system on pool,
// sending through a fake relay that reports each request on relayed.
func profileSetWakeSystem(t *testing.T, pool *pgxpool.Pool, repo *PushDeviceRepository, settings map[string]string, relayed chan<- relayedWake) *System {
	t.Helper()
	values := mapSettingReader{SettingPushRelayURL: DefaultPushRelayURL, SettingPushRelayAPIKey: "relay-key"}
	for key, value := range settings {
		values[key] = value
	}
	sender := newPushSender(repo, NewDeliveryRepository(pool), testPushCipher(t), NewSettings(values))
	sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode relay body: %v", err)
		}
		relayed <- relayedWake{path: req.URL.Path, idempotencyKey: req.Header.Get("Idempotency-Key"), body: body}
		return relayResponse(http.StatusOK, `{"request_id":"relay-request","status":"accepted"}`), nil
	})}
	return &System{Settings: sender.settings, pushDeviceRepo: repo, pushSender: sender, logger: slog.Default()}
}

// wakeHarness reads and ages one test schema's wake attempts and collects
// what the fake relay received.
type wakeHarness struct {
	t       *testing.T
	pool    *pgxpool.Pool
	relayed chan relayedWake
}

func (h wakeHarness) attempts() int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.t.Context(), `SELECT count(*) FROM push_delivery_attempts WHERE trigger_type = $1`,
		PushTriggerProfileSet).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// pastTheMinute ages every wake past the debounce interval.
func (h wakeHarness) pastTheMinute() {
	h.t.Helper()
	if _, err := h.pool.Exec(h.t.Context(), `UPDATE push_delivery_attempts SET created_at = created_at - interval '61 seconds'`); err != nil {
		h.t.Fatal(err)
	}
}

// receive waits for n relay requests, which the waker sends in the background.
func (h wakeHarness) receive(n int) []relayedWake {
	h.t.Helper()
	got := make([]relayedWake, 0, n)
	deadline := time.After(30 * time.Second)
	for len(got) < n {
		select {
		case wake := <-h.relayed:
			got = append(got, wake)
		case <-deadline:
			h.t.Fatalf("relay got %d wakes, want %d", len(got), n)
		}
	}
	return got
}

// editThenWake records a name edit of account 7's profile in a request that
// commits or rolls it back, then wakes with that request's context.
func (h wakeHarness) editThenWake(node *System, commit bool) bool {
	h.t.Helper()
	ctx, req := profileedits.WithRequest(h.t.Context())
	req.SetActor(7, false, nil)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := profileedits.RecordFields(ctx, tx, 7, "default", []string{profileedits.FieldName}); err != nil {
		h.t.Fatal(err)
	}
	if commit {
		err = tx.Commit(ctx)
	} else {
		err = tx.Rollback(ctx)
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return node.WakeProfileSetDevices(ctx, 7)
}

// wakeTogether wakes account 7 from every node at the same moment.
func wakeTogether(nodes ...*System) []bool {
	answers := make([]bool, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Go(func() { answers[i] = node.WakeProfileSetDevices(context.Background(), 7) })
	}
	wg.Wait()
	return answers
}

// An edit to a profile from the account's set wakes each of the account's
// phones once a minute at most, on every API server, with a background push
// that shows nothing. Regressions: a push storm when an owner edits several
// fields or two servers record at once; an alert where a silent wake belongs;
// a wake for an edit that rolled back; pushes from a server with push off.
func TestProfileSetWakeDebounceDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE profile_set_edits (LIKE public.profile_set_edits INCLUDING ALL);
		CREATE TABLE user_profiles (user_id integer NOT NULL, id text NOT NULL, sync_id uuid);
		INSERT INTO user_profiles VALUES (7, 'default', gen_random_uuid())`); err != nil {
		t.Fatal(err)
	}
	registerForSession(t, repo, "live")
	h := wakeHarness{t: t, pool: pool, relayed: make(chan relayedWake, 16)}
	nodeA := profileSetWakeSystem(t, pool, repo, nil, h.relayed)
	nodeB := profileSetWakeSystem(t, pool, repo, nil, h.relayed)

	// Two servers recording at the same moment queue one wake per phone.
	if answers := wakeTogether(nodeA, nodeB); !answers[0] || !answers[1] || h.attempts() != 2 {
		t.Fatalf("concurrent wakes answered %v with %d attempts, want true, true and 2", answers, h.attempts())
	}
	paths := map[string]bool{}
	for _, wake := range h.receive(2) {
		paths[wake.path] = true
		if wake.body["mode"] != relayModeBackgroundWake || wake.body["collapse_id"] != profileSetCollapseID ||
			wake.idempotencyKey == "" || wake.body["delivery_id"] != wake.idempotencyKey {
			t.Errorf("relay request to %s with key %q = %v; want mode %s, collapse_id %s and the attempt ID",
				wake.path, wake.idempotencyKey, wake.body, relayModeBackgroundWake, profileSetCollapseID)
		}
	}
	if !paths[relayAppleSendPath] || !paths[relayFcmSendPath] {
		t.Fatalf("relay paths = %v, want the Apple and the Android phone", paths)
	}

	// A second edit inside the minute sends nothing more and still tells the
	// owner a wake is on its way; one after the minute wakes again.
	if !nodeB.WakeProfileSetDevices(ctx, 7) || h.attempts() != 2 {
		t.Fatalf("wake inside the minute: %d attempts, want 2 and a true answer", h.attempts())
	}
	h.pastTheMinute()
	if !nodeA.WakeProfileSetDevices(ctx, 7) || h.attempts() != 4 {
		t.Fatalf("wake after the minute: %d attempts, want 4 and a true answer", h.attempts())
	}
	h.receive(2)

	// A request whose edit rolled back wakes nothing; one whose edit
	// committed does.
	h.pastTheMinute()
	if h.editThenWake(nodeA, false) || h.attempts() != 4 {
		t.Fatalf("wake after a rolled-back edit: %d attempts, want 4 and a false answer", h.attempts())
	}
	if !h.editThenWake(nodeA, true) || h.attempts() != 6 {
		t.Fatalf("wake after a committed edit: %d attempts, want 6 and a true answer", h.attempts())
	}
	h.receive(2)

	// No registered phone, or push off for both platforms: nothing queued,
	// nothing recorded, no error.
	h.pastTheMinute()
	if nodeA.WakeProfileSetDevices(ctx, 8) || h.attempts() != 6 {
		t.Fatalf("wake of an account with no phone: %d attempts, want 6 and a false answer", h.attempts())
	}
	off := profileSetWakeSystem(t, pool, repo, map[string]string{
		SettingApplePushDeliveryEnabled: "false", SettingAndroidPushDeliveryEnabled: "false",
	}, h.relayed)
	if off.WakeProfileSetDevices(ctx, 7) || h.attempts() != 6 {
		t.Fatalf("wake with push off: %d attempts, want 6 and a false answer", h.attempts())
	}
	select {
	case wake := <-h.relayed:
		t.Fatalf("unexpected relay request to %s", wake.path)
	default:
	}
}

// The relay mode follows the attempt's trigger: alerts and the
// administrator's test push keep the alert mode and the delivery's collapse
// ID, and only a profile set wake is a background wake.
func TestRelayModeFollowsTheTrigger(t *testing.T) {
	for _, tc := range []struct {
		trigger, mode, collapseID string
	}{
		{PushTriggerDelivery, relayModePrivateAlert, "delivery-1"},
		{PushTriggerTest, relayModePrivateAlert, "delivery-1"},
		{PushTriggerProfileSet, relayModeBackgroundWake, profileSetCollapseID},
	} {
		for _, platform := range []string{PushPlatformApple, PushPlatformAndroid} {
			var body map[string]any
			sender := newPushSender(nil, nil, nil, NewSettings(mapSettingStore{}))
			sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Errorf("decode relay body: %v", err)
				}
				return relayResponse(http.StatusOK, `{"request_id":"r","status":"accepted"}`), nil
			})}
			delivery := "delivery-1"
			attempt := PushDeliveryAttempt{ID: "attempt-1", TriggerType: tc.trigger, NotificationDeliveryID: &delivery}
			if tc.trigger == PushTriggerProfileSet {
				attempt.NotificationDeliveryID = nil
			}
			device := &PushDevice{Platform: platform, ProfileID: "profile-1", ServerDeviceID: "server-device-1"}
			sender.sendWithCapability(context.Background(), attempt, device, "token", pushRelayTarget{}, DefaultPushRelayURL, "capability")
			if body["mode"] != tc.mode || body["collapse_id"] != tc.collapseID {
				t.Errorf("%s on %s: mode %v, collapse_id %v; want %s, %s", tc.trigger, platform, body["mode"], body["collapse_id"], tc.mode, tc.collapseID)
			}
		}
	}
}

// assertNeverClaimed ages every wake row past each claim query's time
// threshold and checks that no claim query returns one: a server of the
// previous release would send a claimed wake as a visible alert.
func (h wakeHarness) assertNeverClaimed(repo *PushDeviceRepository, state string) {
	h.t.Helper()
	ctx := h.t.Context()
	rows, err := h.pool.Query(ctx, `UPDATE push_delivery_attempts
		SET attempted_at = attempted_at - interval '2 hours', created_at = created_at - interval '2 hours'
		WHERE trigger_type = $1 RETURNING id`, PushTriggerProfileSet)
	if err != nil {
		h.t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) == 0 {
		h.t.Fatalf("wake rows %s: %v (err %v), want some", state, ids, err)
	}
	claimed, err := repo.ClaimDuePushAttempts(ctx, 100)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, id := range ids {
		byID, err := repo.ClaimPushAttemptByID(ctx, id)
		if err != nil {
			h.t.Fatal(err)
		}
		claimed = append(claimed, byID...)
	}
	if len(claimed) != 0 {
		h.t.Fatalf("claim queries returned wakes %s: %+v", state, claimed)
	}
}

// waitForOutcome waits until every wake row has outcome, as the background
// send records it.
func (h wakeHarness) waitForOutcome(outcome string) {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var other int
		if err := h.pool.QueryRow(h.t.Context(), `SELECT count(*) FROM push_delivery_attempts
			WHERE trigger_type = $1 AND (outcome <> $2 OR next_retry_at IS NOT NULL)`,
			PushTriggerProfileSet, outcome).Scan(&other); err != nil {
			h.t.Fatal(err)
		}
		if other == 0 {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%d wake rows not %s with no next retry", other, outcome)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// No claim query, on this release or the previous one (they share these
// queries), ever returns a wake: not while its send is in flight, not after
// a relay failure that would retry an alert, not once delivered. Regression:
// a wake left pending or retrying is claimed by a previous-release server's
// sweep and reaches the phone as an alert whose display fetch answers 404.
func TestProfileSetWakeIsNeverClaimedDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	registerForSession(t, repo, "live")
	h := wakeHarness{t: t, pool: pool}
	arrived, gate := make(chan struct{}, 4), make(chan int)
	node := profileSetWakeSystem(t, pool, repo, nil, nil)
	node.pushSender.client = &http.Client{Transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
		arrived <- struct{}{}
		status := <-gate
		return relayResponse(status, `{"request_id":"r","status":"accepted"}`), nil
	})}

	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("first wake answered false")
	}
	<-arrived // the first phone's send is in flight; the second waits behind it
	h.assertNeverClaimed(repo, "in flight")
	gate <- http.StatusServiceUnavailable
	<-arrived
	gate <- http.StatusServiceUnavailable
	h.waitForOutcome(PushOutcomeFailed)
	h.assertNeverClaimed(repo, "after a retryable relay failure")

	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("wake after the minute answered false")
	}
	for range 2 {
		<-arrived
		gate <- http.StatusOK
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM push_delivery_attempts WHERE outcome = $1`, PushOutcomeFailed); err != nil {
		t.Fatal(err)
	}
	h.waitForOutcome(PushOutcomeDelivered)
	h.assertNeverClaimed(repo, "delivered")
}
