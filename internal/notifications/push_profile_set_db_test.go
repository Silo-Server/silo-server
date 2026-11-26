package notifications

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
		select {
		case relayed <- relayedWake{path: req.URL.Path, idempotencyKey: req.Header.Get("Idempotency-Key"), body: body}:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
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
	if _, err := h.pool.Exec(h.t.Context(), `
		UPDATE push_delivery_attempts SET created_at = created_at - interval '61 seconds';
		UPDATE push_profile_set_wakes SET last_wake_at = last_wake_at - interval '61 seconds'`); err != nil {
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

// edit records an edit of field of account 7's profile in a new request that
// commits or rolls it back, and returns that request's context.
func (h wakeHarness) edit(field string, commit bool) context.Context {
	h.t.Helper()
	ctx, req := profileedits.WithRequest(h.t.Context())
	req.SetActor(7, false, nil)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := profileedits.RecordFields(ctx, tx, 7, "default", []string{field}); err != nil {
		h.t.Fatal(err)
	}
	if commit {
		if err := tx.Commit(ctx); err != nil {
			h.t.Fatal(err)
		}
	}
	return ctx
}

// editThenWake records a name edit of account 7's profile in a request that
// commits or rolls it back, then wakes with that request's context.
func (h wakeHarness) editThenWake(node *System, commit bool) bool {
	h.t.Helper()
	return node.WakeProfileSetDevices(h.edit(profileedits.FieldName, commit), 7)
}

// wakeTogether wakes account 7 from every node at once, staged so that the
// wakes overlap: a third connection holds the account's wake time row until
// every node is waiting on it. A node that never waits on that row fails the
// test, so it fails whenever the wake is not decided on the row, not only
// when the timing happens to line up. released is the holder's clock just
// before it let the row go.
func (h wakeHarness) wakeTogether(nodes ...*System) (answers []bool, released time.Time) {
	h.t.Helper()
	ctx := h.t.Context()
	if _, err := h.pool.Exec(ctx, `INSERT INTO push_profile_set_wakes (user_id, last_wake_at)
		VALUES (7, now() - interval '1 hour')
		ON CONFLICT (user_id) DO UPDATE SET last_wake_at = excluded.last_wake_at`); err != nil {
		h.t.Fatal(err)
	}
	holder, err := h.pool.Begin(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM push_profile_set_wakes WHERE user_id = 7 FOR UPDATE`); err != nil {
		h.t.Fatal(err)
	}

	answers = make([]bool, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Go(func() { answers[i] = node.WakeProfileSetDevices(context.Background(), 7) })
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()

	deadline := time.After(30 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for waiting := 0; waiting < len(nodes); {
		select {
		case <-finished:
			h.t.Fatal("the wakes finished without waiting on the account's wake time")
		case <-deadline:
			h.t.Fatalf("%d of %d wakes waiting on the account's wake time", waiting, len(nodes))
		case <-poll.C:
		}
		if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE 'INSERT INTO push_profile_set_wakes%'`).Scan(&waiting); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := holder.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&released); err != nil {
		h.t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		h.t.Fatal(err)
	}
	<-finished
	return answers, released
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

	// Two servers recording at the same moment queue one wake per phone, and
	// only the one that queued it answers true. The time recorded is when
	// the wake was admitted, after its wait on the account's row, so the next
	// wake is a full interval after this one. Regression: the upsert stored
	// the time it proposed before the wait, earlier than its admission.
	answers, released := h.wakeTogether(nodeA, nodeB)
	if answers[0] == answers[1] || h.attempts() != 2 {
		t.Fatalf("concurrent wakes answered %v with %d attempts, want one true, one false and 2", answers, h.attempts())
	}
	var recordedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT last_wake_at FROM push_profile_set_wakes WHERE user_id = 7`).Scan(&recordedAt); err != nil {
		t.Fatal(err)
	}
	if recordedAt.Before(released) {
		t.Fatalf("wake recorded at %v, before the account's row was released at %v", recordedAt, released)
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
	// The relay saw the requests before the send recorded them and released
	// the account's mark; the next wake waits for that.
	waitForWakeEnded(t, nodeA)
	waitForWakeEnded(t, nodeB)

	// A second edit inside the minute queues nothing and answers false: the
	// earlier wake may have been read before this edit existed. One after the
	// minute wakes again.
	if nodeB.WakeProfileSetDevices(ctx, 7) || h.attempts() != 2 {
		t.Fatalf("wake inside the minute: %d attempts, want 2 and a false answer", h.attempts())
	}
	h.pastTheMinute()
	if !nodeA.WakeProfileSetDevices(ctx, 7) || h.attempts() != 4 {
		t.Fatalf("wake after the minute: %d attempts, want 4 and a true answer", h.attempts())
	}
	h.receive(2)
	waitForWakeEnded(t, nodeA)

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
	waitForWakeEnded(t, nodeA)

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

// A request's wake answers for that request's own edits: one whose edit
// rolled back answers false and queues nothing, even when another request's
// later edit of the same account committed first, and that other request
// queues its own wake. Regression: the commit check looked for any edit row at
// or above the request's first revision, so the rolled-back request found the
// other's row, queued a wake and answered true, and the request whose edit did
// commit then met the limit and answered false.
func TestProfileSetWakeAnswersForItsOwnEditDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	if _, err := pool.Exec(t.Context(), `
		CREATE TABLE profile_set_edits (LIKE public.profile_set_edits INCLUDING ALL);
		CREATE TABLE user_profiles (user_id integer NOT NULL, id text NOT NULL, sync_id uuid);
		INSERT INTO user_profiles VALUES (7, 'default', gen_random_uuid())`); err != nil {
		t.Fatal(err)
	}
	registerForSession(t, repo, "live")
	h := wakeHarness{t: t, pool: pool, relayed: make(chan relayedWake, 4)}
	node := profileSetWakeSystem(t, pool, repo, nil, h.relayed)

	rolledBack := h.edit(profileedits.FieldName, false)
	committed := h.edit(profileedits.FieldPicture, true)
	if node.WakeProfileSetDevices(rolledBack, 7) || h.attempts() != 0 {
		t.Fatalf("wake of the rolled-back request: %d attempts, want 0 and a false answer", h.attempts())
	}
	if !node.WakeProfileSetDevices(committed, 7) || h.attempts() != 2 {
		t.Fatalf("wake of the committed request: %d attempts, want 2 and a true answer", h.attempts())
	}
	h.receive(2)
	waitForWakeEnded(t, node)
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
	if _, err := h.pool.Exec(ctx, `UPDATE push_profile_set_wakes SET last_wake_at = last_wake_at - interval '2 hours'`); err != nil {
		h.t.Fatal(err)
	}
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
	deadline := time.After(30 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
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
		select {
		case <-deadline:
			h.t.Fatalf("%d wake rows not %s with no next retry", other, outcome)
		case <-poll.C:
		}
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
	node.pushSender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		arrived <- struct{}{}
		select {
		case status := <-gate:
			return relayResponse(status, `{"request_id":"r","status":"accepted"}`), nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})}

	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("first wake answered false")
	}
	awaitRelay(t, arrived, "the first wake's first phone") // in flight; the second waits behind it
	h.assertNeverClaimed(repo, "in flight")
	answerRelay(t, gate, http.StatusServiceUnavailable, "the first wake's first phone")
	awaitRelay(t, arrived, "the first wake's second phone")
	answerRelay(t, gate, http.StatusServiceUnavailable, "the first wake's second phone")
	h.waitForOutcome(PushOutcomeFailed)
	h.assertNeverClaimed(repo, "after a retryable relay failure")
	waitForWakeEnded(t, node)

	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("wake after the minute answered false")
	}
	for _, phone := range []string{"first", "second"} {
		awaitRelay(t, arrived, "the second wake's "+phone+" phone")
		answerRelay(t, gate, http.StatusOK, "the second wake's "+phone+" phone")
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM push_delivery_attempts WHERE outcome = $1`, PushOutcomeFailed); err != nil {
		t.Fatal(err)
	}
	h.waitForOutcome(PushOutcomeDelivered)
	h.assertNeverClaimed(repo, "delivered")
	waitForWakeEnded(t, node)
}

