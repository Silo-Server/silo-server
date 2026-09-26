package apiv2

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeNotificationChannels struct {
	user          int
	profile, mode string
	writes        int
	err           error
}

func (f *fakeNotificationChannels) EmailPreferences(_ context.Context, user int, profile string) (notifications.EmailPreferencesState, error) {
	f.user, f.profile = user, profile
	return notifications.EmailPreferencesState{Mode: "off", CustomEmail: "verified@example.test", PendingEmail: "", IsChild: true}, nil
}
func (f *fakeNotificationChannels) SetEmailMode(_ context.Context, user int, profile, mode string) error {
	f.user, f.profile, f.mode = user, profile, mode
	f.writes++
	return f.err
}
func (f *fakeNotificationChannels) DiscordPrefsFor(_ context.Context, user int) (notifications.DiscordPrefs, error) {
	f.user = user
	return notifications.DiscordPrefs{UserID: user, DiscordUserID: "opaque-secret-id", DiscordUsername: "Example", Mode: "off"}, nil
}
func (f *fakeNotificationChannels) SetDiscordMode(_ context.Context, user int, mode string) error {
	f.user, f.mode = user, mode
	f.writes++
	return f.err
}

func TestNotificationChannelAuthority(t *testing.T) {
	fake := new(fakeNotificationChannels)
	deps := pilotDeps(nil, nil)
	deps.NotificationChannels = fake
	h := NewHandler(deps)
	emailPath := Prefix + "/notifications/email-preferences"
	discordPath := Prefix + "/notifications/discord-preferences"
	rec := do(t, h, http.MethodGet, emailPath, "", bearer(memberToken))
	if rec.Code != 422 {
		t.Fatalf("profileless email: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, emailPath, "", profileOwner())
	if rec.Code != 200 || fake.user != 1 || fake.profile != "p-owner" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, discordPath, "", bearer(memberToken))
	if rec.Code != 200 || fake.user != 1 {
		t.Fatalf("account Discord: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, discordPath, "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	for _, path := range []string{emailPath, discordPath} {
		rec = do(t, h, http.MethodPut, path, `{"mode":"per_episode_and_digest"}`, profileOwner())
		if rec.Code != 200 || fake.mode != "per_episode_and_digest" {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		before := fake.writes
		for _, body := range []string{`{}`, `{"mode":null}`, `{"mode":"unknown"}`, `{"mode":"off","custom_email":"unverified@example.test"}`} {
			requireProblem(t, do(t, h, http.MethodPut, path, body, profileOwner()), TypeValidationFailed)
		}
		if fake.writes != before {
			t.Fatal("invalid mode dispatched")
		}
	}
	for _, err := range []error{notifications.ErrEmailNoAddress, notifications.ErrEmailModeNotAllowed, notifications.ErrDiscordNotLinked, notifications.ErrDiscordModeNotAllowed} {
		require := notificationChannelProblem(fmt.Errorf("wrapped: %w", err))
		if require.Status != 422 {
			t.Fatalf("wrong domain error: %v", require)
		}
	}
}

func notificationChannelFixtureCases() []fixtureCase {
	cases := []fixtureCase{
		{name: "notification_email_preferences", operationID: "getNotificationEmailPreferences", method: "GET", path: Prefix + "/notifications/email-preferences", headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationEmailPreferences", scenario: "A child profile cannot edit its notification email address."},
		{name: "notification_email_mode", operationID: "updateNotificationEmailPreferences", method: "PUT", path: Prefix + "/notifications/email-preferences", body: `{"mode":"off"}`, headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationEmailPreferences", scenario: "The existing delivery mode setter is invoked once."},
		{name: "notification_discord_preferences", operationID: "getNotificationDiscordPreferences", method: "GET", path: Prefix + "/notifications/discord-preferences", headers: bearer(memberToken), status: 200, schema: "#/components/schemas/NotificationDiscordPreferences", scenario: "Discord preferences belong to the login account without requiring a selected profile."},
		{name: "notification_discord_mode", operationID: "updateNotificationDiscordPreferences", method: "PUT", path: Prefix + "/notifications/discord-preferences", body: `{"mode":"off"}`, headers: bearer(memberToken), status: 200, schema: "#/components/schemas/NotificationDiscordPreferences", scenario: "A mode mutation reports account state without leaking the linked identity token."},
	}
	for i := range cases {
		cases[i].assertHeaders = []string{"Content-Type", "Cache-Control"}
	}
	return cases
}

func (f *fakeNotificationChannels) UnlinkDiscord(_ context.Context, user int) error {
	f.user = user
	f.writes++
	return f.err
}
func TestNotificationDiscordUnlink(t *testing.T) {
	f := new(fakeNotificationChannels)
	deps := pilotDeps(nil, nil)
	deps.NotificationChannels = f
	h := NewHandler(deps)
	path := Prefix + "/notifications/discord-link"
	for _, headers := range []map[string]string{bearer(memberToken), profileOwner()} {
		rec := do(t, h, http.MethodDelete, path, "", headers)
		if rec.Code != 204 || rec.Body.Len() != 0 || f.user != 1 {
			t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
		}
	}
	requireProblem(t, do(t, h, http.MethodDelete, path, "", nil), TypeAuthenticationRequired)
	if f.writes != 2 {
		t.Fatal("unauthorized dispatch")
	}
	f.err = fmt.Errorf("storage failure")
	requireProblem(t, do(t, h, http.MethodDelete, path, "", bearer(memberToken)), TypeInternalError)
	deps.NotificationChannels = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodDelete, path, "", bearer(memberToken)), TypeDependencyUnavailable)
}

func (f *fakeNotificationChannels) ClearEmailAddress(_ context.Context, user int, profile string) error {
	f.user, f.profile = user, profile
	f.writes++
	return f.err
}
func TestNotificationEmailClearAddress(t *testing.T) {
	f := new(fakeNotificationChannels)
	deps := pilotDeps(nil, nil)
	deps.NotificationChannels = f
	h := NewHandler(deps)
	path := Prefix + "/notifications/email-preferences/address"
	rec := do(t, h, http.MethodDelete, path, "", profileOwner())
	if rec.Code != 200 || f.user != 1 || f.profile != "p-owner" || f.writes != 1 {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	requireProblem(t, do(t, h, http.MethodDelete, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodDelete, path, "", bearer(memberToken)), TypeValidationFailed)
	if f.writes != 1 {
		t.Fatal("unauthorized dispatch")
	}
	f.err = notifications.ErrEmailChildProfile
	requireProblem(t, do(t, h, http.MethodDelete, path, "", profileOwner()), TypePermissionDenied)
	f.err = fmt.Errorf("storage failure")
	requireProblem(t, do(t, h, http.MethodDelete, path, "", profileOwner()), TypeInternalError)
	deps.NotificationChannels = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodDelete, path, "", profileOwner()), TypeDependencyUnavailable)
}
