package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeMarkers struct {
	calls   int
	target  handlers.MarkerTarget
	changes handlers.MarkerChanges
	err     error
}

func (f *fakeMarkers) GetMarkers(_ context.Context, _ catalogpkg.AccessFilter, target handlers.MarkerTarget) (handlers.FileMarkersView, error) {
	f.calls++
	f.target = target
	return handlers.FileMarkersView{FileID: 5, Intro: handlers.MarkerSegmentView{Start: new(0.0), End: new(12.0)}}, f.err
}
func (f *fakeMarkers) SetMarkers(ctx context.Context, access catalogpkg.AccessFilter, target handlers.MarkerTarget, changes handlers.MarkerChanges) (handlers.FileMarkersView, error) {
	f.changes = changes
	return f.GetMarkers(ctx, access, target)
}
func (f *fakeMarkers) ClearMarker(ctx context.Context, access catalogpkg.AccessFilter, id int, segment string) (handlers.FileMarkersView, error) {
	return f.SetMarkers(ctx, access, handlers.MarkerTarget{FileID: id}, handlers.MarkerChanges{segment: nil})
}

func TestMarkerOperations(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &fakeMarkers{}
	deps.Markers = fake
	h := newTestHandler(t, deps)
	for _, path := range []string{"/api/v2/markers/files/5", "/api/v2/markers/items/movie:one"} {
		response := do(t, h, http.MethodGet, path, "", bearer(memberToken))
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"file_id":"5"`) || !strings.Contains(response.Body.String(), `"start_seconds":0`) || strings.Contains(response.Body.String(), ":null") {
			t.Fatalf("read: %d %s", response.Code, response.Body.String())
		}
	}
	for _, path := range []string{"/api/v2/markers/files/5", "/api/v2/markers/items/movie:one"} {
		response := do(t, h, http.MethodPut, path, `{"intro":{"end_seconds":12},"credits":null}`, bearer(adminToken))
		if response.Code != 200 {
			t.Fatalf("write: %d %s", response.Code, response.Body.String())
		}
		intro := fake.changes["intro"]
		credits, present := fake.changes["credits"]
		if len(fake.changes) != 2 || intro == nil || intro.Start != nil || intro.End == nil || *intro.End != 12 || !present || credits != nil {
			t.Fatalf("presence lost: %+v", fake.changes)
		}
	}
	response := do(t, h, http.MethodDelete, "/api/v2/markers/files/5/intro", "", bearer(adminToken))
	if response.Code != 200 || fake.target.FileID != 5 || len(fake.changes) != 1 {
		t.Fatalf("clear: %d %s", response.Code, response.Body.String())
	}
	before := fake.calls
	for _, body := range []string{`null`, `{"unknown":null}`, `{"intro":{"unknown":1}}`, `{"intro":{"start_seconds":null,"end_seconds":12}}`, `{"intro":{"start_seconds":-1,"end_seconds":12}}`, `{"credits":true}`} {
		requireProblem(t, do(t, h, http.MethodPut, "/api/v2/markers/files/5", body, bearer(adminToken)), TypeValidationFailed)
	}
	if fake.calls != before {
		t.Fatal("invalid input reached service")
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/markers/files/0", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/markers/files/5/unknown", "", bearer(adminToken)), TypeValidationFailed)
}

func TestMarkerGatesBeforeService(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &fakeMarkers{}
	deps.Markers = fake
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
	}{
		{http.MethodGet, "/api/v2/markers/files/5", "", nil, 401},
		{http.MethodPut, "/api/v2/markers/files/5", `{}`, bearer(memberToken), 403},
		{http.MethodDelete, "/api/v2/markers/files/5/intro", "", bearer(memberToken), 403},
		{http.MethodGet, "/api/v2/markers/items/movie:one", "", with(bearer(memberToken), "X-Profile-Id", "p-locked"), 403},
	} {
		response := do(t, h, tc.method, tc.path, tc.body, tc.headers)
		if response.Code != tc.status {
			t.Fatalf("gate: %d %s", response.Code, response.Body.String())
		}
	}
	if fake.calls != 0 {
		t.Fatal("denied request reached service")
	}
	deps.Markers = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/markers/files/5", "", bearer(memberToken)), TypeDependencyUnavailable)
	fake.err = &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Media file not found"}
	deps.Markers = fake
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/markers/files/5", "", bearer(memberToken)), TypeNotFound)
}

func markerFixtureCases() []fixtureCase {
	var cases []fixtureCase
	for _, spec := range []struct{ name, id, method, path, body string }{
		{"get_file_markers_ok", "getFileMarkers", http.MethodGet, "/api/v2/markers/files/5", ""},
		{"get_item_markers_ok", "getItemMarkers", http.MethodGet, "/api/v2/markers/items/movie:one", ""},
		{"set_file_markers_ok", "setFileMarkers", http.MethodPut, "/api/v2/markers/files/5", `{"intro":{"end_seconds":12},"credits":null}`},
		{"set_item_markers_ok", "setItemMarkers", http.MethodPut, "/api/v2/markers/items/movie:one", `{"recap":null}`},
		{"clear_file_marker_segment_ok", "clearFileMarkerSegment", http.MethodDelete, "/api/v2/markers/files/5/intro", ""},
	} {
		cases = append(cases, fixtureCase{name: spec.name, operationID: spec.id, method: spec.method, path: spec.path, body: spec.body, headers: bearer(adminToken), status: http.StatusOK, schema: "#/components/schemas/FileMarkers", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "Synthetic marker projection; supplied segments update or clear while omitted segments remain unchanged."})
	}
	return append(cases, fixtureCase{name: "set_file_markers_permission_denied", operationID: "setFileMarkers", method: http.MethodPut, path: "/api/v2/markers/files/5", body: `{}`, headers: bearer(memberToken), status: http.StatusForbidden, schema: "#/components/schemas/Problem", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "An account without marker-edit permission cannot mutate marker state."})
}
