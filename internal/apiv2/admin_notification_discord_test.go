package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/discord"
)

type fakeAdminNotificationDiscord struct {
	calls int
	err   error
}

func (f *fakeAdminNotificationDiscord) TestDiscordBot(context.Context) (discord.User, error) {
	f.calls++
	return discord.User{Username: "fixture-bot"}, f.err
}

func adminNotificationDiscordFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_discord_test", operationID: testAdminDiscordNotificationOperation, method: "POST", path: Prefix + "/admin/notifications/discord/test", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminNotificationDiscordTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A synthetic bot identity lookup verifies credentials without sending a notification."}}
}
