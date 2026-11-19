package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeNotificationRelay struct {
	registers, clears int
	url               string
	err               error
}

func (f *fakeNotificationRelay) RegisterNotificationRelay(_ context.Context, url string) (handlers.NotificationRelayView, error) {
	f.registers++
	f.url = url
	return handlers.NotificationRelayView{RelayURL: "https://relay.example.test", DeploymentID: "deployment-1", KeyPrefix: "prefix", ExpiresAt: fixedTime()}, f.err
}
func (f *fakeNotificationRelay) ClearNotificationRelay(context.Context) error {
	f.clears++
	return f.err
}

func notificationRelayFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "notification_relay_register", operationID: "registerAdminNotificationRelay", method: "POST", path: Prefix + "/admin/notifications/push/relay/register", body: `{"relay_url":"https://relay.example.test"}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/NotificationRelayRegistration", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "Synthetic relay registration returns credential metadata without its reusable secret."},
		{name: "notification_relay_clear", operationID: "clearAdminNotificationRelay", method: "DELETE", path: Prefix + "/admin/notifications/push/relay", headers: bearer(adminToken), status: 204, assertHeaders: []string{"Cache-Control"}, scenario: "An explicit administrator command clears the local relay credential."},
	}
}
