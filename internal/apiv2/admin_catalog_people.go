package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminPeopleService interface {
	RefreshAdminPerson(context.Context, int64) (handlers.PersonView, error)
	UpdateAdminPerson(context.Context, int64, handlers.UpdatePersonRequest) (handlers.PersonView, error)
}
type AdminPersonUpdate struct {
	Name       *string `json:"name,omitempty" nullable:"true"`
	Bio        *string `json:"bio,omitempty" nullable:"true"`
	BirthDate  *string `json:"birth_date,omitempty" nullable:"true" doc:"YYYY-MM-DD; empty clears; null or omission preserves."`
	DeathDate  *string `json:"death_date,omitempty" nullable:"true" doc:"YYYY-MM-DD; empty clears; null or omission preserves."`
	Birthplace *string `json:"birthplace,omitempty" nullable:"true"`
	Homepage   *string `json:"homepage,omitempty" nullable:"true"`
	TmdbID     *string `json:"tmdb_id,omitempty" nullable:"true"`
	ImdbID     *string `json:"imdb_id,omitempty" nullable:"true"`
	TvdbID     *string `json:"tvdb_id,omitempty" nullable:"true"`
}
type AdminPersonUpdateInput struct {
	ID   ID `path:"id"`
	Body AdminPersonUpdate
}

func registerAdminCatalogPeople(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/people/{id}"+path, id, "admin-catalog", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: isMutatingMethod(method), RetrySafety: RetrySafetyNonRetryable}
		o.MaxBodyBytes = 1 << 20
		return o
	}
	Register(reg, op(http.MethodPost, "/refresh", "refreshAdminPerson", "Wait for a provider refresh and return the updated person."), reg.refreshAdminPerson)
	Register(reg, op(http.MethodPatch, "", "updateAdminPerson", "Apply a partial person metadata update."), reg.updateAdminPerson)
}
func (reg *Registry) refreshAdminPerson(ctx context.Context, in *PersonInput) (*PersonOutput, error) {
	if reg.deps.AdminPeople == nil {
		return nil, unavailable("people administration")
	}
	id, p := in.ID.positive(locationPathID)
	if p != nil {
		return nil, p
	}
	person, err := reg.deps.AdminPeople.RefreshAdminPerson(ctx, int64(id))
	if err != nil {
		return nil, collectionProblem(err)
	}
	return &PersonOutput{Body: personOf(person)}, nil
}
func (reg *Registry) updateAdminPerson(ctx context.Context, in *AdminPersonUpdateInput) (*PersonOutput, error) {
	if reg.deps.AdminPeople == nil {
		return nil, unavailable("people administration")
	}
	id, p := in.ID.positive(locationPathID)
	if p != nil {
		return nil, p
	}
	b := in.Body
	person, err := reg.deps.AdminPeople.UpdateAdminPerson(ctx, int64(id), handlers.UpdatePersonRequest{Name: b.Name, Bio: b.Bio, BirthDate: b.BirthDate, DeathDate: b.DeathDate, Birthplace: b.Birthplace, Homepage: b.Homepage, TmdbID: b.TmdbID, ImdbID: b.ImdbID, TvdbID: b.TvdbID})
	if err != nil {
		return nil, collectionProblem(err)
	}
	return &PersonOutput{Body: personOf(person)}, nil
}
