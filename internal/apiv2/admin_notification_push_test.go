package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeAdminNotificationPush struct {
	calls                     int
	account                   int
	platform, profile, device string
	err                       error
}

// fixtureNotifiedProfile is the one profile the fake reports a notification
// for, standing in for a profile whose inbox is not empty.
const fixtureNotifiedProfile = "notified-profile"

func (f *fakeAdminNotificationPush) send(platform string, account int, profile, device string) (*notifications.ApplePushTestResult, error) {
	f.calls++
	f.account, f.platform, f.profile, f.device = account, platform, profile, device
	result := &notifications.ApplePushTestResult{AttemptID: "attempt-1", PushDeviceID: "device-1", ServerDeviceID: device, Outcome: "retrying", UpstreamStatus: new(503)}
	if profile == fixtureNotifiedProfile {
		result.DeliveryID, result.Outcome, result.UpstreamStatus = "delivery-1", "delivered", new(200)
	}
	return result, f.err
}
func (f *fakeAdminNotificationPush) SendApplePushTest(_ context.Context, account int, profile, device string) (*notifications.ApplePushTestResult, error) {
	return f.send("apple", account, profile, device)
}
func (f *fakeAdminNotificationPush) SendAndroidPushTest(_ context.Context, account int, profile, device string) (*notifications.ApplePushTestResult, error) {
	return f.send("fcm", account, profile, device)
}

func adminNotificationPushFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_apple_push_test", operationID: testAdminApplePushOperation, method: "POST", path: Prefix + "/admin/notifications/push/apple/test", body: `{"profile_id":"target-profile","server_device_id":"target-device"}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminNotificationPushTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A synthetic test attempt reports retrying; HTTP success is not proof of push delivery."},
		{name: "admin_android_push_test", operationID: testAdminAndroidPushOperation, method: "POST", path: Prefix + "/admin/notifications/push/fcm/test", body: `{"profile_id":"target-profile","server_device_id":"target-device"}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminNotificationPushTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A synthetic Android test dispatch preserves its upstream status."},
	}
}

// adminNotificationPushDeliveryFixtureCases are appended last so the request
// ids of earlier fixtures stay put.
func adminNotificationPushDeliveryFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_apple_push_test_notification", operationID: testAdminApplePushOperation, method: "POST", path: Prefix + "/admin/notifications/push/apple/test", body: `{"profile_id":"` + fixtureNotifiedProfile + `","server_device_id":"target-device"}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminNotificationPushTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "The test push carries the profile's newest notification and names it, so the device shows that notification's real text."},
	}
}
