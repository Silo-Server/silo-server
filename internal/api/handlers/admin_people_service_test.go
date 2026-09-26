package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/go-chi/chi/v5"
)

type adminPeopleRepo struct {
	person models.Person
	writes int
}

func (r *adminPeopleRepo) Get(context.Context, int64) (*models.Person, error) { return &r.person, nil }
func (r *adminPeopleRepo) Search(context.Context, string, int) ([]models.Person, error) {
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

type adminPersonRefresher struct {
	err      error
	deadline bool
}

func (f *adminPersonRefresher) RefreshPerson(ctx context.Context, id int64) (*models.Person, error) {
	_, f.deadline = ctx.Deadline()
	return &models.Person{ID: id, Name: "Refreshed"}, f.err
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
func TestAdminPersonV1ResponseSemantics(t *testing.T) {
	repo := &adminPeopleRepo{person: models.Person{ID: 1, Name: "Before"}}
	refresher := &adminPersonRefresher{}
	h := &PeopleHandler{personRepo: repo, refresher: refresher}
	router := chi.NewRouter()
	router.Patch("/people/{id}", h.HandleAdminUpdatePerson)
	router.Post("/people/{id}/refresh", h.HandleAdminRefreshPerson)
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"birth_date":"bad"}`, 400, "bad_request"}, {`{"name":null}`, 200, ""}, {`{"name":"After"}`, 200, ""},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("PATCH", "/people/1", strings.NewReader(tc.body)))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != tc.status || tc.code != "" && body["error"] != tc.code {
			t.Fatalf("patch: %d %s", rec.Code, rec.Body)
		}
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{metadata.ErrPersonNotFound, 404, "not_found"}, {metadata.ErrPersonMetadataNotFound, 502, "provider_error"}, {errors.New("private provider"), 500, "internal_error"}, {nil, 200, ""},
	} {
		refresher.err = tc.err
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/people/1/refresh", nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != tc.status || tc.code != "" && body["error"] != tc.code || !refresher.deadline {
			t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
		}
	}
}
