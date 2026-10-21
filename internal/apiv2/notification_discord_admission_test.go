package apiv2

import (
	"net/http"
	"testing"
)

// A mutation without household authorization wiring must never dispatch to
// an account-wide destination backend. All integrations here are fakes.
func TestDiscordMutationsFailClosedWithoutHouseholdAdmission(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/discord/link/init", ""},
		{http.MethodPut, "/discord-preferences", `{"mode":"off"}`},
		{http.MethodDelete, "/discord-link", ""},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			channels := new(fakeNotificationChannels)
			links := new(fakeNotificationDiscordLinks)
			deps := pilotDeps(nil, nil)
			deps.NotificationChannels = channels
			deps.NotificationDiscordLinks = links
			rec := do(t, NewHandler(deps), tc.method, Prefix+"/notifications"+tc.path, tc.body, profileOwner())
			if rec.Code < 400 || channels.writes != 0 || links.calls != 0 {
				t.Fatalf("unwired household admission: status=%d writes=%d links=%d", rec.Code, channels.writes, links.calls)
			}
		})
	}
}
