package apiv2

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// orderedPushFixtureHeaders are a current login session acting as its owner
// profile, plus a synthetic installation credential and generation.
func orderedPushFixtureHeaders() map[string]string {
	headers := with(bearer("tok-events"), "X-Profile-Id", "p-owner")
	headers["X-Push-Installation-Key"] = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	headers["X-Push-Generation"] = "3"
	return headers
}

func applePushFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "apple_push_registration_capability", operationID: "getApplePushRegistrationCapabilities", scenario: "Local ordered Apple registration and current-login validation are configured; this does not describe provider delivery.", method: "GET", path: Prefix + "/devices/push/apple/capabilities", headers: profileOwner(), status: 200, schema: "#/components/schemas/ApplePushRegistrationCapability", assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}
}

// pushRegistrationReceiptFixtureCases are appended last so the request ids of
// earlier fixtures stay put.
func pushRegistrationReceiptFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "apple_push_registration_ok", operationID: "registerApplePushDevice", scenario: "An iPhone registers for private push and receives a display credential; the receipt names the server so the app can key that credential by server and profile.", method: http.MethodPost, path: Prefix + "/devices/push/apple", headers: orderedPushFixtureHeaders(), body: `{"device_id":"install","apns_token":"` + strings.Repeat("a", 64) + `","apns_environment":"production","apns_topic":"org.siloserver.silo","push_mode":"private_push"}`, status: 200, schema: "#/components/schemas/ApplePushRegistrationReceipt", assertHeaders: []string{"Content-Type", "Cache-Control"}},
		{name: "android_push_registration_ok", operationID: "registerPushDevice", scenario: "An Android installation registers for private push; the receipt names the server so the app can route that server's pushes by server and profile.", method: http.MethodPost, path: Prefix + "/notifications/push/devices", headers: orderedPushFixtureHeaders(), body: `{"device_id":"install","platform":"android","token":"` + strings.Repeat("a", 64) + `","push_mode":"private_push"}`, status: 200, schema: "#/components/schemas/AndroidPushRegistrationReceipt", assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}
}
