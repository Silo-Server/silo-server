package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/librarymonitor"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeLibraryMonitoring struct {
	snap librarymonitor.StatusSnapshot
	err  error
}

func (f *fakeLibraryMonitoring) RealtimeMonitoringStatus(context.Context) (librarymonitor.StatusSnapshot, error) {
	return f.snap, f.err
}

type monitoringEntry struct {
	LibraryID   string  `json:"library_id"`
	Enabled     bool    `json:"enabled"`
	State       string  `json:"state"`
	Backend     string  `json:"backend"`
	Detail      string  `json:"detail"`
	Directories int     `json:"directories"`
	NodeID      *string `json:"node_id"`
	UpdatedAt   *string `json:"updated_at"`
}

type monitoringBody struct {
	ServerEnabled bool              `json:"server_enabled"`
	Libraries     []monitoringEntry `json:"libraries"`
}

const monitoringPath = Prefix + "/libraries/realtime-monitoring"

func getMonitoring(t *testing.T, snap librarymonitor.StatusSnapshot) monitoringBody {
	t.Helper()
	deps := pilotDeps(nil, nil)
	deps.LibraryMonitoring = &fakeLibraryMonitoring{snap: snap}
	rec := do(t, newTestHandler(t, deps), http.MethodGet, monitoringPath, "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body monitoringBody
	decodeJSON(t, rec.Body, &body)
	return body
}

func monitoredFolder(id, sortOrder int) *models.MediaFolder {
	return &models.MediaFolder{ID: id, Name: "lib", Enabled: true, RealtimeMonitoring: true, SortOrder: sortOrder}
}

func TestLibraryRealtimeMonitoringDerivedStates(t *testing.T) {
	reportedAt := time.Date(2026, 9, 27, 17, 0, 0, 0, time.UTC)
	report := func(libraryID int) librarymonitor.NodeReport {
		return librarymonitor.NodeReport{NodeID: "node-a", LibraryID: libraryID, State: librarymonitor.StateMonitoring,
			Backend: librarymonitor.BackendInotify, Directories: 12, UpdatedAt: reportedAt}
	}
	disabled := monitoredFolder(2, 1)
	disabled.Enabled = false
	switchedOff := monitoredFolder(3, 2)
	switchedOff.RealtimeMonitoring = false
	// A disabled library whose switch is also off is library_disabled: the
	// checks run in contract order.
	bothOff := monitoredFolder(5, 4)
	bothOff.Enabled, bothOff.RealtimeMonitoring = false, false
	libraries := []*models.MediaFolder{monitoredFolder(1, 0), disabled, switchedOff, monitoredFolder(4, 3), bothOff}
	// Every library has a fresh report except library 4; derived states
	// ignore them.
	reports := []librarymonitor.NodeReport{report(1), report(2), report(3), report(5)}

	body := getMonitoring(t, librarymonitor.StatusSnapshot{ServerEnabled: true, Libraries: libraries, Reports: reports})
	if !body.ServerEnabled || len(body.Libraries) != 5 {
		t.Fatalf("body = %+v", body)
	}
	want := []struct {
		id, state string
		enabled   bool
	}{
		{"1", "monitoring", true},
		{"2", "library_disabled", true},
		{"3", "monitoring_off", false},
		{"4", "not_reporting", true},
		{"5", "library_disabled", false},
	}
	for i, w := range want {
		got := body.Libraries[i]
		if got.LibraryID != w.id || got.State != w.state || got.Enabled != w.enabled {
			t.Errorf("entry %d = %+v, want id %s state %s enabled %v", i, got, w.id, w.state, w.enabled)
		}
	}
	live := body.Libraries[0]
	if live.Backend != "inotify" || live.Directories != 12 || live.NodeID == nil || *live.NodeID != "node-a" ||
		live.UpdatedAt == nil || *live.UpdatedAt != "2026-09-27T17:00:00.000Z" {
		t.Errorf("reported entry = %+v", live)
	}
	for _, derived := range body.Libraries[1:] {
		if derived.NodeID != nil || derived.UpdatedAt != nil || derived.Backend != "" || derived.Directories != 0 {
			t.Errorf("derived entry %s carries report fields: %+v", derived.LibraryID, derived)
		}
	}
	if got := body.Libraries[3].Detail; got != "No server node can see this library's folders" {
		t.Errorf("not_reporting detail = %q", got)
	}

	// The server switch wins over everything.
	body = getMonitoring(t, librarymonitor.StatusSnapshot{ServerEnabled: false, Libraries: libraries, Reports: reports})
	if body.ServerEnabled {
		t.Fatal("server_enabled = true, want false")
	}
	for _, got := range body.Libraries {
		if got.State != "server_disabled" || got.NodeID != nil {
			t.Errorf("entry %s = %+v, want server_disabled without report fields", got.LibraryID, got)
		}
	}
	if !body.Libraries[0].Enabled || body.Libraries[2].Enabled {
		t.Error("enabled must stay the library's own switch while the server is off")
	}
}

func TestLibraryRealtimeMonitoringBestNodeReport(t *testing.T) {
	base := time.Date(2026, 9, 27, 17, 0, 0, 0, time.UTC)
	rep := func(node string, libraryID int, state librarymonitor.State, at time.Time) librarymonitor.NodeReport {
		return librarymonitor.NodeReport{NodeID: node, LibraryID: libraryID, State: state, Detail: node + " says " + string(state), UpdatedAt: at}
	}
	libraries := []*models.MediaFolder{monitoredFolder(1, 0), monitoredFolder(2, 1), monitoredFolder(3, 2), monitoredFolder(4, 3), monitoredFolder(5, 4), monitoredFolder(6, 5)}
	reports := []librarymonitor.NodeReport{
		// monitoring beats starting and anything else.
		rep("node-a", 1, librarymonitor.StateError, base),
		rep("node-b", 1, librarymonitor.StateMonitoring, base),
		rep("node-c", 1, librarymonitor.StateStarting, base),
		// starting beats the failure states.
		rep("node-a", 2, librarymonitor.StateLimitReached, base),
		rep("node-b", 2, librarymonitor.StateStarting, base),
		// Among failures the contract order holds.
		rep("node-a", 3, librarymonitor.StateUnsupportedPlatform, base),
		rep("node-b", 3, librarymonitor.StateRootUnavailable, base),
		rep("node-c", 3, librarymonitor.StateError, base),
		rep("node-d", 3, librarymonitor.StateUnsupportedFilesystem, base),
		// A tie goes to the newer report.
		rep("node-a", 4, librarymonitor.StateMonitoring, base.Add(time.Minute)),
		rep("node-b", 4, librarymonitor.StateMonitoring, base),
		// An unknown stored state reads as error and never beats a known one.
		rep("node-a", 5, librarymonitor.State("paused"), base),
		rep("node-a", 6, librarymonitor.State("paused"), base),
		rep("node-b", 6, librarymonitor.StateError, base),
	}
	body := getMonitoring(t, librarymonitor.StatusSnapshot{ServerEnabled: true, Libraries: libraries, Reports: reports})
	want := []struct{ state, node string }{
		{"monitoring", "node-b"},
		{"starting", "node-b"},
		{"root_unavailable", "node-b"},
		{"monitoring", "node-a"},
		{"error", "node-a"},
		{"error", "node-b"},
	}
	for i, w := range want {
		got := body.Libraries[i]
		if got.State != w.state || got.NodeID == nil || *got.NodeID != w.node {
			t.Errorf("library %s = %+v, want %s from %s", got.LibraryID, got, w.state, w.node)
		}
	}
	if got := body.Libraries[4].Detail; got != "node-a says paused" {
		t.Errorf("unknown state detail = %q, want it kept", got)
	}
}

func TestLibraryRealtimeMonitoringOrdering(t *testing.T) {
	libraries := []*models.MediaFolder{monitoredFolder(9, 2), monitoredFolder(4, 0), nil, monitoredFolder(7, 1), monitoredFolder(2, 1)}
	body := getMonitoring(t, librarymonitor.StatusSnapshot{ServerEnabled: true, Libraries: libraries})
	var ids []string
	for _, l := range body.Libraries {
		ids = append(ids, l.LibraryID)
	}
	if want := []string{"4", "2", "7", "9"}; len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] || ids[3] != want[3] {
		t.Fatalf("order = %v, want %v", ids, want)
	}

	// No libraries is an empty array, not null.
	deps := pilotDeps(nil, nil)
	deps.LibraryMonitoring = &fakeLibraryMonitoring{snap: librarymonitor.StatusSnapshot{ServerEnabled: true}}
	rec := do(t, newTestHandler(t, deps), http.MethodGet, monitoringPath, "", bearer(adminToken))
	var raw map[string]json.RawMessage
	decodeJSON(t, rec.Body, &raw)
	if string(raw["libraries"]) != "[]" {
		t.Fatalf("libraries = %s, want []", raw["libraries"])
	}
}

