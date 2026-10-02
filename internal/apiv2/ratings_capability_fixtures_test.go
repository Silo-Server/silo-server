package apiv2

import "net/http"

func ratingsCapabilityFixtureCases() []fixtureCase {
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	problem := "#/components/schemas/Problem"
	return []fixtureCase{
		{name: "get_ratings_capability_ok", operationID: "getRatingsCapability",
			scenario: "The ratings capability document on a server that shows only IMDb and TMDB, the default.",
			method:   http.MethodGet, path: "/api/v2/capabilities/ratings", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RatingsCapability"},
		{name: "get_ratings_capability_authentication_required", operationID: "getRatingsCapability",
			scenario: "The ratings capability document without a credential.",
			method:   http.MethodGet, path: "/api/v2/capabilities/ratings",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
	}
}
