package notifications

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/emailalerts"
)

// TestEmailAlertsSendCycle runs an added recipient through its lifecycle: a
// first run only sets its starting point, a new episode goes out in one
// instant email, nothing is sent twice, a rewind resends, and a failed send
// backs off without losing the item.
func TestEmailAlertsSendCycle(t *testing.T) {
	pool := schemaDB(t, "email_alerts_")
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE release_events (LIKE public.release_events INCLUDING ALL);
		CREATE TABLE notification_email_alert_settings (LIKE public.notification_email_alert_settings INCLUDING ALL);
		CREATE TABLE notification_email_recipients (LIKE public.notification_email_recipients INCLUDING ALL);
		CREATE TABLE notification_email_prefs (LIKE public.notification_email_prefs INCLUDING ALL);
		CREATE TABLE notification_email_alert_state (LIKE public.notification_email_alert_state INCLUDING ALL);
		CREATE TABLE notification_email_alert_log (LIKE public.notification_email_alert_log INCLUDING ALL)`); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	c := seedAccessCatalog(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE episodes SET overview = 'Mara reaches the lantern room.' WHERE content_id = $1`, c.episode); err != nil {
		t.Fatal(err)
	}

	sender := &fakeVerificationSender{enabled: true}
	settings := NewSettings(mapSettingReader{"server.public_url": "https://silo.example"})
	alerts := newEmailAlerts(pool, settings, sender, nil, nil)
	s := emailalerts.DefaultSettings()
	s.Delivery.SettleMinutes = 0
	s.Delivery.InstantGapMinutes = 0
	if _, err := alerts.SaveSettings(ctx, s); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	rec, err := alerts.SaveAdminRecipient(ctx, EmailAlertRecipient{Name: "Alex", Email: "Alex <alex@example.test>", Enabled: true, Mode: emailalerts.ModeInstant})
	if err != nil {
		t.Fatalf("add recipient: %v", err)
	}
	if rec.Email != "alex@example.test" || rec.Name != "Alex" || rec.ManageToken == "" {
		t.Fatalf("recipient = %+v", rec)
	}

	clock := time.Now()
	alerts.now = func() time.Time { return clock }
	if r := alerts.RunDue(ctx, "test"); r.Sent != 0 || len(r.Errors) > 0 {
		t.Fatalf("first run = %+v, want only the starting point recorded", r)
	}

	clock = clock.Add(time.Minute)
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_events (id, library_id, series_id, episode_id, season_number, episode_number,
			episode_key, available_at, dedupe_key, created_at)
		VALUES ('ev-1', $1, $2, $3, 1, 2, $4, now(), 'ev-1', $5)`,
		c.library, c.series, c.episode, EpisodeKey(1, 2), clock.Add(-30*time.Second)); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if r := alerts.RunDue(ctx, "test"); r.Sent != 1 || len(r.Errors) > 0 {
		t.Fatalf("run = %+v, want one email", r)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d emails", len(sender.sent))
	}
	msg := sender.sent[0]
	for _, want := range []string{"Series", "E2", "Mara reaches the lantern room.", "/api/v2/notifications/email/manage?token=" + rec.ManageToken} {
		if !strings.Contains(msg.HTMLBody, want) {
			t.Errorf("email is missing %q", want)
		}
	}
	if msg.Headers["List-Unsubscribe"] == "" {
		t.Error("no List-Unsubscribe header")
	}

	clock = clock.Add(time.Minute)
	if r := alerts.RunDue(ctx, "test"); r.Sent != 0 {
		t.Fatalf("second run sent %d, want nothing new", r.Sent)
	}

	if err := alerts.Rewind(ctx, rec.Key(), 1); err != nil {
		t.Fatal(err)
	}
	sender.err = errors.New("smtp down")
	clock = clock.Add(time.Minute)
	if r := alerts.RunDue(ctx, "test"); len(r.Errors) != 1 {
		t.Fatalf("failing run = %+v, want one error", r)
	}
	st, _, err := alerts.repo.State(ctx, rec.Key(), emailalerts.LegInstant)
	if err != nil || st.Failures != 1 || st.NextAttemptAt == nil {
		t.Fatalf("state after failure = %+v (%v)", st, err)
	}

	sender.err = nil
	clock = clock.Add(5 * time.Minute)
	if r := alerts.RunDue(ctx, "test"); r.Sent != 1 {
		t.Fatalf("retry = %+v, want the rewound episode resent", r)
	}
	log, err := alerts.repo.Log(ctx, 10)
	if err != nil || len(log) != 3 || log[1].Error == "" {
		t.Fatalf("log = %+v (%v), want two sends and one failure", log, err)
	}
}

// Unsubscribing through the manage token switches an added recipient off.
func TestEmailAlertsUnsubscribe(t *testing.T) {
	pool := schemaDB(t, "email_alerts_unsub_")
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE notification_email_recipients (LIKE public.notification_email_recipients INCLUDING ALL);
		CREATE TABLE notification_email_prefs (LIKE public.notification_email_prefs INCLUDING ALL);
		CREATE TABLE notification_email_alert_state (LIKE public.notification_email_alert_state INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	alerts := newEmailAlerts(pool, NewSettings(mapSettingReader{}), &fakeVerificationSender{enabled: true}, nil, nil)
	rec, err := alerts.SaveAdminRecipient(ctx, EmailAlertRecipient{Email: "sam@example.test", Enabled: true, Mode: emailalerts.ModeWeekly})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alerts.SaveAdminRecipient(ctx, EmailAlertRecipient{Email: "SAM@example.test", Enabled: true, Mode: emailalerts.ModeDaily}); !errors.Is(err, ErrEmailRecipientExists) {
		t.Fatalf("duplicate address: err = %v", err)
	}
	ok, err := alerts.Unsubscribe(ctx, rec.ManageToken)
	if err != nil || !ok {
		t.Fatalf("unsubscribe = %t, %v", ok, err)
	}
	got, err := alerts.repo.AdminRecipient(ctx, rec.ID)
	if err != nil || got.Enabled || got.Mode != emailalerts.ModeWeekly {
		t.Fatalf("after unsubscribe = %+v (%v), want disabled with its mode kept", got, err)
	}
	if ok, _ := alerts.Unsubscribe(ctx, "not-a-token"); ok {
		t.Fatal("unknown token unsubscribed something")
	}
}
