package apiv2

import (
	"encoding/json"
	"testing"
)

func TestAdminDashboardCapabilities(t *testing.T) {
	// Build discovery remains available with no runtime service dependencies.
	h := NewHandler(pilotDeps(nil, nil))
	path := Prefix + "/admin/dashboard/capabilities"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	expected := map[string]any{"server_layouts": false, "timeseries": true, "playback_activity": true, "top_activity": true, "health": true, "log_level_list": true, "watch_providers": true, "downloads_stats": true}
	if len(got) != len(expected)+3 {
		t.Fatal(got)
	}
	for key, value := range expected {
		if actual, present := got[key]; !present || actual != value {
			t.Fatalf("%s: got %v present=%v", key, actual, present)
		}
	}
}
