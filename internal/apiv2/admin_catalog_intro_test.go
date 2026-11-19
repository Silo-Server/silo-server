package apiv2

import (
	"context"
)

type fakeAdminIntro struct {
	calls                    int
	id, action, kind, status string
	err                      error
}

func (f *fakeAdminIntro) RedetectItemMarkers(_ context.Context, id, kind string) (string, error) {
	f.calls++
	f.id, f.kind = id, kind
	if f.status != "" {
		return f.status, f.err
	}
	return "queued", f.err
}

func (f *fakeAdminIntro) RefreshEpisodeMarkers(_ context.Context, id, action string) (string, error) {
	f.calls++
	f.id, f.action = id, action
	if f.status != "" {
		return f.status, f.err
	}
	return "queued", f.err
}
func adminCatalogIntroFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_episode_markers_refresh", operationID: "refreshAdminEpisodeMarkers", method: "POST", path: Prefix + "/admin/items/episode-1/refresh-markers", headers: bearer(adminToken), status: 202, schema: "#/components/schemas/AdminEpisodeMarkersStatus", assertHeaders: []string{"Content-Type"}, scenario: "Marker refresh acknowledges configured online or local sources."},
		{name: "admin_marker_capabilities", operationID: "getAdminMarkerCapabilities", method: "GET", path: Prefix + "/admin/markers/capabilities", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminMarkerCapabilities", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "Marker capabilities report local movie credits, marker re-detection, and detection kind settings support."},
		{name: "admin_episode_intro_redetect", operationID: "redetectAdminEpisodeIntro", method: "POST", path: Prefix + "/admin/items/episode-1/redetect-intro", headers: bearer(adminToken), status: 202, schema: "#/components/schemas/AdminEpisodeMarkersStatus", assertHeaders: []string{"Content-Type"}, scenario: "Intro re-detection preserves the same local execution service and eligibility checks."},
		{name: "admin_item_markers_redetect", operationID: "redetectAdminItemMarkers", method: "POST", path: Prefix + "/admin/items/episode-1/redetect-markers", headers: bearer(adminToken), body: `{"kind":"credits"}`, status: 202, schema: "#/components/schemas/AdminEpisodeMarkersStatus", assertHeaders: []string{"Content-Type"}, scenario: "Marker re-detection reruns local detection of the requested kinds."},
		{name: "admin_item_markers_redetect_unknown_kind", operationID: "redetectAdminItemMarkers", method: "POST", path: Prefix + "/admin/items/episode-1/redetect-markers", headers: bearer(adminToken), body: `{"kind":"outro"}`, status: 422, schema: "#/components/schemas/Problem", assertHeaders: []string{"Content-Type"}, scenario: "A kind other than intro, credits, or all is rejected before analysis is queued."},
	}
}
