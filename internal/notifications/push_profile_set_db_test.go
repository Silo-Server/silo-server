package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

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
	apple, android := registerForSession(t, repo, "live")
	relayed := make(chan relayedWake, 16)
	nodeA := profileSetWakeSystem(t, pool, repo, nil, relayed)
	nodeB := profileSetWakeSystem(t, pool, repo, nil, relayed)

	attempts := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_delivery_attempts WHERE trigger_type = $1`,
			PushTriggerProfileSet).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// pastTheMinute ages every wake past the debounce interval.
	pastTheMinute := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE push_delivery_attempts SET created_at = created_at - interval '61 seconds'`); err != nil {
			t.Fatal(err)
		}
	}
	receive := func(n int) []relayedWake {
		t.Helper()
		got := make([]relayedWake, 0, n)
		deadline := time.After(30 * time.Second)
		for len(got) < n {
			select {
			case wake := <-relayed:
				got = append(got, wake)
			case <-deadline:
				t.Fatalf("relay got %d wakes, want %d", len(got), n)
			}
		}
		return got
	}

	// Two servers recording at the same moment queue one wake per phone.
	var wg sync.WaitGroup
	answers := make([]bool, 2)
	for i, node := range []*System{nodeA, nodeB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = node.WakeProfileSetDevices(ctx, 7)
		}()
	}
	wg.Wait()
	if !answers[0] || !answers[1] || attempts() != 2 {
		t.Fatalf("concurrent wakes answered %v with %d attempts, want true, true and 2", answers, attempts())
	}
	paths := map[string]bool{}
	for _, wake := range receive(2) {
		paths[wake.path] = true
		if wake.body["mode"] != relayModeBackgroundWake || wake.body["collapse_id"] != profileSetCollapseID ||
			wake.body["delivery_id"] != wake.idempotencyKey || wake.idempotencyKey == "" {
			t.Errorf("relay request to %s with key %q = mode %v, collapse_id %v, delivery_id %v; want %s, %s and the attempt ID",
				wake.path, wake.idempotencyKey, wake.body["mode"], wake.body["collapse_id"], wake.body["delivery_id"],
				relayModeBackgroundWake, profileSetCollapseID)
		}
	}
	if !paths[relayAppleSendPath] || !paths[relayFcmSendPath] {
		t.Fatalf("relay paths = %v, want the Apple (%s) and Android (%s) phones", paths, apple.id, android.id)
	}

	// A second edit inside the minute sends nothing more and still tells the
	// owner a wake is on its way.
	if !nodeB.WakeProfileSetDevices(ctx, 7) || attempts() != 2 {
		t.Fatalf("wake inside the minute: %d attempts, want 2 and a true answer", attempts())
	}

	pastTheMinute()
	if !nodeA.WakeProfileSetDevices(ctx, 7) || attempts() != 4 {
		t.Fatalf("wake after the minute: %d attempts, want 4 and a true answer", attempts())
	}
	receive(2)

	// A request whose edit rolled back wakes nothing; one whose edit
	// committed does.
	pastTheMinute()
	for _, commit := range []bool{false, true} {
		reqCtx, req := profileedits.WithRequest(ctx)
		req.SetActor(7, false, nil)
		tx, err := pool.Begin(reqCtx)
		if err != nil {
			t.Fatal(err)
		}
		if err := profileedits.RecordFields(reqCtx, tx, 7, "default", []string{profileedits.FieldName}); err != nil {
			t.Fatal(err)
		}
		if commit {
			err = tx.Commit(reqCtx)
		} else {
			err = tx.Rollback(reqCtx)
		}
		if err != nil {
			t.Fatal(err)
		}
		want := map[bool]int{false: 4, true: 6}[commit]
		if got := nodeA.WakeProfileSetDevices(reqCtx, 7); got != commit || attempts() != want {
			t.Fatalf("wake after an edit that committed=%v answered %v with %d attempts, want %v and %d", commit, got, attempts(), commit, want)
		}
	}
	receive(2)

	// No registered phone, or push off for both platforms: nothing queued,
	// nothing recorded, no error.
	pastTheMinute()
	if nodeA.WakeProfileSetDevices(ctx, 8) || attempts() != 6 {
		t.Fatalf("wake of an account with no phone: %d attempts, want 6 and a false answer", attempts())
	}
	off := profileSetWakeSystem(t, pool, repo, map[string]string{
		SettingApplePushDeliveryEnabled: "false", SettingAndroidPushDeliveryEnabled: "false",
	}, relayed)
	if off.WakeProfileSetDevices(ctx, 7) || attempts() != 6 {
		t.Fatalf("wake with push off: %d attempts, want 6 and a false answer", attempts())
	}
	select {
	case wake := <-relayed:
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
