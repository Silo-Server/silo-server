package api

import (
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

// householdGateExemptPrefixes are /api/v1 namespaces whose profile-optional
// viewer routes keep account scope without X-Profile-Id: none of them serves
// catalog content under the request's viewer scope.
var householdGateExemptPrefixes = []string{
	"/api/v1/admin/",           // server-admin and admin-permission routes
	"/api/v1/profiles/",        // profile selection and management, before a profile is chosen
	"/api/v1/settings/",        // account and profile settings
	"/api/v1/stream/",          // bound to a playback session started with a verified profile
	"/api/v1/playback/",        // capability probe and session-bound transcode and control routes
	"/api/v1/history-imports/", // account-level history import
	"/api/v1/plex-sync/",       // account-level sync connections
	"/api/v1/webhook-sync/",    // account-level sync connections
	"/api/v1/notifications/",   // account-level notification links and preferences
	"/api/v1/downloads/subscriptions",
}

// householdGateExemptRoutes are the remaining profile-optional v1 viewer
// routes that keep account scope, each for a stated reason.
var householdGateExemptRoutes = map[string]string{
	"POST /api/v1/auth/device/approve-handoff":           "account sign-in handoff",
	"GET /api/v1/downloads/capability":                   "capability probe",
	"GET /api/v1/downloads/batches/{batch_id}/manifests": "managed download: the handler requires a profile",
	"PATCH /api/v1/downloads/{id}":                       "managed download: the handler requires a profile",
	"GET /api/v1/downloads/{id}/manifest":                "managed download: the handler requires a profile",
	"GET /api/v1/downloads/{id}/artwork/{kind}":          "managed download: the handler requires a profile",
	"GET /api/v1/downloads/{id}/subtitles/{ref}":         "managed download: the handler requires a profile",
	"GET /api/v1/events/capability":                      "capability probe",
	"GET /api/v1/events/ws":                              "account event socket",
	"GET /api/v1/images/capability":                      "capability probe",
	"GET /api/v1/items/trailers/capability":              "capability probe",
	"GET /api/v1/metadata/ai/status":                     "capability probe",
	"GET /api/v1/policy/capability":                      "capability probe",
	"GET /api/v1/subtitles/ai/status":                    "capability probe",
	"GET /api/v1/subtitles/providers/status":             "capability probe",
	"GET /api/v1/subtitles/ai/quota":                     "account-level quota",
	"POST /api/v1/subtitles/detect-language":             "file-less language detection",
	"GET /api/v1/sections/recipes":                       "recipe gallery metadata",
	"GET /api/v1/sections/recipes/{type}/candidates":     "recipe gallery metadata",
	"GET /api/v1/user/libraries":                         "library discovery before a profile is chosen",
	"GET /api/v1/watch-together/rooms/{room_id}/ws":      "room joined through profile-required routes",
}

// TestHouseholdProfileGateCoversV1ViewerRoutes pins which v1 routes carry the
// household profile gate. Every /api/v1 route that runs viewer access without
// requiring a profile must carry the gate or be exempt above, so a viewer
// route that moves out of a gated group, or a new one registered without it,
// fails here instead of answering at account scope. The inventory is checked
// against router.go by make verify-route-inventory.
func TestHouseholdProfileGateCoversV1ViewerRoutes(t *testing.T) {
	inventory, err := routeinventory.LoadArtifact(".")
	if err != nil {
		t.Fatal(err)
	}
	usedExemptions := map[string]bool{}
	gated := 0
	for _, route := range inventory.Routes {
		if route.Listener != "api" || !strings.HasPrefix(route.Path, "/api/v1/") {
			continue
		}
		if !slices.Contains(route.AuthTraits, "viewer_access") || slices.Contains(route.AuthTraits, "profile_required") {
			continue
		}
		if route.AuthClass == "acting_admin" {
			continue // primary-profile admin routes, not viewer reads
		}
		key := route.Method + " " + route.Path
		isGated := slices.Contains(route.AuthTraits, "household_profile_gate")
		_, exempt := householdGateExemptRoutes[key]
		if exempt {
			usedExemptions[key] = true
		}
		exempt = exempt || slices.ContainsFunc(householdGateExemptPrefixes, func(prefix string) bool {
			return strings.HasPrefix(route.Path, prefix)
		})
		switch {
		case isGated && exempt:
			t.Errorf("%s carries the household gate but is listed as exempt", key)
		case isGated:
			gated++
		case !exempt:
			t.Errorf("%s runs viewer access without a profile and without the household gate; gate it or exempt it with a reason", key)
		}
	}
	if gated == 0 {
		t.Fatal("no v1 route carries the household gate; the inventory trait or the router wiring changed")
	}
	for key := range householdGateExemptRoutes {
		if !usedExemptions[key] {
			t.Errorf("exemption %s matches no profile-optional v1 viewer route; remove it", key)
		}
	}
}
