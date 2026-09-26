package apiv2

import (
	"net/http"
	"net/url"
)

func progressBootstrapFixtureCases() []fixtureCase {
	first, _ := renderProgressSnapshot(NewCursors([]byte("fixture-cursor-key")), bootstrapFixture(false))
	return []fixtureCase{
		{name: "progress_bootstrap_capabilities", operationID: opProgressBootstrapCapabilities, scenario: "Full replacement is available without an incremental sync promise.", method: http.MethodGet, path: Prefix + "/sync/progress/capabilities", headers: profileOwner(), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ProgressBootstrapCapabilities"},
		{name: "progress_bootstrap_created", operationID: opCreateProgressSnapshot, scenario: "An admission identity captures the first immutable page and canonical snapshot location.", method: http.MethodPost, path: progressSnapshotPath, body: `{"request_id":"9a91f367-e3bc-4305-b2ba-133bb507ce2d","limit":1}`, headers: profileOwner(), status: 201, assertHeaders: []string{"Content-Type", "Cache-Control", "Location"}, schema: "#/components/schemas/ProgressSnapshot"},
		{name: "progress_bootstrap_complete", operationID: opGetProgressSnapshot, scenario: "The terminal page carries a completion receipt rather than a next cursor.", method: http.MethodGet, path: progressSnapshotPath + "/" + bootstrapFixtureID + "?cursor=" + url.QueryEscape(first.Body.Page.NextCursor), headers: profileOwner(), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ProgressSnapshot"},
		{name: "progress_bootstrap_invalid_cursor", operationID: opGetProgressSnapshot, scenario: "A legacy numeric progress sequence cannot continue a replacement snapshot.", method: http.MethodGet, path: progressSnapshotPath + "/" + bootstrapFixtureID + "?cursor=12345", headers: profileOwner(), status: 400, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Problem"},
	}
}