// awaitRelay waits for the stub relay to report the call named what.
func awaitRelay(t *testing.T, arrived <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-arrived:
	case <-time.After(30 * time.Second):
		t.Fatalf("the relay was never called for %s", what)
	}
}

// answerRelay lets the stub relay's waiting call, named what, answer status.
func answerRelay(t *testing.T, gate chan<- int, status int, what string) {
	t.Helper()
	select {
	case gate <- status:
	case <-time.After(30 * time.Second):
		t.Fatalf("no relay call was waiting to answer %s", what)
	}
}

// A wake ends within its budget, however the relay behaves: with a relay
// that never answers and more phones than the budget reaches, every row
// ends failed as out of time, none in flight. Regression: a wake sent each
// phone under its own long deadline, so one account's wake could run for
// over half an hour while the next one started.
func TestProfileSetWakeEndsWithinItsBudgetDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	registerForSession(t, repo, "live")
	registerAndroid(t, repo, "android-extra", 1, false)
	defer func(previous time.Duration) { profileSetWakeBudget = previous }(profileSetWakeBudget)
	profileSetWakeBudget = 300 * time.Millisecond

	var calls atomic.Int32
	node := profileSetWakeSystem(t, pool, repo, nil, nil)
	node.pushSender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-req.Context().Done() // the relay never answers
		return nil, req.Context().Err()
	})}
	started := time.Now()
	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("wake answered false")
	}
	outOfTime := PushOutcomeFailed + ":wake_out_of_time"
	waitForWakeRows(t, pool, outOfTime, outOfTime, outOfTime)
	if elapsed := time.Since(started); elapsed > profileSetWakeBudget+profileSetFinalizeTimeout {
		t.Fatalf("the wake took %v, want at most its budget %v plus the final write's %v", elapsed, profileSetWakeBudget, profileSetFinalizeTimeout)
	}
	if calls.Load() != 3 {
		t.Fatalf("relay calls = %d, want 3: the three phones are sent at once", calls.Load())
	}
}

