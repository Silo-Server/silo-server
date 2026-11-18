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

type singlePersonRepo struct {
	peopleRepository
	person models.Person
}

func (r singlePersonRepo) GetVisible(context.Context, int64, catalog.AccessFilter) (*models.Person, error) {
	return &r.person, nil
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
