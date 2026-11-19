package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeNotificationDestinationTests struct {
	calls       int
	user        int
	profile, id string
	err         error
}

func (f *fakeNotificationDestinationTests) TestNotificationWebhook(_ context.Context, user int, profile, id string) (*notifications.WebhookTestResult, error) {
	f.calls++
	f.user, f.profile = user, profile
	f.id = id
	return &notifications.WebhookTestResult{OK: false, HTTPStatus: 429, DurationMS: 2, Message: "429 Too Many Requests"}, f.err
}
func (f *fakeNotificationDestinationTests) TestNotificationServerChannel(ctx context.Context, id string) (*notifications.WebhookTestResult, error) {
	return f.TestNotificationWebhook(ctx, 0, "", id)
}

func notificationDestinationTestFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "notification_webhook_test", operationID: testNotificationWebhookOperation, method: "POST", path: Prefix + "/notifications/webhooks/hook/test", headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationDestinationTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A synchronous synthetic webhook send reports429 without scheduling a retry."},
		{name: "notification_server_channel_test", operationID: testNotificationServerChannelOperation, method: "POST", path: Prefix + "/admin/notifications/server-channels/hook/test", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/NotificationDestinationTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "An administrator's synthetic server-channel test preserves the sender's diagnostic outcome."},
	}
}