// A wake for an account does not start while the previous one is still in
// flight on this server, and starts again once it ends. Regression: nothing
// kept one account's wakes apart but the relay answering in time.
func TestProfileSetWakeWaitsForTheWakeInFlightDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	registerAndroid(t, repo, "android-held", 1, false)
	h := wakeHarness{t: t, pool: pool}
	held, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	node := profileSetWakeSystem(t, pool, repo, nil, nil)
	node.pushSender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			held <- struct{}{}
			select { // the first wake's send stays in flight until the test lets it go
			case <-release:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		return relayResponse(http.StatusOK, `{"request_id":"r","status":"accepted"}`), nil
	})}

	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("first wake answered false")
	}
	awaitRelay(t, held, "the first wake")
	h.pastTheMinute() // the account's limit no longer holds the next wake back
	if node.WakeProfileSetDevices(t.Context(), 7) || h.attempts() != 1 || calls.Load() != 1 {
		t.Fatalf("wake while one is in flight: %d rows, %d relay calls; want a false answer, 1 and 1", h.attempts(), calls.Load())
	}

	close(release)
	waitForWakeEnded(t, node)
	h.pastTheMinute()
	if !node.WakeProfileSetDevices(t.Context(), 7) || h.attempts() != 2 {
		t.Fatalf("wake after the first ended: %d rows, want 2", h.attempts())
	}
	waitForWakeRows(t, pool, PushOutcomeDelivered+":", PushOutcomeDelivered+":")
	waitForWakeEnded(t, node)
}

