package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type recordingPersonRefreshQueue struct {
	ids []int64
}

func (q *recordingPersonRefreshQueue) Enqueue(id int64, _ *time.Time) {
	q.ids = append(q.ids, id)
}

func TestEnqueuePersonRefreshIfDue(t *testing.T) {
	now := time.Now()
	birthDate := now.AddDate(-30, 0, 0)
	justAttempted := now.Add(-30 * time.Second)
	attemptedLongAgo := now.Add(-catalog.PersonRefreshRetryAfter - time.Hour)

	tests := []struct {
		name   string
		person models.Person
		want   bool
	}{
		{
			name: "incomplete person never attempted",
			person: models.Person{
				ID:        1,
				Name:      "Incomplete",
				TmdbID:    "1",
				UpdatedAt: now,
			},
			want: true,
		},
		{
			name: "incomplete person attempted moments ago",
			person: models.Person{
				ID:                         2,
				Name:                       "Just Attempted",
				TmdbID:                     "2",
				UpdatedAt:                  now,
				MetadataRefreshAttemptedAt: &justAttempted,
			},
			want: false,
		},
		{
			name: "incomplete person past the retry window",
			person: models.Person{
				ID:                         3,
				Name:                       "Retry Due",
				TmdbID:                     "3",
				UpdatedAt:                  now,
				MetadataRefreshAttemptedAt: &attemptedLongAgo,
			},
			want: true,
		},
		{
			name: "fresh complete person",
			person: models.Person{
				ID:        4,
				Name:      "Fresh",
				Bio:       "Bio",
				PhotoPath: "photo.jpg",
				BirthDate: &birthDate,
				TmdbID:    "4",
				UpdatedAt: now,
			},
			want: false,
		},
		{
			name: "stale complete person",
			person: models.Person{
				ID:        5,
				Name:      "Stale",
				Bio:       "Bio",
				PhotoPath: "photo.jpg",
				BirthDate: &birthDate,
				TmdbID:    "5",
				UpdatedAt: now.Add(-catalog.PersonMetadataStaleAfter - time.Hour),
			},
			want: true,
		},
		{
			name: "stale complete person attempted moments ago",
			person: models.Person{
				ID:                         6,
				Name:                       "Stale But Attempted",
				Bio:                        "Bio",
				PhotoPath:                  "photo.jpg",
				BirthDate:                  &birthDate,
				TmdbID:                     "6",
				UpdatedAt:                  now.Add(-catalog.PersonMetadataStaleAfter - time.Hour),
				MetadataRefreshAttemptedAt: &justAttempted,
			},
			want: false,
		},
		{
			name: "person whose provider has no photo",
			person: models.Person{
				ID:        7,
				Name:      "No Photo Available",
				Bio:       "Bio",
				PhotoPath: "-",
				BirthDate: &birthDate,
				TmdbID:    "7",
				UpdatedAt: now,
			},
			want: false,
		},
		{
			name: "incomplete person without provider id",
			person: models.Person{
				ID:        8,
				Name:      "Local",
				UpdatedAt: now,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := &recordingPersonRefreshQueue{}
			handler := &PeopleHandler{refreshQueue: queue}

			handler.enqueuePersonRefreshIfDue(tt.person)

			got := len(queue.ids) == 1
			if got != tt.want {
				t.Fatalf("queued = %v, want %v", got, tt.want)
			}
		})
	}
}

type singlePersonRepo struct {
	peopleRepository
	person models.Person
}

func (r singlePersonRepo) GetVisible(context.Context, int64, catalog.AccessFilter) (*models.Person, error) {
	return &r.person, nil
}

func TestPersonQueuesRefreshOnlyForViews(t *testing.T) {
	due := models.Person{ID: 1, Name: "Incomplete", TmdbID: "1", UpdatedAt: time.Now()}
	for _, queueRefresh := range []bool{true, false} {
		queue := &recordingPersonRefreshQueue{}
		handler := &PeopleHandler{personRepo: singlePersonRepo{person: due}, refreshQueue: queue}

		if _, err := handler.Person(context.Background(), due.ID, queueRefresh, catalog.AccessFilter{}); err != nil {
			t.Fatal(err)
		}
		if queued := len(queue.ids) == 1; queued != queueRefresh {
			t.Fatalf("queueRefresh=%v queued=%v", queueRefresh, queued)
		}
	}
}

// matureOnlyPersonRepo stands in for PersonRepository.GetVisible: person 1's
// only credit is a TV-MA title, so a viewer under a rating ceiling cannot see
// them, and every other ID is unknown. Get is left unimplemented so a viewer
// path that bypasses visibility panics.
type matureOnlyPersonRepo struct {
	peopleRepository
	filters []catalog.AccessFilter
}

func (r *matureOnlyPersonRepo) GetVisible(_ context.Context, id int64, filter catalog.AccessFilter) (*models.Person, error) {
	r.filters = append(r.filters, filter)
	if id != 1 || filter.MaxContentRating != "" {
		return nil, pgx.ErrNoRows
	}
	return &models.Person{ID: 1, Name: "Bryan Cranston", Bio: "Actor", TmdbID: "17419", UpdatedAt: time.Now()}, nil
}

