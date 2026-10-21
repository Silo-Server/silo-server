package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/historyimport"
)

type fakeAdminHistoryImports struct {
	AdminHistoryImportService
	source  historyimport.Source
	mapping historyimport.UserMapping
	run     historyimport.Run
	writes  int
	race    bool

	// users and plex are the failures the two source-server calls answer
	// with; nil means the call succeeds.
	users    []historyimport.ExternalUser
	usersErr error
	plexErr  error
}

func fixtureAdminHistoryImports() *fakeAdminHistoryImports {
	return &fakeAdminHistoryImports{source: historyimport.Source{ID: 1, Name: "Source", SourceType: "emby", BaseURL: "https://source.example.test", Enabled: true, HasAdminToken: true, Revision: 1}, mapping: historyimport.UserMapping{ID: 2, SourceID: 1, ExternalUserID: "external", ExternalUserName: "Example", SiloUserID: 3, SiloProfileID: "profile", Revision: 2}, run: historyimport.Run{ID: "run-1", UserID: 3, ProfileID: "profile", SourceType: "emby", ConnectionMode: "admin_token", Status: "queued", CreatedAt: fixedTime()}}
}
func (f *fakeAdminHistoryImports) GetAdminSource(context.Context, int) (*historyimport.Source, error) {
	r := f.source
	return &r, nil
}
func (f *fakeAdminHistoryImports) GetMapping(context.Context, int) (*historyimport.UserMapping, error) {
	r := f.mapping
	return &r, nil
}
func (f *fakeAdminHistoryImports) UpdateSourceConditional(_ context.Context, _ int, in historyimport.UpdateSourceInput, rev int64) (*historyimport.Source, error) {
	if f.race {
		f.source.Revision++
		return nil, historyimport.ErrStaleRevision
	}
	if rev != -1 && rev != f.source.Revision {
		return nil, historyimport.ErrStaleRevision
	}
	f.writes++
	f.source.Revision++
	if in.Name != nil {
		f.source.Name = *in.Name
	}
	r := f.source
	return &r, nil
}
func (f *fakeAdminHistoryImports) DeleteSourceConditional(_ context.Context, _ int, rev int64) error {
	if rev != -1 && rev != f.source.Revision {
		return historyimport.ErrStaleRevision
	}
	f.writes++
	return nil
}
func (f *fakeAdminHistoryImports) ClearSourceAdminTokenConditional(ctx context.Context, id int, rev int64) (*historyimport.Source, error) {
	return f.UpdateSourceConditional(ctx, id, historyimport.UpdateSourceInput{}, rev)
}
func (f *fakeAdminHistoryImports) ListAdminSources(context.Context) ([]historyimport.Source, error) {
	return []historyimport.Source{f.source, {ID: 4, Name: "Second", Revision: 3}}, nil
}
func (f *fakeAdminHistoryImports) CreateAdminRun(context.Context, int) (*historyimport.Run, error) {
	f.writes++
	r := f.run
	return &r, nil
}
func (f *fakeAdminHistoryImports) GetAdminRun(context.Context, string) (*historyimport.Run, error) {
	r := f.run
	return &r, nil
}
func (f *fakeAdminHistoryImports) BulkCreateAdminRuns(context.Context, int) (*historyimport.BulkRunResult, error) {
	return &historyimport.BulkRunResult{Outcomes: []historyimport.BulkRunOutcome{{MappingID: 2, Status: "accepted", Run: &f.run}, {MappingID: 3, Status: "active", Run: &f.run}, {MappingID: 4, Status: historyimport.RunStatusFailed, Error: "secret-token"}}}, nil
}
func adminHistoryHandler(f *fakeAdminHistoryImports) http.Handler {
	deps := requestDeps(fixtureRequests())
	deps.AdminHistoryImports = f
	return NewHandler(deps)
}
func TestAdminHistorySourceGuards(t *testing.T) {
	f := fixtureAdminHistoryImports()
	h := adminHistoryHandler(f)
	path := Prefix + "/admin/history-import-sources/1"
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if read.Code != 200 {
		t.Fatal(read.Code, read.Body.String())
	}
	tag := read.Header().Get("ETag")
	if tag == "" {
		t.Fatal("missing tag")
	}
	for _, field := range []string{"created_at", "updated_at", "revision", "\"admin_token\""} {
		if strings.Contains(read.Body.String(), field) {
			t.Fatal("noncanonical editor", read.Body.String())
		}
	}
	requireProblem(t, do(t, h, http.MethodPut, path, `{"name":"Edited"}`, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, path, `{"name":"Edited"}`, with(actingRequestAdmin, "If-Match", `"stale"`)), TypePreconditionFailed)
	if f.writes != 0 {
		t.Fatal("precondition effects")
	}
	saved := do(t, h, http.MethodPut, path, `{"name":"Edited"}`, with(actingRequestAdmin, "If-Match", tag))
	if saved.Code != 200 || saved.Header().Get("ETag") == tag {
		t.Fatal(saved.Code, saved.Body.String())
	}
	f.race = true
	raced := do(t, h, http.MethodPut, path, `{"name":"Raced"}`, with(actingRequestAdmin, "If-Match", saved.Header().Get("ETag")))
	requireProblem(t, raced, TypePreconditionFailed)
	if raced.Header().Get("ETag") == saved.Header().Get("ETag") {
		t.Fatal("stale response tag")
	}
	f.race = false
	deleted := do(t, h, http.MethodDelete, Prefix+"/admin/history-imports/sources/1/token", "", with(actingRequestAdmin, "If-Match", "*"))
	if deleted.Code != 204 || deleted.Body.Len() != 0 || deleted.Header().Get("ETag") != "" {
		t.Fatal(deleted.Code, deleted.Body.String(), deleted.Header())
	}
}
func TestAdminHistoryListCursorAndAuthority(t *testing.T) {
	f := fixtureAdminHistoryImports()
	h := adminHistoryHandler(f)
	path := Prefix + "/admin/history-import-sources"
	requireProblem(t, do(t, h, http.MethodGet, path, "", requestOwner), TypePermissionDenied)
	first := do(t, h, http.MethodGet, path+"?limit=1", "", actingRequestAdmin)
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	var page Collection[AdminHistoryImportSource]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.Page.HasMore {
		t.Fatal(first.Body.String())
	}
	next := do(t, h, http.MethodGet, path+"?limit=1&cursor="+page.Page.NextCursor, "", actingRequestAdmin)
	if next.Code != 200 || !strings.Contains(next.Body.String(), `"id":"4"`) {
		t.Fatal(next.Code, next.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/history-imports/mappings?source_id=1&cursor="+page.Page.NextCursor, "", actingRequestAdmin), TypeInvalidCursor)
}

func (f *fakeAdminHistoryImports) ListMappings(context.Context, int) ([]historyimport.UserMapping, error) {
	return []historyimport.UserMapping{f.mapping}, nil
}

func (f *fakeAdminHistoryImports) CancelAdminRun(context.Context, string) error {
	switch f.run.Status {
	case historyimport.RunStatusCompleted, historyimport.RunStatusFailed:
		return historyimport.ErrRunNotCancelable
	case historyimport.RunStatusCancelled:
		return nil
	case "queued":
		f.run.Status = historyimport.RunStatusCancelled
	default:
		f.run.CancelRequested = true
	}
	return nil
}

func TestAdminHistoryLegacySourceAddressRedaction(t *testing.T) {
	f := fixtureAdminHistoryImports()
	f.source.BaseURL = "https://olduser:privatepass@source.example.test/base?api_key=privatekey#privatefragment"
	h := adminHistoryHandler(f)
	for _, path := range []string{"/admin/history-import-sources/1", "/admin/history-import-sources"} {
		r := do(t, h, http.MethodGet, Prefix+path, "", actingRequestAdmin)
		if r.Code != 200 || strings.Contains(r.Body.String(), "private") || strings.Contains(r.Body.String(), "olduser") || !strings.Contains(r.Body.String(), `"needs_reconfiguration":true`) {
			t.Fatal(r.Code, r.Body.String())
		}
	}
}

func (f *fakeAdminHistoryImports) CreateSource(context.Context, historyimport.CreateSourceInput) (*historyimport.Source, error) {
	r := f.source
	return &r, nil
}
func (f *fakeAdminHistoryImports) CreateMapping(context.Context, historyimport.CreateMappingInput) (*historyimport.UserMapping, error) {
	r := f.mapping
	return &r, nil
}

func (f *fakeAdminHistoryImports) DiscoverExternalUsers(context.Context, int) ([]historyimport.ExternalUser, error) {
	if f.usersErr != nil {
		return nil, f.usersErr
	}
	return f.users, nil
}
func (f *fakeAdminHistoryImports) AuthenticatePlex(context.Context, string, string) (string, error) {
	if f.plexErr != nil {
		return "", f.plexErr
	}
	return "plex-token", nil
}

// TestAdminHistorySourceCallFailuresKeepTheirStatus: neither call that leaves
// the server may collapse every failure onto the dependency problem. A source
// that does not exist is 404, a source with no admin token is 409, a source
// server that rejected the stored admin token is 409 as well, and plex.tv
// rejecting a password is a validation failure; only a source server that
// could not answer is 503.
func TestAdminHistorySourceCallFailuresKeepTheirStatus(t *testing.T) {
	users := Prefix + "/admin/history-imports/sources/1/users"
	login := Prefix + "/admin/history-imports/plex/login"
	credentials := `{"username":"owner","password":"private-password"}`
	cases := []struct {
		name    string
		method  string
		path    string
		body    string
		fail    func(*fakeAdminHistoryImports)
		problem ProblemType
	}{
		{"missing source", http.MethodGet, users, "", func(f *fakeAdminHistoryImports) { f.usersErr = historyimport.ErrSourceNotFound }, TypeNotFound},
		{"no admin token", http.MethodGet, users, "", func(f *fakeAdminHistoryImports) { f.usersErr = historyimport.ErrNoAdminToken }, TypeConflict},
		{"source unreachable", http.MethodGet, users, "", func(f *fakeAdminHistoryImports) { f.usersErr = errors.New("dial tcp: connection refused") }, TypeDependencyUnavailable},
		{"source rejected the stored token", http.MethodGet, users, "", func(f *fakeAdminHistoryImports) {
			f.usersErr = historyimport.UpstreamHTTPError(http.StatusUnauthorized)
		}, TypeConflict},
		{"source failed", http.MethodGet, users, "", func(f *fakeAdminHistoryImports) {
			f.usersErr = historyimport.UpstreamHTTPError(http.StatusBadGateway)
		}, TypeDependencyUnavailable},
		{"plex rejected the credentials", http.MethodPost, login, credentials, func(f *fakeAdminHistoryImports) {
			f.plexErr = handlers.HistoryImportUpstreamAPIError(http.StatusUnauthorized)
		}, TypeValidationFailed},
		{"plex could not answer", http.MethodPost, login, credentials, func(f *fakeAdminHistoryImports) {
			f.plexErr = handlers.HistoryImportUpstreamAPIError(http.StatusBadGateway)
		}, TypeDependencyUnavailable},
		{"plex answered with something else", http.MethodPost, login, credentials, func(f *fakeAdminHistoryImports) {
			f.plexErr = errors.New("plex: authentication response had no token")
		}, TypeDependencyUnavailable},
	}
	f := fixtureAdminHistoryImports()
	h := adminHistoryHandler(f)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			*f = *fixtureAdminHistoryImports()
			c.fail(f)
			r := do(t, h, c.method, c.path, c.body, actingRequestAdmin)
			requireProblem(t, r, c.problem)
			if strings.Contains(r.Body.String(), "private-password") {
				t.Fatal("credential echoed", r.Body.String())
			}
		})
	}
}
