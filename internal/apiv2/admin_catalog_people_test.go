package apiv2

import (
	"context"

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
func adminCatalogPeopleFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_person_refreshed", operationID: "refreshAdminPerson", method: "POST", path: Prefix + "/admin/people/7/refresh", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/Person", assertHeaders: []string{"Content-Type"}, scenario: "Administrator refresh waits and returns the person with an opaque string ID."},
		{name: "admin_person_updated", operationID: "updateAdminPerson", method: "PATCH", path: Prefix + "/admin/people/7", body: `{"name":"Updated","birth_date":""}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/Person", assertHeaders: []string{"Content-Type"}, scenario: "Partial update returns after persistence; empty dates clear."},
	}
}
