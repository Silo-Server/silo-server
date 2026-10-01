package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

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
func TestNotificationDestinationCreate(t *testing.T) {
	for _, tc := range []struct {
		name, path, profile      string
		headers                  map[string]string
		disabled, invalid, limit error
	}{
		{"webhook", Prefix + "/notifications/webhooks", "p-owner", profileOwner(), notifications.ErrWebhooksDisabled, notifications.ErrWebhookInvalid, notifications.ErrWebhookLimit},
		{"channel", Prefix + "/admin/notifications/server-channels", "", bearer(adminToken), notifications.ErrServerChannelsDisabled, notifications.ErrServerChannelInvalid, notifications.ErrServerChannelLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := new(fakeNotificationDestinationCreate)
			deps := pilotDeps(nil, nil)
			deps.NotificationDestinationCreate = fake
			h := NewHandler(deps)
			body := `{"name":"test","url":"https://example.test/private-hook","type":"generic"}`
			rec := do(t, h, http.MethodPost, tc.path, body, nil)
			if rec.Code != 401 || fake.calls != 0 {
				t.Fatalf("unauthorized: %d calls=%d", rec.Code, fake.calls)
			}
			rec = do(t, h, http.MethodPost, tc.path, body, tc.headers)
			var out NotificationDestinationCreated
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 201 || fake.calls != 1 || fake.profile != tc.profile || out.ID != "created" || out.SigningSecret != "one-time-secret" || strings.Contains(rec.Body.String(), "private-hook") {
				t.Fatalf("create: %d %s calls=%d profile=%s", rec.Code, rec.Body.String(), fake.calls, fake.profile)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("one-time secret response must not be cached")
			}
			if tc.profile != "" && (fake.webhook.URL == nil || *fake.webhook.URL != "https://example.test/private-hook" || fake.webhook.NotifyFavorites != nil) {
				t.Fatal("personal input/defaults changed")
			}
			if tc.profile == "" && (fake.channel.URL == nil || *fake.channel.URL != "https://example.test/private-hook" || fake.channel.Enabled != nil) {
				t.Fatal("admin input/defaults changed")
			}
			for _, failure := range []struct {
				err    error
				status int
			}{{tc.disabled, 403}, {tc.invalid, 422}, {tc.limit, 422}, {errors.New("secret-provider-diagnostic"), 500}} {
				fake.err = failure.err
				before := fake.calls
				rec = do(t, h, http.MethodPost, tc.path, body, tc.headers)
				if rec.Code != failure.status || fake.calls != before+1 || strings.Contains(rec.Body.String(), "secret-provider-diagnostic") {
					t.Fatalf("failure: %d %s", rec.Code, rec.Body.String())
				}
			}
			before := fake.calls
			rec = do(t, h, http.MethodPost, tc.path, `{"name":"test"}`, tc.headers)
			if rec.Code != 422 || fake.calls != before {
				t.Fatalf("missing URL dispatched: %d", rec.Code)
			}
			if tc.profile == "" {
				rec = do(t, h, http.MethodPost, tc.path, body, profileOwner())
				if rec.Code != 403 || fake.calls != before {
					t.Fatal("non-admin created channel")
				}
			}
			deps.NotificationDestinationCreate = nil
			rec = do(t, NewHandler(deps), http.MethodPost, tc.path, body, tc.headers)
			if rec.Code != 503 {
				t.Fatalf("unavailable: %d", rec.Code)
			}
		})
	}
}

func notificationDestinationCreateFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "notification_webhook_create", operationID: createNotificationWebhookOperation, method: "POST", path: Prefix + "/notifications/webhooks", body: `{"name":"fixture","url":"https://example.test/hook"}`, headers: profileOwner(), status: 201, schema: "#/components/schemas/NotificationDestinationCreated", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A new profile webhook returns its signing secret once; creation is not automatically replayed."},
		{name: "notification_server_channel_create", operationID: createNotificationServerChannelOperation, method: "POST", path: Prefix + "/admin/notifications/server-channels", body: `{"name":"fixture","url":"https://example.test/hook"}`, headers: bearer(adminToken), status: 201, schema: "#/components/schemas/NotificationDestinationCreated", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "An administrator creates a server channel without sending a notification."},
	}
}
