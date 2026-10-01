package apiv2

import (
	"context"
	"encoding/json"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"net/url"
	"strings"
	"testing"
)

type fakeAdminAutoscanConnections struct{ calls int }

func (f *fakeAdminAutoscanConnections) ReadAdminAutoscanConnections(context.Context) ([]handlers.AdminAutoscanConnectionView, error) {
	f.calls++
	return []handlers.AdminAutoscanConnectionView{{ID: "b", Name: "same", Kind: "radarr", HasAPIKey: true}, {ID: "a", Name: "same", Kind: "sonarr"}}, nil
}
func TestAdminAutoscanConnectionsRead(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeAdminAutoscanConnections)
	deps.AdminAutoscanConnections = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/connections"
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized read")
	}
	rec := do(t, h, "GET", path+"?limit=1", "", bearer(adminToken))
	var body Collection[AdminAutoscanConnection]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Items) != 1 || body.Items[0].ID != "a" || body.Page == nil || !body.Page.HasMore {
		t.Fatal(rec.Code, rec.Body.String())
	}
	cursor := url.QueryEscape(body.Page.NextCursor)
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	for _, want := range []string{`"id":"b"`, `"has_api_key":true`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatal(want, rec.Code, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "api_key_ref") {
		t.Fatal("credential reference exposed")
	}

	requireProblem(t, do(t, h, "GET", path+"?limit=2&cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", actingRequestAdmin), TypeInvalidCursor)
	deps.AdminAutoscanConnections = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}
