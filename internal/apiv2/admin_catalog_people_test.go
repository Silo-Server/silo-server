package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminPeople struct {
	calls  int
	id     int64
	update handlers.UpdatePersonRequest
	err    error
}

func (f *fakeAdminPeople) RefreshAdminPerson(_ context.Context, id int64) (handlers.PersonView, error) {
	f.calls++
	f.id = id
	return handlers.PersonView{ID: id, Name: "Refreshed"}, f.err
}
func (f *fakeAdminPeople) UpdateAdminPerson(_ context.Context, id int64, b handlers.UpdatePersonRequest) (handlers.PersonView, error) {
	f.calls++
	f.id = id
	f.update = b
	return handlers.PersonView{ID: id, Name: "Updated"}, f.err
}
func TestAdminPeopleTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminPeople{}
	deps.AdminPeople = f
	h := newTestHandler(t, deps)
	rec := do(t, h, "POST", Prefix+"/admin/people/7/refresh", "", bearer(adminToken))
	if rec.Code != 200 || f.calls != 1 || f.id != 7 || !strings.Contains(rec.Body.String(), `"id":"7"`) {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "PATCH", Prefix+"/admin/people/7", `{"name":"Updated","birth_date":"","bio":null}`, bearer(adminToken))
	if rec.Code != 200 || f.calls != 2 || f.update.Name == nil || *f.update.Name != "Updated" || f.update.BirthDate == nil || *f.update.BirthDate != "" || f.update.Bio != nil {
		t.Fatalf("update: %d %s %#v", rec.Code, rec.Body, f.update)
	}
	for _, tc := range []struct{ method, path, body string }{{"POST", "/7/refresh", ""}, {"PATCH", "/7", `{"name":"Other"}`}} {
		before := f.calls
		rec = do(t, h, tc.method, Prefix+"/admin/people"+tc.path, tc.body, bearer(memberToken))
		if rec.Code != 403 || f.calls != before {
			t.Fatalf("member: %d calls=%d", rec.Code, f.calls)
		}
	}
	before := f.calls
	rec = do(t, h, "PATCH", Prefix+"/admin/people/not-an-id", `{}`, bearer(adminToken))
	if rec.Code != 422 || f.calls != before {
		t.Fatalf("invalid ID: %d %s", rec.Code, rec.Body)
	}
	f.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Invalid birth_date", Field: "birth_date"}
	rec = do(t, h, "PATCH", Prefix+"/admin/people/7", `{"birth_date":"bad"}`, bearer(adminToken))
	if rec.Code != 422 {
		t.Fatalf("date error: %d %s", rec.Code, rec.Body)
	}
	f.err = &handlers.APIError{Status: http.StatusBadGateway, Code: "provider_error", Message: "No person metadata found"}
	rec = do(t, h, "POST", Prefix+"/admin/people/7/refresh", "", bearer(adminToken))
	if rec.Code != 503 {
		t.Fatalf("provider: %d %s", rec.Code, rec.Body)
	}
	deps.AdminPeople = nil
	h = newTestHandler(t, deps)
	rec = do(t, h, "POST", Prefix+"/admin/people/7/refresh", "", bearer(adminToken))
	if rec.Code != 503 {
		t.Fatalf("unwired: %d %s", rec.Code, rec.Body)
	}
}
func adminCatalogPeopleFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_person_refreshed", operationID: "refreshAdminPerson", method: "POST", path: Prefix + "/admin/people/7/refresh", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/Person", assertHeaders: []string{"Content-Type"}, scenario: "Administrator refresh waits and returns the person with an opaque string ID."},
		{name: "admin_person_updated", operationID: "updateAdminPerson", method: "PATCH", path: Prefix + "/admin/people/7", body: `{"name":"Updated","birth_date":""}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/Person", assertHeaders: []string{"Content-Type"}, scenario: "Partial update returns after persistence; empty dates clear."},
	}
}
