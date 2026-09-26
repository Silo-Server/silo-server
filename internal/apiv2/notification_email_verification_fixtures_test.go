package apiv2

func emailVerificationFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "notification_email_verification_capability", operationID: "getNotificationEmailVerificationCapabilities", scenario: "Durable queue admission and dispatch are separate; neither is available in this absent-service fixture.", method: "GET", path: Prefix + "/notifications/email-preferences/address/capabilities", headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationEmailVerificationCapability", assertHeaders: []string{"Content-Type", "Cache-Control"}}}
}
