package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

type personTotalsSource struct {
	includeTotal bool
	limit        int
	offset       int
	opts         catalog.PersonSearchOptions
}

func (s *personTotalsSource) SearchVisibleWithOptions(ctx context.Context, opts catalog.PersonSearchOptions) ([]models.Person, int, error) {
	s.opts = opts
	return s.SearchVisible(ctx, opts.Term, opts.Exact, opts.Limit, opts.Offset, opts.Filter, opts.IncludeTotal)
}

func (s *personTotalsSource) SearchVisible(_ context.Context, _ string, _ bool, limit, offset int, _ catalog.AccessFilter, includeTotal bool) ([]models.Person, int, error) {
	s.includeTotal, s.limit, s.offset = includeTotal, limit, offset
	total := 0
	if includeTotal {
		total = 5
	}
	return []models.Person{{ID: 1, Name: "Visible Person"}}, total, nil
}

func TestListingTotalRecordCountControls(t *testing.T) {
	for _, tc := range []struct {
		name, query  string
		includeTotal bool
	}{
		{name: "default", includeTotal: true},
		{name: "enabled", query: "&EnableTotalRecordCount=true", includeTotal: true},
		{name: "disabled", query: "&EnableTotalRecordCount=false"},
		{name: "case insensitive", query: "&enabletotalrecordcount=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codec := NewResourceIDCodec()
			upcoming := &upcomingContractRepo{}
			items := &ItemsHandler{episodeRepo: upcoming, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}}
			people := &personTotalsSource{}
			persons := &PersonsHandler{personRepo: people, codec: codec}
			for _, endpoint := range []struct {
				path    string
				handler http.HandlerFunc
			}{
				{path: "/Shows/Upcoming", handler: items.HandleUpcoming},
				{path: "/Persons", handler: persons.HandleGetPersons},
			} {
				t.Run(endpoint.path, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodGet, endpoint.path+"?StartIndex=2&Limit=1"+tc.query, nil)
					req = req.WithContext(context.WithValue(t.Context(), compatSessionKey, collectionsTestSession()))
					rec := httptest.NewRecorder()
					endpoint.handler(rec, req)
					if rec.Code != http.StatusOK {
						t.Fatalf("response: %d %s", rec.Code, rec.Body.String())
					}
					var result queryResultDTO
					if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					wantTotal := 0
					if tc.includeTotal {
						wantTotal = 5
					}
					if result.TotalRecordCount != wantTotal || result.StartIndex != 2 || len(result.Items) != 1 || result.Items[0].ID == "" {
						t.Fatalf("total control changed the selected page: %+v", result)
					}
				})
			}
			if upcoming.includeTotal != tc.includeTotal || people.includeTotal != tc.includeTotal || upcoming.limit != 1 || upcoming.offset != 2 || people.limit != 1 || people.offset != 2 {
				t.Fatalf("catalog query controls: upcoming=%+v people=%+v", upcoming, people)
			}
		})
	}
}

// Jellyfin 12 /Persons adds name bounds and ParentId scoping, and pages
// browse requests (no SearchTerm) beyond the substring-search cap.
func TestPersonsJellyfin12QueryOptions(t *testing.T) {
	codec := NewResourceIDCodec()
	people := &personTotalsSource{}
	allowLibrary7 := &directContentService{accessFilter: func(context.Context, int, string) catalog.AccessFilter {
		return catalog.AccessFilter{AllowedLibraryIDs: []int{7}}
	}}
	persons := &PersonsHandler{personRepo: people, codec: codec, content: allowLibrary7}
	serve := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/Persons?"+query, nil)
		req = req.WithContext(context.WithValue(t.Context(), compatSessionKey, collectionsTestSession()))
		rec := httptest.NewRecorder()
		persons.HandleGetPersons(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("response: %d %s", rec.Code, rec.Body.String())
		}
		return rec
	}

	serve("NameStartsWith=Ha&NameLessThan=Hz&NameStartsWithOrGreater=Hb&Limit=60&ParentId=" + codec.EncodeIntID(EncodedIDLibrary, 7))
	if people.opts.NameStartsWith != "Ha" || people.opts.NameLessThan != "Hz" || people.opts.NameStartsWithOrGreater != "Hb" || people.opts.LibraryID != 7 || people.opts.Limit != 60 {
		t.Fatalf("browse options = %+v", people.opts)
	}
	serve("SearchTerm=hanks&Limit=60")
	if people.opts.Limit != auxSearchMaxResults || people.opts.LibraryID != 0 {
		t.Fatalf("search keeps the aux cap: %+v", people.opts)
	}
	for _, limit := range []string{"5000", "0"} {
		serve("Limit=" + limit)
		if people.opts.Limit != personBrowseMaxResults {
			t.Fatalf("browse Limit=%s = %d, want the default page %d", limit, people.opts.Limit, personBrowseMaxResults)
		}
	}
	serve("ParentId=" + codec.EncodeStringID(EncodedIDItem, "series-9"))
	if people.opts.ContentID != "series-9" {
		t.Fatalf("item parent = %+v", people.opts)
	}

	people.opts = catalog.PersonSearchOptions{Limit: -1}
	rec := serve("ParentId=" + codec.EncodeStringID(EncodedIDSeason, "season-1"))
	if people.opts.Limit != -1 || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("an unsupported parent must not query people: %+v", people.opts)
	}

	// Items shared with a visible library must not reveal who is credited in
	// a library the viewer cannot browse.
	rec = serve("ParentId=" + codec.EncodeIntID(EncodedIDLibrary, 9))
	var result queryResultDTO
	if people.opts.Limit != -1 || json.Unmarshal(rec.Body.Bytes(), &result) != nil || len(result.Items) != 0 {
		t.Fatalf("a hidden library parent queried people: %+v, %s", people.opts, rec.Body.String())
	}
}
