package notifications

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5/pgxpool"
)

// loginSessionWebPushDB returns a repository on a schema with its own copies of
// the web push and login session tables, holding one session in each state a
// subscription can find its login in.
func loginSessionWebPushDB(t *testing.T) (*WebPushRepository, *pgxpool.Pool) {
	t.Helper()
	pool := schemaDB(t, "web_push_login_session_")
	if _, err := pool.Exec(t.Context(), `
		CREATE TABLE auth_sessions (LIKE public.auth_sessions INCLUDING ALL);
		CREATE TABLE notification_deliveries (LIKE public.notification_deliveries INCLUDING ALL);
		CREATE TABLE web_push_subscriptions (LIKE public.web_push_subscriptions INCLUDING ALL);
		CREATE TABLE web_push_delivery_attempts (LIKE public.web_push_delivery_attempts INCLUDING ALL);
		ALTER TABLE web_push_delivery_attempts ADD FOREIGN KEY (subscription_id) REFERENCES web_push_subscriptions(id) ON DELETE CASCADE;
		INSERT INTO auth_sessions (id, user_id, expires_at, revoked_at) VALUES
			('live', 7, now() + interval '1 hour', NULL),
			('other-live', 8, now() + interval '1 hour', NULL),
			('revoked', 7, now() + interval '1 hour', now()),
			('expired', 7, now() - interval '1 hour', NULL)`); err != nil {
		t.Fatal(err)
	}
	return NewWebPushRepository(pool), pool
}

