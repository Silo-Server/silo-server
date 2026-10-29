package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/requests/discoverrows"
)

// fakeDiscoverAdmin adds Discover layout administration to the admin
// request fake; it checks rows the way the service does.
type fakeDiscoverAdmin struct {
	*fakeAdminRequests
	layout mediarequests.DiscoverLayoutAdmin
}

func (f *fakeDiscoverAdmin) GetDiscoverLayout(context.Context, mediarequests.Viewer) (*mediarequests.DiscoverLayoutAdmin, error) {
	l := f.layout
	return &l, nil
}

func (f *fakeDiscoverAdmin) ReplaceDiscoverLayoutConditional(_ context.Context, _ mediarequests.Viewer, rows []discoverrows.Row, expected int64) (*mediarequests.DiscoverLayoutAdmin, error) {
	if expected != -1 && expected != f.layout.Revision {
		return nil, mediarequests.ErrStaleRevision
	}
	normalized, errs := discoverrows.NormalizeLayout(rows)
	if len(errs) > 0 {
		return nil, &mediarequests.ValidationError{FieldErrors: errs}
	}
	f.writes++
	f.layout.Rows, f.layout.Customized, f.layout.Revision = normalized, true, f.layout.Revision+1
	return f.GetDiscoverLayout(context.Background(), mediarequests.Viewer{})
}

func (f *fakeDiscoverAdmin) ResetDiscoverLayoutConditional(_ context.Context, _ mediarequests.Viewer, expected int64) error {
	if expected != -1 && expected != f.layout.Revision {
		return mediarequests.ErrStaleRevision
	}
	f.writes++
	f.layout = mediarequests.DiscoverLayoutAdmin{DiscoverLayout: mediarequests.DiscoverLayout{Rows: discoverrows.DefaultRows()}}
	return nil
}

func (f *fakeDiscoverAdmin) PreviewDiscoverRow(context.Context, mediarequests.Viewer, discoverrows.Row) (*mediarequests.MediaPage, error) {
	return &mediarequests.MediaPage{Page: 1, TotalPages: 1, Results: []mediarequests.MediaResult{fixtureResult(949)}}, nil
}

func (f *fakeDiscoverAdmin) ListWatchProviders(context.Context, mediarequests.Viewer, mediarequests.MediaType, string) ([]tmdb.WatchProvider, error) {
	return []tmdb.WatchProvider{{ID: 8, Name: "Netflix"}}, nil
}

func TestAdminRequestDiscoverLayout(t *testing.T) {
	listRow := discoverrows.Row{ID: "r_list", Type: discoverrows.TypeMDBList, Title: "List", Enabled: true, Limit: 20, List: &discoverrows.ListSpec{URL: "https://mdblist.com/lists/u/l"}}
	f := &fakeDiscoverAdmin{fakeAdminRequests: fixtureAdminRequests()}
	f.layout = mediarequests.DiscoverLayoutAdmin{
		DiscoverLayout: mediarequests.DiscoverLayout{Rows: []discoverrows.Row{listRow}, Customized: true, Revision: 7},
		Sources:        map[string]discoverrows.SourceStatus{"r_list": {State: discoverrows.StateError, Entries: 3, Error: "mdblist down"}},
	}
	deps := requestDeps(fixtureRequests())
	deps.AdminRequests = f
	h := NewHandler(deps)
	path := Prefix + "/admin/request-discover/layout"

	requireProblem(t, do(t, h, http.MethodGet, path, "", requestOwner), TypePermissionDenied)
	var got AdminDiscoverLayout
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	decodeBody(t, read.Body, &got)
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" || !got.Customized || len(got.Rows) != 1 || got.Rows[0].Source == nil ||
		got.Rows[0].Source.State != "error" || got.Rows[0].Source.Entries != 3 || got.MaxRows != discoverrows.MaxRows {
		t.Fatalf("%d %s", read.Code, read.Body.String())
	}

	body := `{"rows":[{"type":"tmdb_discover","title":"Anime on Netflix","enabled":true,"limit":30,"hide_available":true,"discover":{"media_type":"series","sort_by":"popularity.desc","keyword_ids":[210024],"watch_provider_ids":[8],"watch_region":"US"}},{"type":"studios","title":"Studios","enabled":false}]}`
	requireProblem(t, do(t, h, http.MethodPut, path, body, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, path, body, with(actingRequestAdmin, "If-Match", `"stale"`)), TypePreconditionFailed)
	invalid := do(t, h, http.MethodPut, path, `{"rows":[{"type":"mdblist","title":"Bad","enabled":true,"list":{"url":"http://10.0.0.1/lists/a/b"}}]}`, with(actingRequestAdmin, "If-Match", tag))
	if doc := requireProblem(t, invalid, TypeValidationFailed); len(doc.Errors) != 1 || doc.Errors[0].Location != "body.rows[0].list.url" {
		t.Fatalf("errors = %+v, want the list url", doc.Errors)
	}
	if f.writes != 0 {
		t.Fatal("a refused write took effect")
	}
	saved := do(t, h, http.MethodPut, path, body, with(actingRequestAdmin, "If-Match", tag))
	decodeBody(t, saved.Body, &got)
	if saved.Code != 200 || saved.Header().Get("ETag") == tag || len(got.Rows) != 2 || got.Rows[1].ID != "studios" || got.Rows[1].Enabled {
		t.Fatalf("%d %s", saved.Code, saved.Body.String())
	}

	reset := do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", saved.Header().Get("ETag")))
	if reset.Code != http.StatusNoContent || f.layout.Customized {
		t.Fatalf("%d %s", reset.Code, reset.Body.String())
	}
}
