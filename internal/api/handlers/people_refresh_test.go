package handlers

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
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

// A person the viewer can see through no credit answers what an unknown one
// does, on detail and on refresh, and nothing is queued for them.
func TestHiddenPersonAnswersLikeAnUnknownOne(t *testing.T) {
	repo := &matureOnlyPersonRepo{}
	queue := &recordingPersonRefreshQueue{}
	h := &PeopleHandler{personRepo: repo, refreshQueue: queue, refreshLimiter: ratelimit.NewMemoryLimiter()}
	kid := catalog.AccessFilter{UserID: 5, AllowedLibraryIDs: []int{3}, MaturityLimits: access.MaturityLimits{MaxContentRating: "TV-Y7"}}
	adult := catalog.AccessFilter{UserID: 5, AllowedLibraryIDs: []int{3}}

	notFound := func(name string, err error) {
		t.Helper()
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok || apiErr.Status != http.StatusNotFound {
			t.Fatalf("%s: err = %v, want 404", name, err)
		}
	}
	_, unknownErr := h.Person(t.Context(), 2, true, kid)
	notFound("unknown person", unknownErr)
	_, hiddenErr := h.Person(t.Context(), 1, true, kid)
	notFound("hidden person", hiddenErr)
	if hiddenErr.Error() != unknownErr.Error() {
		t.Fatalf("hidden person answers %q, unknown answers %q", hiddenErr, unknownErr)
	}
	if got := repo.filters[len(repo.filters)-1]; got.MaxContentRating != "TV-Y7" || !slices.Equal(got.AllowedLibraryIDs, []int{3}) {
		t.Fatalf("detail did not pass the viewer's access: %+v", got)
	}
	notFound("hidden refresh", h.RefreshPerson(t.Context(), 5, 1, kid))
	if len(queue.ids) != 0 {
		t.Fatalf("hidden person was queued: %v", queue.ids)
	}

	if _, err := h.Person(t.Context(), 1, false, adult); err != nil {
		t.Fatalf("visible person: %v", err)
	}
	if err := h.RefreshPerson(t.Context(), 5, 1, adult); err != nil || !slices.Equal(queue.ids, []int64{1}) {
		t.Fatalf("visible refresh: %v, queued %v", err, queue.ids)
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
