package apiv2

import "net/http"

// deviceLoginCancelFixtureCases covers cancelDeviceLogin.
func deviceLoginCancelFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "cancel_device_login_ok", operationID: "cancelDeviceLogin",
			scenario: "The device withdraws its pending request when it stops showing the code.",
			method:   http.MethodPost, path: "/api/v2/auth/device/cancel", body: `{"device_code":"dev-cancel"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/DeviceLoginCancellation"},
		{name: "cancel_device_login_device_code_required", operationID: "cancelDeviceLogin",
			scenario: "A cancel without its device code is a validation failure naming the member.",
			method:   http.MethodPost, path: "/api/v2/auth/device/cancel", body: `{}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Problem"},
	}
}
