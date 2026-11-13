package notifications

import (
	"regexp"
	"testing"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// A verification link opens a standalone page, so the Settings tab that asked
// for it would show the address as waiting until reloaded. Verifying publishes
// notification.email_changed to the profile, once (#3406).
func TestVerifyEmailTokenTellsTheProfilesTabsDB(t *testing.T) {
	repo, d, sender, _ := dispatchFixture(t)
	ctx := t.Context()
	in := emailOutboxIntent()
	if _, err := repo.QueueVerification(ctx, in, d.cipher); err != nil {
		t.Fatal(err)
	}
	d.runPass(ctx)
	sent := sender.messages()
	if len(sent) != 1 {
		t.Fatalf("sent %d verification emails, want 1", len(sent))
	}
	match := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(sent[0].TextBody)
	if len(match) != 2 {
		t.Fatal("message carries no link")
	}

	hub := evt.NewHub("test", nil)
	events, stop := hub.Subscribe()
	defer stop()
	system := &System{EmailPrefs: repo, events: hub}

	if outcome, err := system.VerifyEmailToken(ctx, match[1]); err != nil || outcome != EmailVerifyOK {
		t.Fatalf("VerifyEmailToken = %v, %v; want verified", outcome, err)
	}
	select {
	case env := <-events:
		if env.Channel != evt.ChannelNotifications || env.Event != EventNotificationEmailChanged ||
			env.UserID != in.UserID || env.ProfileID != in.ProfileID {
			t.Fatalf("event = %s/%s for user %d profile %q, want %s/%s for user %d profile %q",
				env.Channel, env.Event, env.UserID, env.ProfileID,
				evt.ChannelNotifications, EventNotificationEmailChanged, in.UserID, in.ProfileID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event after the address was verified")
	}

	// The link is single-use: clicking it again changes nothing and tells no one.
	if outcome, err := system.VerifyEmailToken(ctx, match[1]); err != nil || outcome != EmailVerifyInvalid {
		t.Fatalf("second VerifyEmailToken = %v, %v; want invalid", outcome, err)
	}
	select {
	case env := <-events:
		t.Fatalf("a used link published %s/%s", env.Channel, env.Event)
	case <-time.After(200 * time.Millisecond):
	}
}