// subscribeBrowser registers one browser, named by endpoint suffix, for the
// session.
func subscribeBrowser(t *testing.T, repo *WebPushRepository, name string, user int, profile, session string) *WebPushSubscription {
	t.Helper()
	sub, err := repo.Upsert(t.Context(), WebPushSubscription{
		ID: "sub-" + name + "-" + session, UserID: user, ProfileID: profile,
		Endpoint: "https://push.example.test/" + name, P256dh: "key", Auth: "auth",
		LoginSessionID: session,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// Web push follows the login that registered the browser, whichever way that
// login ended: fan-out skips the browser and retention removes its keys. A
// subscription with no known session keeps delivering, and a session a restored
// backup no longer holds counts as ended.
func TestWebPushStopsWhenTheRegisteringLoginEndsDB(t *testing.T) {
	repo, pool := loginSessionWebPushDB(t)
	ctx := t.Context()
	want := map[string]bool{"live": true, "": true, "revoked": false, "expired": false, "deleted": false}
	for session := range want {
		subscribeBrowser(t, repo, "browser-"+session, 7, "default", session)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := repo.ListEnabledByProfiles(ctx, tx, []profileKey{{7, "default"}})
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	picked := map[string]bool{}
	for _, sub := range listed[profileKey{7, "default"}] {
		picked[sub.LoginSessionID] = true
	}
	for session, delivered := range want {
		if picked[session] != delivered {
			t.Errorf("fan-out picked the browser of session %q = %v, want %v", session, picked[session], delivered)
		}
	}

	if deleted, err := repo.DeleteEndedLoginSubscriptions(ctx); err != nil || deleted != 3 {
		t.Fatalf("retention deleted %d subscriptions, err=%v, want 3", deleted, err)
	}
	kept, err := repo.ListByProfile(ctx, 7, "default")
	if err != nil {
		t.Fatal(err)
	}
	remaining := map[string]bool{}
	for _, sub := range kept {
		remaining[sub.LoginSessionID] = true
	}
	for session, delivered := range want {
		if remaining[session] != delivered {
			t.Errorf("retention kept the browser of session %q = %v, want %v", session, remaining[session], delivered)
		}
	}
}

// Subscribing again from a browser whose login ended brings it back on the new
// login, including when the endpoint moves to another account, whose queued
// attempts must not be delivered. A caller with no login session leaves none on
// the row rather than the previous caller's.
func TestWebPushResubscribeRecordsTheNewLoginDB(t *testing.T) {
	repo, _ := loginSessionWebPushDB(t)
	ctx := t.Context()
	enabled := func(user int, profile string) []WebPushSubscription {
		t.Helper()
		tx, err := repo.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		listed, err := repo.ListEnabledByProfiles(ctx, tx, []profileKey{{user, profile}})
		if err != nil {
			t.Fatal(err)
		}
		return listed[profileKey{user, profile}]
	}

	first := subscribeBrowser(t, repo, "browser", 7, "default", "revoked")
	if got := enabled(7, "default"); len(got) != 0 {
		t.Fatalf("fan-out after the login ended = %+v, want none", got)
	}

	again := subscribeBrowser(t, repo, "browser", 7, "default", "live")
	if again.ID != first.ID || again.LoginSessionID != "live" || len(enabled(7, "default")) != 1 {
		t.Fatalf("same account after a new sign-in = %+v, want the same row on the live login", again)
	}

	moved := subscribeBrowser(t, repo, "browser", 8, "default", "other-live")
	if moved.ID != first.ID || moved.UserID != 8 || moved.LoginSessionID != "other-live" {
		t.Fatalf("moved to another account = %+v", moved)
	}
	if got := enabled(7, "default"); len(got) != 0 {
		t.Fatalf("previous account still fans out to the browser: %+v", got)
	}
	if got := enabled(8, "default"); len(got) != 1 {
		t.Fatalf("new account fan-out = %+v, want the browser", got)
	}

	if keyed := subscribeBrowser(t, repo, "browser", 8, "default", ""); keyed.LoginSessionID != "" {
		t.Fatalf("subscribing with no login session kept %q", keyed.LoginSessionID)
	}
}

// A push queued while the login was live must not leave once it has ended: the
// retry schedule can hold an attempt for half an hour after the revoke.
func TestQueuedWebPushStopsWhenTheRegisteringLoginEndsDB(t *testing.T) {
	repo, pool := loginSessionWebPushDB(t)
	ctx := t.Context()
	var sent atomic.Int32
	pushService := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(pushService.Close)

	vapidPrivate, vapidPublic, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	keypair, err := json.Marshal(vapidKeypair{Public: vapidPublic, Private: vapidPrivate})
	if err != nil {
		t.Fatal(err)
	}
	settings := NewSettings(mapSettingReader{SettingWebPushVAPIDKeypair: string(keypair)})
	sender := newWebPushSender(repo, NewDeliveryRepository(pool), newWebPushService(repo, settings, nil), settings)
	sender.client = pushService.Client()

	browserKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := repo.Upsert(ctx, WebPushSubscription{
		ID: "sub", UserID: 7, ProfileID: "default", Endpoint: pushService.URL + "/browser",
		P256dh:         base64.RawURLEncoding.EncodeToString(browserKey.PublicKey().Bytes()),
		Auth:           base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		LoginSessionID: "live",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_deliveries (id, user_id, profile_id, type, reason_flags)
		VALUES ('delivery', 7, 'default', $1, '{}'),
		       ('other-delivery', 7, 'default', $1, '{}')`, DeliveryTypeRequestDeclined); err != nil {
		t.Fatal(err)
	}
	queue := func(id, delivery string) DeliveryAttempt {
		t.Helper()
		queued := DeliveryAttempt{ID: id, NotificationDeliveryID: delivery, TargetID: sub.ID}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := repo.EnqueueAttempts(ctx, tx, []DeliveryAttempt{queued}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return queued
	}
	outcome := func(id string) (string, string) {
		t.Helper()
		var state string
		var message *string
		if err := pool.QueryRow(ctx, `SELECT outcome, failure_message FROM web_push_delivery_attempts WHERE id = $1`, id).Scan(&state, &message); err != nil {
			t.Fatal(err)
		}
		if message == nil {
			return state, ""
		}
		return state, *message
	}

	delivered := queue("delivered", "delivery")
	stopped := queue("stopped", "other-delivery")

	sender.processAttempt(ctx, delivered)
	if state, message := outcome("delivered"); state != WebhookOutcomeDelivered || sent.Load() != 1 {
		t.Fatalf("attempt with a live login = %s (%s) after %d sends, want delivered after 1", state, message, sent.Load())
	}

	if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at = now() WHERE id = 'live'`); err != nil {
		t.Fatal(err)
	}
	sender.processAttempt(ctx, stopped)
	if state, message := outcome("stopped"); state != WebhookOutcomeFailed || message != "login session ended" {
		t.Fatalf("attempt after the revoke = %s (%s), want failed: login session ended", state, message)
	}
	if sent.Load() != 1 {
		t.Fatalf("sends after the revoke = %d, want 1", sent.Load())
	}
}