func (r *matureOnlyPersonRepo) SearchAlphabetical(_ context.Context, _ string, _ int, filter catalog.AccessFilter) ([]models.Person, error) {
	r.filters = append(r.filters, filter)
	if filter.MaxContentRating != "" {
		return nil, nil
	}
	return []models.Person{{ID: 1, Name: "Bryan Cranston", Bio: "Actor", TmdbID: "17419", UpdatedAt: time.Now()}}, nil
}

func TestV1PeopleRoutesHonorViewerAccess(t *testing.T) {
	repo := &matureOnlyPersonRepo{}
	queue := &recordingPersonRefreshQueue{}
	h := &PeopleHandler{personRepo: repo, itemsHandler: &ItemsHandler{}, refreshQueue: queue, refreshLimiter: ratelimit.NewMemoryLimiter()}
	router := chi.NewRouter()
	router.Get("/people", h.HandleSearch)
	router.Get("/people/{id}", h.HandleGetPerson)
	router.Post("/people/{id}/refresh", h.HandleRefreshPerson)
	serve := func(method, path string, scope access.Scope) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 5})
		req = req.WithContext(access.SetScope(ctx, scope))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	kid := access.Scope{AllowedLibraryIDs: []int{3}, MaturityLimits: access.MaturityLimits{MaxContentRating: "TV-Y7"}}
	adult := access.Scope{AllowedLibraryIDs: []int{3}}

	unknown := serve(http.MethodGet, "/people/2", kid)
	hidden := serve(http.MethodGet, "/people/1", kid)
	if hidden.Code != http.StatusNotFound || hidden.Body.String() != unknown.Body.String() || strings.Contains(hidden.Body.String(), "Cranston") {
		t.Fatalf("hidden person: %d %s, unknown: %d %s", hidden.Code, hidden.Body.String(), unknown.Code, unknown.Body.String())
	}
	got := repo.filters[len(repo.filters)-1]
	if got.MaxContentRating != "TV-Y7" || !slices.Equal(got.AllowedLibraryIDs, []int{3}) {
		t.Fatalf("detail did not pass the viewer's access: %+v", got)
	}
	if rec := serve(http.MethodGet, "/people?q=cranston", kid); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Cranston") {
		t.Fatalf("hidden person in search: %d %s", rec.Code, rec.Body.String())
	}
	if got := repo.filters[len(repo.filters)-1]; got.MaxContentRating != "TV-Y7" {
		t.Fatalf("search did not pass the viewer's access: %+v", got)
	}
	if rec := serve(http.MethodGet, "/people?q=cranston", adult); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Bryan Cranston"`) {
		t.Fatalf("visible person in search: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(http.MethodPost, "/people/1/refresh", kid); rec.Code != http.StatusNotFound {
		t.Fatalf("hidden refresh: %d %s", rec.Code, rec.Body.String())
	}
	if len(queue.ids) != 0 {
		t.Fatalf("hidden person was queued: %v", queue.ids)
	}

	visible := serve(http.MethodGet, "/people/1", adult)
	if visible.Code != http.StatusOK || !strings.Contains(visible.Body.String(), `"name":"Bryan Cranston"`) {
		t.Fatalf("visible person: %d %s", visible.Code, visible.Body.String())
	}
	viewed := len(queue.ids) // The view may queue a due refresh on its own.
	if rec := serve(http.MethodPost, "/people/1/refresh", adult); rec.Code != http.StatusAccepted || len(queue.ids) != viewed+1 {
		t.Fatalf("visible refresh: %d %s, queued %v", rec.Code, rec.Body.String(), queue.ids)
	}
}

func TestV1PersonDetailFailsClosedWithoutAccessResolver(t *testing.T) {
	h := &PeopleHandler{personRepo: &matureOnlyPersonRepo{}}
	router := chi.NewRouter()
	router.Get("/people/{id}", h.HandleGetPerson)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/people/1", nil))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "Cranston") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

type failingPersonRepo struct{ peopleRepository }

func (failingPersonRepo) GetVisible(context.Context, int64, catalog.AccessFilter) (*models.Person, error) {
	return nil, errors.New("connection refused")
}

// A lookup that fails is a server error, not "not found": the person may well
// be visible, and a 404 would tell the client otherwise.
func TestPersonLookupFailureIsNotNotFound(t *testing.T) {
	queue := &recordingPersonRefreshQueue{}
	h := &PeopleHandler{personRepo: failingPersonRepo{}, refreshQueue: queue, refreshLimiter: ratelimit.NewMemoryLimiter()}

	_, err := h.Person(context.Background(), 1, true, catalog.AccessFilter{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("Person error = %v, want 500", err)
	}
	err = h.RefreshPerson(context.Background(), 7, 1, catalog.AccessFilter{})
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("RefreshPerson error = %v, want 500", err)
	}
	if len(queue.ids) != 0 {
		t.Fatalf("queued %v after a failed lookup", queue.ids)
	}
}
