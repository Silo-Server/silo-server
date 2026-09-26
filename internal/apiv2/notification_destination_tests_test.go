package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeNotificationDestinationTests struct {
	calls       int
	profile, id string
	err         error
}

func (f *fakeNotificationDestinationTests) TestNotificationWebhook(_ context.Context, profile, id string) (*notifications.WebhookTestResult, error) {
	f.calls++
	f.profile = profile
	f.id = id
	return &notifications.WebhookTestResult{OK: false, HTTPStatus: 429, DurationMS: 2, Message: "429 Too Many Requests"}, f.err
}
func (f *fakeNotificationDestinationTests) TestNotificationServerChannel(ctx context.Context, id string) (*notifications.WebhookTestResult, error) {
	return f.TestNotificationWebhook(ctx, "", id)
}

func TestNotificationDestinationDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, path, profile string
		headers             map[string]string
		missing, disabled   error
	}{
		{"webhook", Prefix + "/notifications/webhooks/hook/test", "p-owner", profileOwner(), notifications.ErrWebhookNotFound, notifications.ErrWebhooksDisabled},
		{"server-channel", Prefix + "/admin/notifications/server-channels/hook/test", "", bearer(adminToken), notifications.ErrServerChannelNotFound, notifications.ErrServerChannelsDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := new(fakeNotificationDestinationTests)
			deps := pilotDeps(nil, nil)
			deps.NotificationDestinationTests = fake
			h := NewHandler(deps)
			rec := do(t, h, http.MethodPost, tc.path, "", nil)
			if rec.Code != 401 || fake.calls != 0 {
				t.Fatalf("unauthorized dispatch: %d", rec.Code)
			}
			rec = do(t, h, http.MethodPost, tc.path, "", tc.headers)
			var body NotificationDestinationTestResult
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || fake.calls != 1 || fake.profile != tc.profile || fake.id != "hook" || body.OK || body.HTTPStatus != 429 || body.DurationMS != 2 || body.Message != "429 Too Many Requests" {
				t.Fatalf("%d %+v %+v", rec.Code, body, fake)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
			for _, failure := range []struct {
				err    error
				status int
			}{{tc.missing, 404}, {tc.disabled, 403}} {
				fake.err = fmt.Errorf("wrapped: %w", failure.err)
				before := fake.calls
				rec = do(t, h, http.MethodPost, tc.path, "", tc.headers)
				if rec.Code != failure.status || fake.calls != before+1 {
					t.Fatalf("failure: %d %s", rec.Code, rec.Body.String())
				}
			}
			if tc.profile == "" {
				before := fake.calls
				rec = do(t, h, http.MethodPost, tc.path, "", profileOwner())
				if rec.Code != 403 || fake.calls != before {
					t.Fatal("non-admin dispatched")
				}
			}
		})
	}
}

func notificationDestinationTestFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "notification_webhook_test", operationID: testNotificationWebhookOperation, method: "POST", path: Prefix + "/notifications/webhooks/hook/test", headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationDestinationTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A synchronous synthetic webhook send reports429 without scheduling a retry."},
		{name: "notification_server_channel_test", operationID: testNotificationServerChannelOperation, method: "POST", path: Prefix + "/admin/notifications/server-channels/hook/test", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/NotificationDestinationTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "An administrator's synthetic server-channel test preserves the sender's diagnostic outcome."},
	}
}
