package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type fakeNotificationDiscordLinks struct{ calls, userID int }

func (f *fakeNotificationDiscordLinks) BeginNotificationDiscordLink(_ context.Context, userID int) (string, error) {
	f.calls++
	f.userID = userID
	return "https://discord.example.test/oauth2/authorize?state=synthetic", nil
}
func (*fakeNotificationDiscordLinks) HandleNotificationDiscordCallback(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings/notifications?discord_linked=1", http.StatusFound)
}

func TestNotificationDiscordLinkTransport(t *testing.T) {
	fake := new(fakeNotificationDiscordLinks)
	deps := pilotDeps(nil, nil)
	deps.NotificationDiscordLinks = fake
	deps.NotificationDiscordManagement = discordManagement([]userstore.Profile{{ID: "p-owner", IsPrimary: true}})
	h := NewHandler(deps)
	path := Prefix + "/notifications/discord/link/init"
	rec := do(t, h, http.MethodPost, path, "", nil)
	if rec.Code != 401 || fake.calls != 0 {
		t.Fatal("anonymous link initiation")
	}
	for _, profile := range []string{"p-locked", "unknown"} {
		rec = do(t, h, http.MethodPost, path, "", with(bearer(memberToken), "X-Profile-Id", profile))
		if rec.Code != 403 && rec.Code != 404 {
			t.Fatalf("optional profile gate: %d", rec.Code)
		}
		if fake.calls != 0 {
			t.Fatal("invalid profile started consent")
		}
	}
	rec = do(t, h, http.MethodPost, path, "", profileOwner())
	if rec.Code != 200 || fake.calls != 1 || fake.userID != 1 {
		t.Fatalf("account initiation: %d %s %+v", rec.Code, rec.Body.String(), fake)
	}
}

func notificationDiscordLinkFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "notification_discord_link_init", operationID: beginNotificationDiscordLinkOperation, method: "POST", path: Prefix + "/notifications/discord/link/init", headers: with(bearer(memberToken), "X-Profile-Id", "p-primary"), status: 200, schema: "#/components/schemas/NotificationDiscordLink", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A synthetic account-bound Discord consent URL is returned without contacting the provider."}}
}
