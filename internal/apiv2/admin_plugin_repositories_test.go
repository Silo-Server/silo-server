package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakePluginRepositories struct {
	calls int
	rows  []*plugins.Repository
	err   error
}

func (f *fakePluginRepositories) List(context.Context) ([]*plugins.Repository, error) {
	f.calls++
	return f.rows, f.err
}
func TestAdminPluginRepositoriesRead(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 123456789, time.UTC)
	f := &fakePluginRepositories{rows: []*plugins.Repository{{ID: 9, URL: "https://example.invalid/index.json", SourceKind: "external", CreatedAt: at, UpdatedAt: at}, {ID: 2, SourceKind: "silo", ManagedKey: new("official"), LastFetchedAt: &at, CreatedAt: at, UpdatedAt: at}}}
	deps := pilotDeps(nil, nil)
	deps.AdminPluginRepositories = f
	h := NewHandler(deps)
	path := Prefix + "/admin/plugins/repositories"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("refusal read store")
	}
	rec := do(t, h, "GET", path+"?limit=1", "", bearer(adminToken))
	var body Collection[AdminPluginRepository]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Items) != 1 || body.Items[0].ID != "2" || !body.Items[0].Managed || body.Page == nil || !body.Page.HasMore || !strings.Contains(rec.Body.String(), "2026-09-01T00:00:00.123Z") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	cursor := url.QueryEscape(body.Page.NextCursor)
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"9"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, "GET", path+"?limit=2&cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", actingRequestAdmin), TypeInvalidCursor)
	f.rows = nil
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.err = errors.New("private database detail")
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private database") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.err = nil
	f.rows = []*plugins.Repository{{ID: 1}, {ID: 1}}
	if rec = do(t, h, "GET", path, "", bearer(adminToken)); rec.Code != 500 {
		t.Fatal(rec.Code)
	}
	deps.AdminPluginRepositories = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}
