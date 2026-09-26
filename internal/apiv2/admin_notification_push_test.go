package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeAdminNotificationPush struct {
	calls                     int
	platform, profile, device string
	err                       error
}

func (f *fakeAdminNotificationPush) send(platform, profile, device string) (*notifications.ApplePushTestResult, error) {
	f.calls++
	f.platform, f.profile, f.device = platform, profile, device
	return &notifications.ApplePushTestResult{AttemptID: "attempt-1", PushDeviceID: "device-1", ServerDeviceID: device, Outcome: "retrying", UpstreamStatus: new(503)}, f.err
}
func (f *fakeAdminNotificationPush) SendApplePushTest(_ context.Context, profile, device string) (*notifications.ApplePushTestResult, error) {
	return f.send("apple", profile, device)
}
func (f *fakeAdminNotificationPush) SendAndroidPushTest(_ context.Context, profile, device string) (*notifications.ApplePushTestResult, error) {
	return f.send("fcm", profile, device)
}

func TestAdminNotificationPushDispatch(t *testing.T) {
	for _, platform := range []string{"apple", "fcm"} {
		t.Run(platform, func(t *testing.T) {
			fake := new(fakeAdminNotificationPush)
			deps := pilotDeps(nil, nil)
			deps.AdminNotificationPush = fake
			h := NewHandler(deps)
			path := Prefix + "/admin/notifications/push/" + platform + "/test"
			body := `{"profile_id":"target-profile","server_device_id":"target-device"}`
			for _, headers := range []map[string]string{nil, profileOwner(), with(bearer(adminToken), "X-Profile-Id", "p-owner")} {
				rec := do(t, h, http.MethodPost, path, body, headers)
				if rec.Code != 401 && rec.Code != 403 {
					t.Fatalf("unauthorized: %d %s", rec.Code, rec.Body.String())
				}
			}
			if fake.calls != 0 {
				t.Fatal("unauthorized dispatch")
			}
			rec := do(t, h, http.MethodPost, path, body, bearer(adminToken))
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			var result AdminNotificationPushTestResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if fake.calls != 1 || fake.platform != platform || fake.profile != "target-profile" || fake.device != "target-device" || result.Outcome != "retrying" || result.UpstreamStatus == nil || *result.UpstreamStatus != 503 {
				t.Fatalf("dispatch/result mismatch: %+v %+v", fake, result)
			}
			for _, tc := range []struct {
				err    error
				status int
			}{
				{notifications.ErrPushDeliveryInvalid, 422},
				{notifications.ErrPushDeliveryNotFound, 404},
				{notifications.ErrPushDeliveryUnavailable, 503},
			} {
				fake.err = fmt.Errorf("wrapped: %w", tc.err)
				before := fake.calls
				rec = do(t, h, http.MethodPost, path, body, bearer(adminToken))
				if rec.Code != tc.status || fake.calls != before+1 {
					t.Fatalf("%d %s; calls=%d", rec.Code, rec.Body.String(), fake.calls)
				}
			}
			before := fake.calls
			rec = do(t, h, http.MethodPost, path, `{}`, bearer(adminToken))
			if rec.Code != 422 || fake.calls != before {
				t.Fatalf("invalid body dispatched: %d", rec.Code)
			}
		})
	}
}

func adminNotificationPushFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_apple_push_test", operationID: testAdminApplePushOperation, method: "POST", path: Prefix + "/admin/notifications/push/apple/test", body: `{"profile_id":"target-profile","server_device_id":"target-device"}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminNotificationPushTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A synthetic test attempt reports retrying; HTTP success is not proof of push delivery."},
		{name: "admin_android_push_test", operationID: testAdminAndroidPushOperation, method: "POST", path: Prefix + "/admin/notifications/push/fcm/test", body: `{"profile_id":"target-profile","server_device_id":"target-device"}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminNotificationPushTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A synthetic Android test dispatch preserves its upstream status."},
	}
}
