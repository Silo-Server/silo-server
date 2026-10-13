package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// matureOnlyItemPersonRepo stands in for PersonRepository.GetVisible: the
// person's only credit is a TV-MA title, so a viewer under a rating ceiling
// cannot see them.
type matureOnlyItemPersonRepo struct {
	person  models.Person
	filters []catalog.AccessFilter
}

func (r *matureOnlyItemPersonRepo) GetVisible(_ context.Context, id int64, filter catalog.AccessFilter) (*models.Person, error) {
	r.filters = append(r.filters, filter)
	if id != r.person.ID || filter.MaxContentRating != "" {
		return nil, pgx.ErrNoRows
	}
	p := r.person
	return &p, nil
}

func (r *matureOnlyItemPersonRepo) EnsureAccessible(context.Context, int64, catalog.AccessFilter) error {
	return nil
}

func (r *matureOnlyItemPersonRepo) CountItemsByType(context.Context, int64) (map[string]int, error) {
	return map[string]int{"series": 1}, nil
}

// TestPersonItemHonorsViewerAccess covers GET /Items/{personId}: a profile
// that can see none of the person's credits gets the same 404 as for an
// unknown person, as native person detail answers.
func TestPersonItemHonorsViewerAccess(t *testing.T) {
	codec := NewResourceIDCodec()
	repo := &matureOnlyItemPersonRepo{person: models.Person{ID: 17419, Name: "Bryan Cranston", Bio: "Actor"}}
	h := &ItemsHandler{
		codec:      codec,
		mapper:     newMapper(codec, &config.Config{}),
		personRepo: repo,
		accessFilter: func(_ context.Context, _ int, profileID string) catalog.AccessFilter {
			if profileID == "child" {
				return catalog.AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "TV-Y7"}}
			}
			return catalog.AccessFilter{}
		},
	}
	routeID := codec.EncodeIntID(EncodedIDPerson, repo.person.ID)
	get := func(profile string, personID int64) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID, nil)
		h.handlePersonItem(rec, req, &Session{StreamAppUserID: 7, ProfileID: profile}, routeID, personID)
		return rec
	}

	if rec := get("adult", repo.person.ID); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Cranston") {
		t.Fatalf("visible person: %d %s", rec.Code, rec.Body.String())
	}
	hidden := get("child", repo.person.ID)
	if hidden.Code != http.StatusNotFound || strings.Contains(hidden.Body.String(), "Cranston") || strings.Contains(hidden.Body.String(), "Actor") {
		t.Fatalf("hidden person: %d %s", hidden.Code, hidden.Body.String())
	}
	if unknown := get("child", 1); unknown.Code != hidden.Code || unknown.Body.String() != hidden.Body.String() {
		t.Fatalf("hidden person answered %d %q, unknown person %d %q", hidden.Code, hidden.Body.String(), unknown.Code, unknown.Body.String())
	}
	if len(repo.filters) != 3 || repo.filters[1].MaxContentRating != "TV-Y7" {
		t.Fatalf("filters = %+v, want the viewer's access on every lookup", repo.filters)
	}
}
