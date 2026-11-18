package requests

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

func newRatedService(store *fakeStore, client *certTMDBClient, presence *fakePresence, ceiling string) *Service {
	if presence == nil {
		presence = &fakePresence{}
	}
	service := NewService(store, client, presence)
	service.SetEntitlementResolver(ratedCeiling{rating: ceiling})
	service.SetUserRepository(requestUserRepo{})
	return service
}

func discoverTestPage() *tmdb.MediaPage {
	return &tmdb.MediaPage{
		Page:         1,
		TotalPages:   10,
		TotalResults: 200,
		Results: []tmdb.MediaResult{
			{ID: 1, MediaType: "movie", Title: "Family Movie"},
			{ID: 2, MediaType: "movie", Title: "Adult Movie"},
			{ID: 3, MediaType: "movie", Title: "Unrated Movie"},
			{ID: 4, MediaType: "movie", Title: "Foreign Cert Movie"},
		},
	}
}

func TestDiscoverFiltersResultsAboveCeiling(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &certTMDBClient{
		fakeTMDBClient: fakeTMDBClient{page: discoverTestPage()},
		certs: map[int]string{
			1: "G",
			2: "R",
			3: "",       // no certification on TMDB
			4: "FSK 16", // foreign-only certification, unknown to the ladder
		},
	}
	presence := &fakePresence{}
	service := newRatedService(store, client, presence, "PG")

	section, err := service.DiscoverRow(context.Background(), testViewer(1), "trending_movies", 1)
	if err != nil {
		t.Fatalf("DiscoverRow returned error: %v", err)
	}
	if len(section.Results) != 1 || section.Results[0].TMDBID != 1 {
		t.Fatalf("results = %+v, want only the G-rated title", section.Results)
	}
	// TMDB's totals pass through untouched (page keeps TMDB cursor semantics).
	if section.TotalPages != 10 || section.TotalResults != 200 {
		t.Fatalf("totals = %d/%d, want TMDB's 10/200", section.TotalPages, section.TotalResults)
	}
	// Filtering runs before presence lookup, so hidden titles never reach it.
	for _, candidate := range presence.got {
		if candidate.TMDBID != 1 {
			t.Fatalf("presence saw filtered-out candidate %+v", candidate)
		}
	}
}

// pagedCertTMDBClient serves distinct section pages so backfill behavior is
// observable: which TMDB pages were fetched and what survived.
type pagedCertTMDBClient struct {
	certTMDBClient
	pages       map[int]*tmdb.MediaPage
	fetchedPage []int
}

func (f *pagedCertTMDBClient) ChartPage(_ context.Context, _ tmdb.ChartParams, page int) (*tmdb.MediaPage, error) {
	f.mu.Lock()
	f.fetchedPage = append(f.fetchedPage, page)
	f.mu.Unlock()
	if p, ok := f.pages[page]; ok {
		return p, nil
	}
	return &tmdb.MediaPage{Page: page, TotalPages: len(f.pages), Results: nil}, nil
}

// TestDiscoverBackfillPreservesOverflowAcrossPages pins the review regression:
// with a permissive ceiling most of TMDB page 1 survives, and an early "20
// survived, stop scanning" break must not drop the un-consumed pages —
// NextPage has to resume exactly where consumption stopped.
func TestDiscoverBackfillPreservesOverflowAcrossPages(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	certs := map[int]string{}
	pages := map[int]*tmdb.MediaPage{}
	for p := 1; p <= 3; p++ {
		var results []tmdb.MediaResult
		for i := 0; i < 20; i++ {
			id := p*100 + i
			results = append(results, tmdb.MediaResult{ID: id, MediaType: "movie", Title: "Movie"})
			certs[id] = "R" // permissive ceiling: everything survives
		}
		pages[p] = &tmdb.MediaPage{Page: p, TotalPages: 3, TotalResults: 60, Results: results}
	}
	client := &pagedCertTMDBClient{
		certTMDBClient: certTMDBClient{certs: certs},
		pages:          pages,
	}
	service := NewService(store, client, &fakePresence{})
	service.SetEntitlementResolver(ratedCeiling{rating: "R"})

	seen := map[int]bool{}
	page := 1
	for hops := 0; page > 0; hops++ {
		if hops > 10 {
			t.Fatal("pagination did not terminate")
		}
		section, err := service.DiscoverRow(context.Background(), testViewer(1), "trending_movies", page)
		if err != nil {
			t.Fatalf("Discover(page=%d) returned error: %v", page, err)
		}
		for _, r := range section.Results {
			if seen[r.TMDBID] {
				t.Fatalf("title %d repeated across pages", r.TMDBID)
			}
			seen[r.TMDBID] = true
		}
		page = section.NextPage
	}
	if len(seen) != 60 {
		t.Fatalf("saw %d unique titles across all pages, want all 60 (overflow lost)", len(seen))
	}
}

func TestGetDetailBlocksTitleAboveCeiling(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &certTMDBClient{
		fakeTMDBClient: fakeTMDBClient{detail: &tmdb.MediaDetail{
			MediaType:     "movie",
			ID:            42,
			Title:         "Adult Movie",
			ContentRating: "R",
		}},
		certs: map[int]string{42: "R"},
	}
	service := newRatedService(store, client, nil, "PG")

	if _, err := service.GetDetail(context.Background(), testViewer(1), MediaTypeMovie, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDetail error = %v, want ErrNotFound", err)
	}

	// Same title is visible without a ceiling.
	service.SetEntitlementResolver(ratedCeiling{rating: ""})
	detail, err := service.GetDetail(context.Background(), testViewer(1), MediaTypeMovie, 42)
	if err != nil {
		t.Fatalf("GetDetail returned error: %v", err)
	}
	if detail.TMDBID != 42 {
		t.Fatalf("detail id = %d, want 42", detail.TMDBID)
	}
}

func TestCreateRequestBlocksTitleAboveCeiling(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &certTMDBClient{certs: map[int]string{7: "R"}}
	service := newRatedService(store, client, nil, "PG")

	_, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    7,
		Title:     "Adult Movie",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateRequest error = %v, want ErrForbidden", err)
	}
	if store.count != 0 {
		t.Fatalf("stored requests = %d, want 0", store.count)
	}
}