// One wake of an account ends before the account's limit lets the next one
// start. A wake ends within its budget plus its tail's write, and is queued
// up to profileSetEnqueueTimeout after the limit records it, so the two
// together must stay under the interval less that. Regression: a budget or
// tail raised, or an interval or queue deadline changed, so that an
// account's wakes overlap, or one holds its in-flight mark past the minute
// and the guard refuses an edit made after it.
func TestProfileSetWakeBudgetIsShorterThanTheInterval(t *testing.T) {
	wake := profileSetWakeBudget + profileSetFinalizeTimeout
	if limit := profileSetWakeInterval - profileSetEnqueueTimeout; profileSetWakeBudget <= 0 || wake >= limit {
		t.Fatalf("wake budget %v plus its tail %v, want under the interval %v less the queue deadline %v",
			profileSetWakeBudget, profileSetFinalizeTimeout, profileSetWakeInterval, profileSetEnqueueTimeout)
	}
}

// waitForWakeRows waits until the wake rows, as outcome:reason sorted by
// outcome, are want.
func waitForWakeRows(t *testing.T, pool *pgxpool.Pool, want ...string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		rows, err := pool.Query(t.Context(), `SELECT outcome || ':' || COALESCE(upstream_reason, '') FROM push_delivery_attempts
			WHERE trigger_type = $1 ORDER BY outcome`, PushTriggerProfileSet)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if slices.Equal(got, want) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("wake rows = %v, want %v", got, want)
		case <-poll.C:
		}
	}
}

// A panic in one phone's send records that phone's wake failed and the
// account's next phone is still sent. Regression: the only recover wrapped
// the loop over the phones, so a panic left that row and every later
// phone's in flight until the daily retention run.
func TestProfileSetWakePanickingSendDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	registerForSession(t, repo, "live")
	var calls atomic.Int32
	node := profileSetWakeSystem(t, pool, repo, nil, nil)
	node.pushSender.client = &http.Client{Transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			panic("relay transport failed")
		}
		return relayResponse(http.StatusOK, `{"request_id":"r","status":"accepted"}`), nil
	})}
	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("wake answered false")
	}
	waitForWakeRows(t, pool, PushOutcomeDelivered+":", PushOutcomeFailed+":wake_send_unfinished")
	if calls.Load() != 2 {
		t.Fatalf("relay calls = %d, want 2: the next phone must still be sent", calls.Load())
	}
	// The panic released the account's in-flight mark: a later wake starts.
	waitForWakeEnded(t, node)
	wakeHarness{t: t, pool: pool}.pastTheMinute()
	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("wake after the panicking one answered false")
	}
	waitForWakeRows(t, pool, PushOutcomeDelivered+":", PushOutcomeDelivered+":", PushOutcomeDelivered+":",
		PushOutcomeFailed+":wake_send_unfinished")
}

// registerAndroid registers, or with remove removes, one Android phone of
// account 7 through the ordered route's repository call at generation.
func registerAndroid(t *testing.T, repo *PushDeviceRepository, deviceID string, generation int64, remove bool) {
	t.Helper()
	key := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	cmd := AndroidPushCommand{UserID: 7, ProfileID: "default", DeviceID: deviceID, InstallationKey: key, Generation: generation, Remove: remove}
	if !remove {
		cmd.Token, cmd.PushMode, cmd.LoginSessionID = strings.Repeat("a", 64), PushModePrivatePush, "live"
	}
	if _, err := repo.ApplyAndroidPush(t.Context(), cmd, testPushCipher(t)); err != nil {
		t.Fatal(err)
	}
}

