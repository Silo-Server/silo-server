package apiv2

func applePushFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "apple_push_registration_capability", operationID: "getApplePushRegistrationCapabilities", scenario: "Local ordered Apple registration is unavailable when the service is absent; this does not describe provider delivery.", method: "GET", path: Prefix + "/devices/push/apple/capabilities", headers: profileOwner(), status: 200, schema: "#/components/schemas/ApplePushRegistrationCapability", assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}
}
