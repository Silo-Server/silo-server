package apiv2

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloads"
)

// demoDownloadDeps is the download surface wired against demo mode, so the
// gate — not a missing service — is what a denial proves.
func demoDownloadDeps(demo bool) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.DemoSettings = fakeSettings{demo: demo}
	deps.Downloads = &fakeDownloadRegistry{}
	deps.DownloadCreation = &fakeDownloadCreation{row: &downloads.Download{ID: "entry", ContentID: "movie", MediaFileID: 42, Revision: 1, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}}
	return deps
}

// TestDownloadDemoRestrictions pins v1's demo rule on v2: with demo.enabled
// set, a non-admin may not POST or DELETE under /downloads (v1's blocked
// prefix, internal/api/middleware/demo_guard.go), an admin still may, and
// PATCH — which v1's guard never listed — keeps working for everyone.
func TestDownloadDemoRestrictions(t *testing.T) {
	h := newTestHandler(t, demoDownloadDeps(true))
	member := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device-one")
	admin := with(with(bearer(adminToken), "X-Profile-Id", "p-primary"), "X-Silo-Device-Id", "device-one")
	create := `{"content_id":"movie","media_file_id":"42","expected_revision":0}`

	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"create downloads", http.MethodPost, Prefix + "/downloads", create},
		{"delete download", http.MethodDelete, Prefix + "/downloads/entry", ""},
		{"create subscription", http.MethodPost, Prefix + "/downloads/subscriptions", `{"content_id":"series","mode":"all"}`},
		{"delete subscription", http.MethodDelete, Prefix + "/downloads/subscriptions/sub", ""},
		{"sync subscription", http.MethodPost, Prefix + "/downloads/subscriptions/sync", `{"subscription_id":"sub"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.path, tc.body, member)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body.String())
			}
			requireProblem(t, rec, TypePermissionDenied)
		})
	}

	// PATCH is not in v1's blocked method list, so a member still reports
	// local status in demo mode.
	rec := do(t, h, http.MethodPatch, Prefix+"/downloads/entry", `{"status":"completed","updated_at":"2026-01-02T03:04:05.000Z","revision":2}`, member)
	if rec.Code != http.StatusOK {
		t.Fatalf("member PATCH in demo mode: %d %s", rec.Code, rec.Body.String())
	}

	// Admins bypass the restriction, exactly as on v1.
	if rec := do(t, h, http.MethodPost, Prefix+"/downloads", create, admin); rec.Code != http.StatusAccepted {
		t.Fatalf("admin POST in demo mode: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodDelete, Prefix+"/downloads/entry", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("admin DELETE in demo mode: %d %s", rec.Code, rec.Body.String())
	}

	// With demo mode off the same member mutations pass.
	off := newTestHandler(t, demoDownloadDeps(false))
	if rec := do(t, off, http.MethodPost, Prefix+"/downloads", create, member); rec.Code != http.StatusAccepted {
		t.Fatalf("member POST with demo off: %d %s", rec.Code, rec.Body.String())
	}
}

// TestDownloadCapabilityReportsDemoRestriction: clients discover the
// restriction through the capability document rather than by attempting a
// mutation.
func TestDownloadCapabilityReportsDemoRestriction(t *testing.T) {
	read := func(t *testing.T, h http.Handler, headers map[string]string) DownloadCapability {
		t.Helper()
		rec := do(t, h, http.MethodGet, Prefix+"/capabilities/downloads", "", headers)
		if rec.Code != http.StatusOK {
			t.Fatalf("capability status %d: %s", rec.Code, rec.Body.String())
		}
		var out DownloadCapability
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	member := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	admin := with(bearer(adminToken), "X-Profile-Id", "p-primary")

	on := newTestHandler(t, demoDownloadDeps(true))
	if out := read(t, on, member); out.Allowed == nil || *out.Allowed {
		t.Fatalf("member capability allowed in demo mode: %+v", out.Allowed)
	}
	if out := read(t, on, admin); out.Allowed == nil || !*out.Allowed {
		t.Fatalf("admin capability refused in demo mode: %+v", out.Allowed)
	}
	off := newTestHandler(t, demoDownloadDeps(false))
	if out := read(t, off, member); out.Allowed == nil || !*out.Allowed {
		t.Fatalf("member capability refused with demo off: %+v", out.Allowed)
	}
}
