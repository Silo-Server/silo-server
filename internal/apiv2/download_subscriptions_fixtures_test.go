package apiv2

func downloadSubscriptionFixtureCases() []fixtureCase {
	viewer := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device-one")
	return []fixtureCase{
		{name: "download_subscription", operationID: "getDownloadSubscription", method: "GET", path: Prefix + "/downloads/subscriptions/monitor", headers: viewer, status: 200, schema: "#/components/schemas/DownloadSubscription", assertHeaders: []string{"Content-Type", "ETag"}, scenario: "A paused device monitor retains its specials-season selection and mutation validator."},
		{name: "download_subscriptions", operationID: "listDownloadSubscriptions", method: "GET", path: Prefix + "/downloads/subscriptions", headers: viewer, status: 200, schema: "#/components/schemas/CollectionDownloadSubscription", assertHeaders: []string{"Content-Type"}, scenario: "A bounded terminal device page includes paused monitors and per-item validators."},
	}
}
