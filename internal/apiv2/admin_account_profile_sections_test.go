package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeAdminProfileSections keeps one override set per profile of account 7
// and refuses a profile it does not hold, the way *handlers.SectionHandler
// answers for a profile of another account.
type fakeAdminProfileSections struct {
	rows       map[string][]userstore.SectionOverride
	lastQuery  handlers.SectionOverridesQuery
	lastWrites []handlers.SectionOverrideWrite
	writes     int
}

func fixtureAdminProfileSections() *fakeAdminProfileSections {
	return &fakeAdminProfileSections{rows: map[string][]userstore.SectionOverride{
		"p-owner": fixtureSectionOverrides(),
		"p-kid":   {{ID: "o-kid", ProfileID: "p-kid", Scope: "home", SectionID: "s-continue", Hidden: true}},
	}}
}

func (f *fakeAdminProfileSections) target(q handlers.SectionOverridesQuery) error {
	f.lastQuery = q
	if _, ok := f.rows[q.ProfileID]; !ok || q.UserID != 7 {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Profile not found"}
	}
	return nil
}

func (f *fakeAdminProfileSections) ListAccountProfileOverrides(_ context.Context, q handlers.SectionOverridesQuery) ([]userstore.SectionOverride, error) {
	if err := f.target(q); err != nil {
		return nil, err
	}
	return f.rows[q.ProfileID], nil
}

func (f *fakeAdminProfileSections) SaveAccountProfileOverrides(_ context.Context, q handlers.SectionOverridesQuery, writes []handlers.SectionOverrideWrite) error {
	if err := f.target(q); err != nil {
		return err
	}
	f.lastWrites = writes
	f.writes++
	rows := make([]userstore.SectionOverride, 0, len(writes))
	for _, w := range writes {
		rows = append(rows, userstore.SectionOverride{ID: w.ID, ProfileID: q.ProfileID, Scope: q.Scope, SectionID: w.SectionID, Position: w.Position, Hidden: w.Hidden})
	}
	f.rows[q.ProfileID] = rows
	return nil
}

func (f *fakeAdminProfileSections) ResetAccountProfileOverrides(_ context.Context, q handlers.SectionOverridesQuery) error {
	if err := f.target(q); err != nil {
		return err
	}
	f.writes++
	f.rows[q.ProfileID] = []userstore.SectionOverride{}
	return nil
}

func (f *fakeAdminProfileSections) ResolveAccountProfileSectionSettings(_ context.Context, q handlers.SectionOverridesQuery, _ *int) ([]sections.ResolvedSection, error) {
	if err := f.target(q); err != nil {
		return nil, err
	}
	return []sections.ResolvedSection{
		{ID: "s-continue", SectionType: "continue_watching", Title: "Continue Watching", DefaultTitle: "Continue Watching", ItemLimit: 20, Position: 0, Customized: true, Hidden: true},
		{ID: "u-gems", SectionType: "hidden_gems", Title: "Hidden gems", ItemLimit: 12, Position: 1, IsCustom: true, Config: json.RawMessage(`{"library_ids":[3]}`)},
	}, nil
}

func adminProfileSectionsHandler(t *testing.T) (http.Handler, *fakeAdminAccounts, *fakeAdminProfileSections) {
	t.Helper()
	accounts := fixtureAdminAccounts()
	svc := fixtureAdminProfileSections()
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = accounts
	deps.AdminProfileSections = svc
	return NewHandler(deps), accounts, svc
}

const adminProfileSectionsBase = Prefix + "/admin/users/7/profiles/"

func TestAdminProfileSectionsRequireAnAdministrator(t *testing.T) {
	h, _, svc := adminProfileSectionsHandler(t)
	path := adminProfileSectionsBase + "p-kid/sections"
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, path, ""},
		{http.MethodGet, path + "/settings", ""},
		{http.MethodPut, path, `{"overrides":[]}`},
		{http.MethodDelete, path, ""},
	} {
		requireProblem(t, do(t, h, c.method, c.path, c.body, bearer(memberToken)), TypePermissionDenied)
	}
	if svc.writes != 0 || svc.lastQuery != (handlers.SectionOverridesQuery{}) {
		t.Fatalf("a member reached the service: %+v", svc.lastQuery)
	}
}

