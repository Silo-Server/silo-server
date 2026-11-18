package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeNotificationDestinationCreate struct {
	calls, user int
	profile     string
	webhook     notifications.WebhookInput
	channel     notifications.ServerChannelInput
	err         error
}

func (f *fakeNotificationDestinationCreate) CreateNotificationWebhook(_ context.Context, user int, profile string, in notifications.WebhookInput) (*notifications.Webhook, string, error) {
	f.calls++
	f.user = user
	f.profile = profile
	f.webhook = in
	return &notifications.Webhook{ID: "created", Name: *in.Name, Type: "generic", URLHost: "example.test"}, "one-time-secret", f.err
}
func (f *fakeNotificationDestinationCreate) CreateNotificationServerChannel(_ context.Context, user int, in notifications.ServerChannelInput) (*notifications.ServerChannel, string, error) {
	f.calls++
	f.user = user
	f.channel = in
	return &notifications.ServerChannel{ID: "created", Name: *in.Name, Type: "generic", URLHost: "example.test"}, "one-time-secret", f.err
}
func notificationDestinationCreateFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "notification_webhook_create", operationID: createNotificationWebhookOperation, method: "POST", path: Prefix + "/notifications/webhooks", body: `{"name":"fixture","url":"https://example.test/hook"}`, headers: profileOwner(), status: 201, schema: "#/components/schemas/NotificationDestinationCreated", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A new profile webhook returns its signing secret once; creation is not automatically replayed."},
		{name: "notification_server_channel_create", operationID: createNotificationServerChannelOperation, method: "POST", path: Prefix + "/admin/notifications/server-channels", body: `{"name":"fixture","url":"https://example.test/hook"}`, headers: bearer(adminToken), status: 201, schema: "#/components/schemas/NotificationDestinationCreated", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "An administrator creates a server channel without sending a notification."},
	}
}