// Removing a phone and registering it again does not reset the
// once-a-minute limit. Regression: the limit was read from the account's
// wake attempts, which a device removal deletes with the device, so a
// remove-and-register cycle of three requests allowed another wake every
// time, against the server's shared relay allowance.
func TestProfileSetWakeSurvivesDeviceRemovalDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	h := wakeHarness{t: t, pool: pool}
	ctx := t.Context()
	platforms := []string{PushPlatformApple, PushPlatformAndroid}
	wake := func() (bool, int) {
		t.Helper()
		sendNow, err := repo.EnqueueProfileSetWake(ctx, 7, platforms, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return len(sendNow) > 0, len(sendNow)
	}

	registerAndroid(t, repo, "android-cycle", 1, false)
	if queued, sent := wake(); !queued || sent != 1 || h.attempts() != 1 {
		t.Fatalf("first wake: queued %v, %d sent, %d rows; want true, 1, 1", queued, sent, h.attempts())
	}
	if queued, sent := wake(); queued || sent != 0 || h.attempts() != 1 {
		t.Fatalf("wake inside the minute: queued %v, %d sent, %d rows; want false, 0, 1", queued, sent, h.attempts())
	}
	// Inside the minute no registration is read: with another transaction
	// holding the registrations table, the wake still answers at once.
	// Regression: every edit inside the minute listed and sorted all of the
	// account's registrations before the limit turned it away.
	func() {
		locker, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = locker.Rollback(context.Background()) }()
		if _, err := locker.Exec(ctx, `LOCK TABLE push_devices IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		limited, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if sendNow, err := repo.EnqueueProfileSetWake(limited, 7, platforms, nil, nil); err != nil || len(sendNow) != 0 {
			t.Fatalf("wake inside the minute with the registrations locked: %d queued, error %v; want none and no error", len(sendNow), err)
		}
	}()
	registerAndroid(t, repo, "android-cycle", 2, true)
	if h.attempts() != 0 {
		t.Fatalf("rows after the phone's removal = %d, want 0: its attempts go with it", h.attempts())
	}
	registerAndroid(t, repo, "android-cycle", 3, false)
	if queued, sent := wake(); queued || sent != 0 || h.attempts() != 0 {
		t.Fatalf("wake inside the minute after remove and register: queued %v, %d sent, %d rows; want false, 0, 0", queued, sent, h.attempts())
	}
	h.pastTheMinute()
	if queued, sent := wake(); !queued || sent != 1 || h.attempts() != 1 {
		t.Fatalf("wake after the minute: queued %v, %d sent, %d rows; want true, 1, 1", queued, sent, h.attempts())
	}
}

// A wake reaches at most profileSetWakeMaxPhones of the account's phones:
// those a push was most recently accepted for, then those that registered
// most recently. Regressions: a wake with no bound on its relay calls; one
// that chose the newest registrations, which anyone who can register
// devices on the account could use to keep the person's phones out.
func TestProfileSetWakeCapsPhonesDB(t *testing.T) {
	for _, tc := range []struct {
		name string
		// phones registers this many phones, android-00 first; android-00
		// has the oldest registration.
		phones int
		// delivered phones have had a push accepted, the first listed most
		// recently.
		delivered []string
		woken     []string
		notWoken  []string
	}{
		{name: "the most recently registered when none had a push accepted",
			phones: profileSetWakeMaxPhones + 1, notWoken: []string{"android-00"}},
		{name: "a phone with an accepted push before a never-delivered newer one",
			phones:    profileSetWakeMaxPhones + 1,
			delivered: deviceNames(0, profileSetWakeMaxPhones),
			woken:     []string{"android-00"}, notWoken: []string{fmt.Sprintf("android-%02d", profileSetWakeMaxPhones)}},
		{name: "real phones before a flood of fresh registrations",
			phones:    3 + profileSetWakeMaxPhones,
			delivered: deviceNames(0, 3),
			woken:     deviceNames(0, 3)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, pool := loginSessionPushDB(t)
			ctx := t.Context()
			for i := range tc.phones {
				registerAndroid(t, repo, fmt.Sprintf("android-%02d", i), 1, false)
			}
			if _, err := pool.Exec(ctx, `UPDATE push_devices
				SET last_seen_at = now() - (100 - substring(device_id from 9)::int) * interval '1 minute'`); err != nil {
				t.Fatal(err)
			}
			for i, device := range tc.delivered {
				if _, err := pool.Exec(ctx, `UPDATE push_devices SET last_success_at = now() - $2 * interval '1 second'
					WHERE device_id = $1`, device, i); err != nil {
					t.Fatal(err)
				}
			}
			sendNow, err := repo.EnqueueProfileSetWake(ctx, 7, []string{PushPlatformAndroid}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := pool.Query(ctx, `SELECT d.device_id FROM push_delivery_attempts a
				JOIN push_devices d ON d.id = a.push_device_id ORDER BY d.device_id`)
			if err != nil {
				t.Fatal(err)
			}
			woken, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatal(err)
			}
			if len(sendNow) != profileSetWakeMaxPhones || len(woken) != profileSetWakeMaxPhones {
				t.Fatalf("woke %d phones (%d to send), want %d", len(woken), len(sendNow), profileSetWakeMaxPhones)
			}
			for _, device := range tc.woken {
				if !slices.Contains(woken, device) {
					t.Errorf("%s not woken; woke %v", device, woken)
				}
			}
			for _, device := range tc.notWoken {
				if slices.Contains(woken, device) {
					t.Errorf("%s woken; woke %v", device, woken)
				}
			}
		})
	}
}

// deviceNames is android-<from> to android-<to-1>.
func deviceNames(from, to int) []string {
	names := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		names = append(names, fmt.Sprintf("android-%02d", i))
	}
	return names
}

// waitForWakeEnded waits until node has no wake of account 7 in flight.
func waitForWakeEnded(t *testing.T, node *System) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for node.profileSetWakeInFlight(7) {
		select {
		case <-deadline:
			t.Fatal("the wake never ended")
		case <-poll.C:
		}
	}
}

// A dead registration at the front of the order costs the account's other
// phones nothing: they are sent beside it and delivered, and it alone is
// recorded out of time, within the budget. Regression: sent one after
// another, one registration whose relay call never answered used the whole
// budget, and against a relay that times out every request a wake reached
// two phones of twenty.
func TestProfileSetWakeSlowFirstPhoneDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	registerForSession(t, repo, "live")
	registerAndroid(t, repo, "android-dead", 1, false)
	// android-dead had the most recent accepted push, so it is sent first.
	var dead string
	if err := pool.QueryRow(t.Context(), `UPDATE push_devices SET last_success_at = now()
		WHERE device_id = 'android-dead' RETURNING server_device_id`).Scan(&dead); err != nil {
		t.Fatal(err)
	}
	defer func(previous time.Duration) { profileSetWakeBudget = previous }(profileSetWakeBudget)
	profileSetWakeBudget = 500 * time.Millisecond

	node := profileSetWakeSystem(t, pool, repo, nil, nil)
	node.pushSender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode relay body: %v", err)
		}
		if body["server_device_id"] == dead {
			<-req.Context().Done() // this registration's relay call never answers
			return nil, req.Context().Err()
		}
		return relayResponse(http.StatusOK, `{"request_id":"r","status":"accepted"}`), nil
	})}
	started := time.Now()
	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("wake answered false")
	}
	waitForWakeRows(t, pool, PushOutcomeDelivered+":", PushOutcomeDelivered+":", PushOutcomeFailed+":wake_out_of_time")
	waitForWakeEnded(t, node)
	if elapsed := time.Since(started); elapsed > profileSetWakeBudget+profileSetFinalizeTimeout {
		t.Fatalf("the wake took %v, want at most its budget %v plus the final write's %v", elapsed, profileSetWakeBudget, profileSetFinalizeTimeout)
	}
}

// A wake with more phones than profileSetWakeConcurrency reaches every one
// of them, with never more than that many relay calls in flight at once.
// Regression: an unbounded fan-out of one wake's sends against the relay's
// shared allowance.
func TestProfileSetWakeBoundsSendsInFlightDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	phones := 2*profileSetWakeConcurrency + 1
	for i := range phones {
		registerAndroid(t, repo, fmt.Sprintf("android-%02d", i), 1, false)
	}
	var inFlight, most, calls atomic.Int32
	var hold atomic.Bool
	node := profileSetWakeSystem(t, pool, repo, nil, nil)
	node.pushSender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		now := inFlight.Add(1)
		defer inFlight.Add(-1)
		calls.Add(1)
		for seen := most.Load(); now > seen && !most.CompareAndSwap(seen, now); seen = most.Load() {
		}
		if hold.Load() {
			<-req.Context().Done() // held until the wake's budget ends
			return nil, req.Context().Err()
		}
		return relayResponse(http.StatusOK, `{"request_id":"r","status":"accepted"}`), nil
	})}

	// The relay answering: every phone is reached, within the bound.
	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("wake answered false")
	}
	delivered := make([]string, phones)
	for i := range delivered {
		delivered[i] = PushOutcomeDelivered + ":"
	}
	waitForWakeRows(t, pool, delivered...)
	if got := most.Load(); got > profileSetWakeConcurrency {
		t.Fatalf("%d relay calls in flight at once while the relay answered, want at most %d", got, profileSetWakeConcurrency)
	}

	// The relay holding every call until the budget ends: exactly the bound
	// is in flight, and the phones after it are never started.
	waitForWakeEnded(t, node)
	defer func(previous time.Duration) { profileSetWakeBudget = previous }(profileSetWakeBudget)
	profileSetWakeBudget = time.Second
	hold.Store(true)
	most.Store(0)
	calls.Store(0)
	if _, err := pool.Exec(t.Context(), `DELETE FROM push_delivery_attempts`); err != nil {
		t.Fatal(err)
	}
	wakeHarness{t: t, pool: pool}.pastTheMinute()
	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("held wake answered false")
	}
	waitForWakeEnded(t, node)
	if got := most.Load(); got != profileSetWakeConcurrency || calls.Load() != profileSetWakeConcurrency {
		t.Fatalf("held wake: %d relay calls, at most %d at once; want %d of each", calls.Load(), got, profileSetWakeConcurrency)
	}
}

// The in-flight guard is taken only once the edit is known to have
// committed: a request whose edits rolled back never takes it, so it can
// neither be refused by it nor hold it against a committed edit. Regression:
// the guard was claimed before the commit check, so a rolled-back edit made
// while a wake was in flight answered differently from one made while none
// was, and held the mark for its whole enqueue.
func TestProfileSetWakeGuardFollowsTheCommitCheckDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	if _, err := pool.Exec(t.Context(), `
		CREATE TABLE profile_set_edits (LIKE public.profile_set_edits INCLUDING ALL);
		INSERT INTO profile_set_edits (user_id, sync_id, field, by, actor_user_id)
		VALUES (7, gen_random_uuid(), 'name', 'account', 7)`); err != nil {
		t.Fatal(err)
	}
	registerForSession(t, repo, "live")
	var committed int64
	if err := pool.QueryRow(t.Context(), `SELECT revision FROM profile_set_edits`).Scan(&committed); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		revision  int64
		wantClaim bool
		wantSent  int
	}{
		{name: "rolled back", revision: committed + 1000, wantClaim: false, wantSent: 0},
		{name: "committed", revision: committed, wantClaim: true, wantSent: 2},
	} {
		claimed := false
		sendNow, err := repo.EnqueueProfileSetWake(t.Context(), 7, []string{PushPlatformApple, PushPlatformAndroid}, []int64{tc.revision},
			func() bool { claimed = true; return true })
		if err != nil {
			t.Fatal(err)
		}
		if claimed != tc.wantClaim || len(sendNow) != tc.wantSent {
			t.Errorf("%s edit: guard claimed %v, %d queued; want %v and %d", tc.name, claimed, len(sendNow), tc.wantClaim, tc.wantSent)
		}
	}
}

// A wake ends within its budget plus one bounded write, even when the rows
// it records are locked: the phones still in flight are recorded in one
// statement on one deadline, and the account's in-flight mark is released
// after it. Regression: each phone got its own failure write with its own
// deadline, one after another, so a wake with locked rows held the mark for
// 15 seconds with three phones, and an edit made after the minute was
// refused by the guard.
func TestProfileSetWakeTailIsOneBoundedWriteDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	registerForSession(t, repo, "live")
	registerAndroid(t, repo, "android-extra", 1, false)
	defer func(previous time.Duration) { profileSetWakeBudget = previous }(profileSetWakeBudget)
	profileSetWakeBudget = 300 * time.Millisecond

	node := profileSetWakeSystem(t, pool, repo, nil, nil)
	node.pushSender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done() // the relay never answers
		return nil, req.Context().Err()
	})}
	started := time.Now()
	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("wake answered false")
	}
	// Another transaction holds the wake's rows, so no write to them can
	// finish until it ends.
	locker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locker.Rollback(context.Background()) }()
	if _, err := locker.Exec(t.Context(), `SELECT id FROM push_delivery_attempts WHERE trigger_type = $1 FOR UPDATE`,
		PushTriggerProfileSet); err != nil {
		t.Fatal(err)
	}
	waitForWakeEnded(t, node)
	if elapsed, bound := time.Since(started), profileSetWakeBudget+profileSetFinalizeTimeout; elapsed > bound+time.Second {
		t.Fatalf("the wake held its mark for %v, want at most its budget plus its tail, %v", elapsed, bound)
	}
}

// A send waiting for the relay credential's lock gives up when the wake's
// budget ends, so a wake ends on time while another send holds the lock
// across a slow credential request. Regression: the lock was a mutex, whose
// wait no context ends, so a wake's send could outlive its budget there.
func TestProfileSetWakeGivesUpWaitingForTheCredentialLockDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	registerForSession(t, repo, "live")
	defer func(previous time.Duration) { profileSetWakeBudget = previous }(profileSetWakeBudget)
	profileSetWakeBudget = 300 * time.Millisecond

	node := profileSetWakeSystem(t, pool, repo, nil, nil)
	node.pushSender.renewLock <- struct{}{} // another send holds the credential lock
	defer node.pushSender.unlockRenew()
	started := time.Now()
	if !node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("wake answered false")
	}
	outOfTime := PushOutcomeFailed + ":wake_out_of_time"
	waitForWakeRows(t, pool, outOfTime, outOfTime)
	waitForWakeEnded(t, node)
	if elapsed, bound := time.Since(started), profileSetWakeBudget+profileSetFinalizeTimeout; elapsed > bound {
		t.Fatalf("the wake took %v waiting for the credential lock, want at most %v", elapsed, bound)
	}
}

// A wake whose queueing fails answers false and leaves nothing behind: no
// attempt, no recorded wake time, and no in-flight mark. Regression: an
// error answering true, telling the caller a phone was told that was not.
func TestProfileSetWakeFailingToQueueAnswersFalseDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	registerForSession(t, repo, "live")
	// The attempts insert fails, after the wake's time is recorded in the
	// same transaction.
	if _, err := pool.Exec(t.Context(), `
		CREATE FUNCTION refuse_wake_attempts() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'refused by the test'; END $$;
		CREATE TRIGGER refuse_wake_attempts BEFORE INSERT ON push_delivery_attempts
		FOR EACH ROW EXECUTE FUNCTION refuse_wake_attempts()`); err != nil {
		t.Fatal(err)
	}
	h := wakeHarness{t: t, pool: pool}
	node := profileSetWakeSystem(t, pool, repo, nil, nil)
	if node.WakeProfileSetDevices(t.Context(), 7) {
		t.Fatal("a wake that failed to queue answered true")
	}
	var recorded int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM push_profile_set_wakes`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if h.attempts() != 0 || recorded != 0 || node.profileSetWakeInFlight(7) {
		t.Fatalf("after a failed queue: %d attempts, %d wake times, in flight %v; want none",
			h.attempts(), recorded, node.profileSetWakeInFlight(7))
	}
}
