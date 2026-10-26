package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type adminPeopleRepo struct {
	person models.Person
	writes int
}

func (r *adminPeopleRepo) Get(context.Context, int64) (*models.Person, error) { return &r.person, nil }
func (r *adminPeopleRepo) GetVisible(context.Context, int64, catalog.AccessFilter) (*models.Person, error) {
	return &r.person, nil
}
func (r *adminPeopleRepo) SearchAlphabetical(context.Context, string, int, catalog.AccessFilter) ([]models.Person, error) {
	return nil, nil
}
func (r *adminPeopleRepo) SearchScoped(context.Context, string, int, string, catalog.AccessFilter) ([]models.Person, error) {
	return nil, nil
}
func (r *adminPeopleRepo) Update(_ context.Context, p models.Person) error {
	r.writes++
	r.person = p
	return nil
}

func TestAdminPersonUpdatePreflightAndClear(t *testing.T) {
	repo := &adminPeopleRepo{person: models.Person{ID: 1, Name: "Before", BirthDate: new(time.Date(1980, 1, 2, 0, 0, 0, 0, time.UTC))}}
	h := &PeopleHandler{personRepo: repo}
	_, err := h.UpdateAdminPerson(t.Context(), 1, UpdatePersonRequest{Name: new("After"), BirthDate: new("invalid")})
	if err == nil || repo.writes != 0 || repo.person.Name != "Before" {
		t.Fatalf("invalid update changed state: %#v %v", repo, err)
	}
	p, err := h.UpdateAdminPerson(t.Context(), 1, UpdatePersonRequest{Name: new("After"), BirthDate: new("")})
	if err != nil || repo.writes != 1 || repo.person.BirthDate != nil || repo.person.SortName != "After" || p.Name != "After" {
		t.Fatalf("clear: %#v %#v %v", repo, p, err)
	}
}
