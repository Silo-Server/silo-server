package apiv2

import (
	"context"
	"net/http"

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
