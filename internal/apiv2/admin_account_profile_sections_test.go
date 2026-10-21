package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
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