func TestLibraryRealtimeMonitoringAccess(t *testing.T) {
	deps := pilotDeps(nil, nil)
	fake := &fakeLibraryMonitoring{snap: librarymonitor.StatusSnapshot{ServerEnabled: true}}
	deps.LibraryMonitoring = fake
	h := newTestHandler(t, deps)
	requireProblem(t, do(t, h, http.MethodGet, monitoringPath, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, monitoringPath, "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, monitoringPath, "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)

	fake.err = errors.New("database down")
	requireProblem(t, do(t, h, http.MethodGet, monitoringPath, "", bearer(adminToken)), TypeInternalError)

	deps.LibraryMonitoring = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, monitoringPath, "", bearer(adminToken)), TypeDependencyUnavailable)
}

func TestLibraryCapabilities(t *testing.T) {
	// Build discovery needs no service.
	h := newTestHandler(t, pilotDeps(nil, nil))
	path := Prefix + "/libraries/capabilities"
	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)
	rec := do(t, h, http.MethodGet, path, "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var got map[string]any
	decodeJSON(t, rec.Body, &got)
	if got["realtime_monitoring"] != true || got["state"] != StateAvailable || got["allowed"] != true || got["revision"] == "" {
		t.Fatalf("body = %v", got)
	}
	if len(got) != 4 {
		t.Fatalf("unexpected members: %v", got)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if again := do(t, h, http.MethodGet, path, "", with(bearer(adminToken), "If-None-Match", etag)); again.Code != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want 304", again.Code)
	}
}

// libraryMonitoringFixtureCases run against fixtureDeps' monitoring fake.
func libraryMonitoringFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "get_library_realtime_monitoring_ok", operationID: "getLibraryRealtimeMonitoring",
			scenario: "One library monitored by a node, one with no fresh node report.",
			method:   http.MethodGet, path: monitoringPath, headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryRealtimeMonitoring"},
		{name: "get_library_capabilities_ok", operationID: "getLibraryCapabilities",
			scenario: "Administrator discovery of the library features this build supports.",
			method:   http.MethodGet, path: Prefix + "/libraries/capabilities", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryCapabilities"},
	}
}
