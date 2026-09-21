package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminIntro struct {
	calls              int
	id, action, status string
	err                error
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
func adminCatalogIntroFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_episode_markers_refresh", operationID: "refreshAdminEpisodeMarkers", method: "POST", path: Prefix + "/admin/items/episode-1/refresh-markers", headers: bearer(adminToken), status: 202, schema: "#/components/schemas/AdminEpisodeMarkersStatus", assertHeaders: []string{"Content-Type"}, scenario: "Marker refresh acknowledges configured online or local sources."},
		{name: "admin_episode_intro_redetect", operationID: "redetectAdminEpisodeIntro", method: "POST", path: Prefix + "/admin/items/episode-1/redetect-intro", headers: bearer(adminToken), status: 202, schema: "#/components/schemas/AdminEpisodeMarkersStatus", assertHeaders: []string{"Content-Type"}, scenario: "Intro re-detection preserves the same local execution service and eligibility checks."},
	}
}