func TestAdminProfileSectionsReadReplaceAndResetOneProfile(t *testing.T) {
	h, _, svc := adminProfileSectionsHandler(t)
	path := adminProfileSectionsBase + "p-kid/sections"

	list := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"id":"o-kid"`) {
		t.Fatal(list.Code, list.Body.String())
	}
	want := handlers.SectionOverridesQuery{UserID: 7, ProfileID: "p-kid", Scope: "home"}
	if svc.lastQuery != want {
		t.Fatalf("query = %+v, want %+v", svc.lastQuery, want)
	}

	settings := do(t, h, http.MethodGet, path+"/settings", "", actingRequestAdmin)
	if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), `"is_custom":true`) {
		t.Fatal(settings.Code, settings.Body.String())
	}

	saved := do(t, h, http.MethodPut, path, `{"overrides":[{"id":"o-a","section_id":"s-recent","position":0},{"id":"o-b","section_id":"s-continue","position":1,"hidden":true}]}`, actingRequestAdmin)
	if saved.Code != http.StatusNoContent || saved.Body.Len() != 0 {
		t.Fatal(saved.Code, saved.Body.String())
	}
	if len(svc.lastWrites) != 2 || svc.lastWrites[0].SectionID != "s-recent" || !svc.lastWrites[1].Hidden || *svc.lastWrites[1].Position != 1 {
		t.Fatalf("writes = %+v", svc.lastWrites)
	}
	if svc.lastQuery != want {
		t.Fatalf("save query = %+v", svc.lastQuery)
	}
	if got := svc.rows["p-owner"]; len(got) != len(fixtureSectionOverrides()) {
		t.Fatalf("another profile changed: %+v", got)
	}

	// The same omitted-versus-null rule as the profile's own route.
	requireProblem(t, do(t, h, http.MethodPut, path, `{"overrides":[{"section_id":"s-continue","hidden":null}]}`, actingRequestAdmin), TypeValidationFailed)

	reset := do(t, h, http.MethodDelete, path, "", actingRequestAdmin)
	if reset.Code != http.StatusNoContent {
		t.Fatal(reset.Code, reset.Body.String())
	}
	if len(svc.rows["p-kid"]) != 0 || len(svc.rows["p-owner"]) == 0 {
		t.Fatalf("reset = %+v", svc.rows)
	}
	if svc.writes != 2 {
		t.Fatalf("writes = %d, want 2", svc.writes)
	}
}

func TestAdminProfileSectionsRefuseAnUnknownAccountOrProfile(t *testing.T) {
	h, accounts, svc := adminProfileSectionsHandler(t)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		body := ""
		if method == http.MethodPut {
			body = `{"overrides":[]}`
		}
		// A profile that is not the account's.
		requireProblem(t, do(t, h, method, adminProfileSectionsBase+"p-elsewhere/sections", body, actingRequestAdmin), TypeNotFound)
	}
	requireProblem(t, do(t, h, http.MethodGet, adminProfileSectionsBase+"p-elsewhere/sections/settings", "", actingRequestAdmin), TypeNotFound)

	accounts.err = auth.ErrNotFound
	requireProblem(t, do(t, h, http.MethodPut, adminProfileSectionsBase+"p-kid/sections", `{"overrides":[]}`, actingRequestAdmin), TypeNotFound)
	accounts.err = nil

	requireProblem(t, do(t, h, http.MethodGet, adminProfileSectionsBase+"p-kid/sections?scope=library", "", actingRequestAdmin), TypeValidationFailed)
	if svc.writes != 0 {
		t.Fatalf("a refused request wrote: %d", svc.writes)
	}
}

func TestAdminAccountCapabilitiesReportProfileSections(t *testing.T) {
	h, _, _ := adminProfileSectionsHandler(t)
	reply := do(t, h, http.MethodGet, Prefix+"/admin/users/capabilities", "", actingRequestAdmin)
	if reply.Code != http.StatusOK || !strings.Contains(reply.Body.String(), `"profile_sections":true`) {
		t.Fatal(reply.Code, reply.Body.String())
	}

	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = fixtureAdminAccounts()
	reply = do(t, NewHandler(deps), http.MethodGet, Prefix+"/admin/users/capabilities", "", actingRequestAdmin)
	if reply.Code != http.StatusOK || !strings.Contains(reply.Body.String(), `"profile_sections":false`) {
		t.Fatal(reply.Code, reply.Body.String())
	}
}
