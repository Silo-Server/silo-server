package apiv2

import (
	"context"
	"encoding/json"
	"testing"
)

// fixtureRatingSettings answers every setting as unset: title pages show IMDb
// and TMDB only.
type fixtureRatingSettings struct{}

func (fixtureRatingSettings) Get(context.Context, string) (string, error) { return "", nil }

func adminRatingSourcesFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_rating_source_capabilities", operationID: "getAdminRatingSourceCapabilities", method: "GET", path: Prefix + "/admin/rating-sources/capabilities", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminRatingSourceCapabilities", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "Rating source capabilities report plugin-declared sources."}, {name: "admin_rating_sources", operationID: "listAdminRatingSources", method: "GET", path: Prefix + "/admin/rating-sources", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/CollectionAdminRatingSource", assertHeaders: []string{"Content-Type"}, scenario: "Silo's own sources in display order, then a source a metadata plugin declared."}}
}

func TestAdminRatingSourceCapabilities(t *testing.T) {
	h := NewHandler(pilotDeps(nil, nil))
	path := Prefix + "/admin/rating-sources/capabilities"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 || rec.Header().Get("ETag") == "" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["state"] != StateAvailable || got["plugin_declared_sources"] != true || got["allowed"] != true {
		t.Fatalf("capabilities %v, want plugin-declared sources available", got)
	}
}
