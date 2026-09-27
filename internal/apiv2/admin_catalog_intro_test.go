package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
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
func TestAdminEpisodeMarkersTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminIntro{}
	deps.AdminEpisodeMarkers = f
	h := newTestHandler(t, deps)
	for _, tc := range []struct{ path, action string }{{"refresh-markers", "refresh-v2"}, {"redetect-intro", "redetect"}} {
		path := Prefix + "/admin/items/episode-1/" + tc.path
		for _, status := range []string{"queued", "already_running"} {
			f.status = status
			rec := do(t, h, "POST", path, "", bearer(adminToken))
			if rec.Code != 202 || f.action != tc.action || f.id != "episode-1" || rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), `"status":"`+status+`"`) {
				t.Fatalf("%s: %d %s", tc.path, rec.Code, rec.Body)
			}
		}
		before := f.calls
		rec := do(t, h, "POST", path, "", bearer(memberToken))
		if rec.Code != 403 || f.calls != before {
			t.Fatalf("member: %d calls=%d", rec.Code, f.calls)
		}
		f.err = &handlers.APIError{Status: http.StatusConflict, Code: "conflict", Message: "Marker detection is disabled"}
		rec = do(t, h, "POST", path, "", bearer(adminToken))
		if rec.Code != 409 {
			t.Fatalf("conflict: %d %s", rec.Code, rec.Body)
		}
		f.err = nil
	}
	deps.AdminEpisodeMarkers = nil
	h = newTestHandler(t, deps)
	rec := do(t, h, "POST", Prefix+"/admin/items/episode-1/redetect-intro", "", bearer(adminToken))
	if rec.Code != 503 {
		t.Fatalf("unwired: %d %s", rec.Code, rec.Body)
	}
}
func TestAdminItemMarkersRedetectTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminIntro{}
	deps.AdminEpisodeMarkers = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/items/episode-1/redetect-markers"
	for _, tc := range []struct{ body, kind string }{
		{"", "all"}, {`{}`, "all"}, {`{"kind":"intro"}`, "intro"}, {`{"kind":"credits"}`, "credits"}, {`{"kind":"all"}`, "all"},
	} {
		f.kind = ""
		rec := do(t, h, "POST", path, tc.body, bearer(adminToken))
		if rec.Code != 202 || f.kind != tc.kind || f.id != "episode-1" || !strings.Contains(rec.Body.String(), `"status":"queued"`) {
			t.Fatalf("body %q: %d %s kind=%q", tc.body, rec.Code, rec.Body, f.kind)
		}
	}
	before := f.calls
	rec := do(t, h, "POST", path, `{"kind":"outro"}`, bearer(adminToken))
	if rec.Code != 422 || f.calls != before {
		t.Fatalf("unknown kind: %d calls=%d %s", rec.Code, f.calls, rec.Body)
	}
	rec = do(t, h, "POST", path, "", bearer(memberToken))
	if rec.Code != 403 || f.calls != before {
		t.Fatalf("member: %d calls=%d", rec.Code, f.calls)
	}
	f.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Item must be an episode"}
	rec = do(t, h, "POST", Prefix+"/admin/items/movie-1/redetect-markers", `{"kind":"intro"}`, bearer(adminToken))
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Item must be an episode") {
		t.Fatalf("movie intro: %d %s", rec.Code, rec.Body)
	}
	f.err = &handlers.APIError{Status: http.StatusConflict, Code: "conflict", Message: "Marker detection is disabled"}
	rec = do(t, h, "POST", path, "", bearer(adminToken))
	if rec.Code != 409 {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body)
	}
	// Requested kinds turned off in marker settings answer a conflict that
	// names them.
	f.err = &handlers.APIError{Status: http.StatusConflict, Code: "conflict", Message: "Credits detection is turned off in marker settings"}
	rec = do(t, h, "POST", path, `{"kind":"credits"}`, bearer(adminToken))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"detail":"Credits detection is turned off in marker settings"`) {
		t.Fatalf("kind turned off: %d %s", rec.Code, rec.Body)
	}
	deps.AdminEpisodeMarkers = nil
	h = newTestHandler(t, deps)
	rec = do(t, h, "POST", path, "", bearer(adminToken))
	if rec.Code != 503 {
		t.Fatalf("unwired: %d %s", rec.Code, rec.Body)
	}
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

func TestAdminMarkerCapabilities(t *testing.T) {
	// Build discovery needs no marker service.
	h := NewHandler(pilotDeps(nil, nil))
	path := Prefix + "/admin/markers/capabilities"
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
	if got["state"] != StateAvailable || got["movie_credits"] != true || got["redetect_markers"] != true || got["detection_kind_settings"] != true || got["allowed"] != true {
		t.Fatalf("capabilities %v, want movie credits, marker re-detection, and detection kind settings available", got)
	}
}
