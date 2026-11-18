package apiv2

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeNotificationDestinations struct {
	deleted string
	calls   int
	user    int
	profile string
	limit   int
	// loginSession is the login session the last subscribe recorded.
	loginSession string
}

func (f *fakeNotificationDestinations) ListNotificationWebPushPage(_ context.Context, user int, profile string, limit int, after *notifications.Cursor) ([]notifications.WebPushSubscription, error) {
	f.user, f.profile, f.limit = user, profile, limit
	rows := []notifications.WebPushSubscription{{ID: "01", Endpoint: "https://push.example.test/1", P256dh: "sensitive-p256dh", Auth: "sensitive-auth", CreatedAt: fixedTime()}, {ID: "02", Endpoint: "https://push.example.test/2", CreatedAt: fixedTime()}}
	if after != nil {
		rows = rows[1:]
	}
	return rows, nil
}
func (f *fakeNotificationDestinations) ListNotificationWebhookPage(_ context.Context, user int, profile string, limit int, after *notifications.Cursor) ([]notifications.Webhook, error) {
	f.user, f.profile, f.limit = user, profile, limit
	rows := []notifications.Webhook{{ID: "01", ProfileID: profile, Revision: 7, Name: "Example", Type: "generic", URLHost: "example.test", URLCiphertext: "sensitive-url", SigningSecretCiphertext: new("sensitive-secret"), CreatedAt: fixedTime()}, {ID: "02", ProfileID: profile, Revision: 7, Name: "Other", Type: "generic", URLHost: "example.test", CreatedAt: fixedTime()}}
	if after != nil {
		rows = rows[1:]
	}
	return rows, nil
}
func (f *fakeNotificationDestinations) ListNotificationServerChannelPage(_ context.Context, limit int, after *notifications.Cursor) ([]notifications.ServerChannel, error) {
	f.limit = limit
	rows := []notifications.ServerChannel{{ID: "01", Name: "Example", Type: "generic", URLHost: "example.test", URLCiphertext: "sensitive-url", SigningSecretCiphertext: new("sensitive-secret"), CreatedAt: fixedTime()}, {ID: "02", Name: "Other", Type: "generic", URLHost: "example.test", CreatedAt: fixedTime()}}
	if after != nil {
		rows = rows[1:]
	}
	return rows, nil
}

func notificationDestinationFixtureCases() []fixtureCase {
	cases := []fixtureCase{
		{name: "notification_web_push_subscriptions", operationID: listNotificationWebPushOperation, path: Prefix + "/notifications/web-push/subscriptions", headers: profileOwner(), schema: "#/components/schemas/CollectionNotificationWebPushSubscription"},
		{name: "notification_webhooks", operationID: listNotificationWebhooksOperation, path: Prefix + "/notifications/webhooks", headers: profileOwner(), schema: "#/components/schemas/CollectionNotificationWebhookDestination"},
		{name: "notification_server_channels", operationID: listNotificationServerChannelsOperation, path: Prefix + "/admin/notifications/server-channels", headers: bearer(adminToken), schema: "#/components/schemas/CollectionNotificationServerChannel"},
	}
	for i := range cases {
		cases[i].method = "GET"
		cases[i].status = 200
		cases[i].scenario = "Bounded notification destination metadata excludes reusable keys and webhook URL ciphertext."
		cases[i].assertHeaders = []string{"Content-Type", "Cache-Control"}
	}
	return cases
}

func (f *fakeNotificationDestinations) DeleteNotificationWebPushSubscription(_ context.Context, user int, profile, id string) error {
	f.calls++
	f.user, f.profile, f.deleted = user, profile, id
	return nil
}
func (f *fakeNotificationDestinations) UnsubscribeNotificationWebPush(_ context.Context, _ int, profile, endpoint string) error {
	f.calls++
	f.profile, f.deleted = profile, endpoint
	return nil
}
func (f *fakeNotificationDestinations) SubscribeNotificationWebPush(_ context.Context, _ int, profile, endpoint, p256dh, auth, _, loginSessionID string) (*notifications.WebPushSubscription, error) {
	f.loginSession = loginSessionID
	f.calls++
	f.profile, f.deleted = profile, endpoint
	if endpoint == "invalid" {
		return nil, notifications.ErrWebPushInvalid
	}
	return &notifications.WebPushSubscription{ID: "row-one", Endpoint: endpoint, P256dh: p256dh, Auth: auth, CreatedAt: fixedTime()}, nil
}
func (f *fakeNotificationDestinations) DeleteNotificationWebhook(_ context.Context, user int, profile, id string, check func(int64) error) error {
	if err := check(7); err != nil {
		return err
	}
	f.calls++
	f.user, f.profile, f.deleted = user, profile, id
	return nil
}
func (f *fakeNotificationDestinations) DeleteNotificationServerChannel(_ context.Context, id string) error {
	f.calls++
	f.deleted = id
	return nil
}
func (f *fakeNotificationDestinations) RotateNotificationWebhookSecret(_ context.Context, user int, profile, id string) (string, error) {
	f.calls++
	f.user, f.profile, f.deleted = user, profile, id
	if id == "discord" {
		return "", notifications.ErrWebhookInvalid
	}
	if id == "missing" {
		return "", notifications.ErrWebhookNotFound
	}
	return "synthetic-secret", nil
}
func (f *fakeNotificationDestinations) UpdateNotificationWebhook(_ context.Context, user int, profile, id string, input notifications.WebhookInput, check func(int64) error) (*notifications.Webhook, error) {
	f.calls++
	f.user, f.profile = user, profile
	if id == "missing" {
		return nil, notifications.ErrWebhookNotFound
	}
	if err := check(7); err != nil {
		return nil, err
	}
	if input.Name != nil && *input.Name == "" {
		return nil, notifications.ErrWebhookInvalid
	}
	return &notifications.Webhook{ID: id, ProfileID: profile, Name: "updated", Type: "generic", Revision: 8}, nil
}

func (f *fakeNotificationDestinations) RotateNotificationServerChannelSecret(_ context.Context, id string) (string, error) {
	f.calls++
	f.deleted = id
	if id == "missing" {
		return "", notifications.ErrServerChannelNotFound
	}
	if id == "discord" {
		return "", notifications.ErrServerChannelInvalid
	}
	return "synthetic-secret", nil
}
func (f *fakeNotificationDestinations) UpdateNotificationServerChannel(_ context.Context, id string, input notifications.ServerChannelInput) (*notifications.ServerChannel, error) {
	f.calls++
	f.deleted = id
	if id == "missing" {
		return nil, notifications.ErrServerChannelNotFound
	}
	if input.Name != nil && *input.Name == "" {
		return nil, notifications.ErrServerChannelInvalid
	}
	return &notifications.ServerChannel{ID: id, Name: "updated", Type: "generic", CreatedAt: time.Unix(1, 0)}, nil
}
