package requests

import (
	"context"
	"errors"
	"sync/atomic"
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

	section, err := service.Discover(context.Background(), testViewer(1), "trending_movies", 1)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
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

// TestDiscoverFilterDropsNRTitles pins the regression the TMDB-side
// certification.lte pre-filter cannot catch: TMDB ranks "NR" below "G", and a
// title with several US cert entries matches when any one qualifies. The
// picked certification for such titles is "NR", which the ladder rejects.
func TestDiscoverFilterDropsNRTitles(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &certTMDBClient{
		fakeTMDBClient: fakeTMDBClient{page: &tmdb.MediaPage{
			Page: 1,
			Results: []tmdb.MediaResult{
				{ID: 10, MediaType: "movie", Title: "Explicitly NR"},
				{ID: 11, MediaType: "movie", Title: "Clean G"},
			},
		}},
		certs: map[int]string{10: "NR", 11: "G"},
	}
	service := newRatedService(store, client, nil, "G")

	section, err := service.Discover(context.Background(), testViewer(1), "trending_movies", 1)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(section.Results) != 1 || section.Results[0].TMDBID != 11 {
		t.Fatalf("results = %+v, want NR title dropped", section.Results)
	}
}

func TestDiscoverUnrestrictedViewerSkipsCertificationLookups(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &certTMDBClient{
		fakeTMDBClient: fakeTMDBClient{page: discoverTestPage()},
	}
	service := newRatedService(store, client, nil, "")

	section, err := service.Discover(context.Background(), testViewer(1), "trending_movies", 1)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(section.Results) != 4 {
		t.Fatalf("results = %d, want all 4 unfiltered", len(section.Results))
	}
	if calls := client.certCalls.Load(); calls != 0 {
		t.Fatalf("certification calls = %d, want 0 for an unrestricted viewer", calls)
	}
}

func TestDiscoverCertificationErrorPropagates(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &certTMDBClient{
		fakeTMDBClient: fakeTMDBClient{page: discoverTestPage()},
		certErr:        errors.New("tmdb unavailable"),
	}
	service := newRatedService(store, client, nil, "PG")

	// A failed certification lookup must surface as an error, not as a
	// silently empty (fail-closed) page that reads as "no matches".
	if _, err := service.Discover(context.Background(), testViewer(1), "trending_movies", 1); err == nil {
		t.Fatal("Discover succeeded, want certification error to propagate")
	}
}

// pagedCertTMDBClient serves distinct section pages so backfill behavior is
// observable: which TMDB pages were fetched and what survived.
type pagedCertTMDBClient struct {
	certTMDBClient
	pages       map[int]*tmdb.MediaPage
	fetchedPage []int
}

func (f *pagedCertTMDBClient) DiscoverSection(_ context.Context, _ string, page int) (*tmdb.MediaPage, error) {
	f.mu.Lock()
	f.fetchedPage = append(f.fetchedPage, page)
	f.mu.Unlock()
	if p, ok := f.pages[page]; ok {
		return p, nil
	}
	return &tmdb.MediaPage{Page: page, TotalPages: len(f.pages), Results: nil}, nil
}

func TestDiscoverBackfillsSectionFromLaterPages(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	// 3 TMDB pages; one G title per page among R titles. Ceiling PG should
	// walk the whole window (pages 1-5, capped at TotalPages=3) and surface
	// all three G titles on Silo page 1.
	pages := map[int]*tmdb.MediaPage{}
	certs := map[int]string{}
	for p := 1; p <= 3; p++ {
		var results []tmdb.MediaResult
		for i := 0; i < 20; i++ {
			id := p*100 + i
			results = append(results, tmdb.MediaResult{ID: id, MediaType: "movie", Title: "Movie"})
			if i == 0 {
				certs[id] = "G"
			} else {
				certs[id] = "R"
			}
		}
		pages[p] = &tmdb.MediaPage{Page: p, TotalPages: 3, TotalResults: 60, Results: results}
	}
	client := &pagedCertTMDBClient{
		certTMDBClient: certTMDBClient{certs: certs},
		pages:          pages,
	}
	service := NewService(store, client, &fakePresence{})
	service.SetEntitlementResolver(ratedCeiling{rating: "PG"})

	section, err := service.Discover(context.Background(), testViewer(1), "trending_movies", 1)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	ids := make([]int, 0, len(section.Results))
	for _, r := range section.Results {
		ids = append(ids, r.TMDBID)
	}
	if len(ids) != 3 || ids[0] != 100 || ids[1] != 200 || ids[2] != 300 {
		t.Fatalf("result ids = %v, want [100 200 300] (one survivor per TMDB page)", ids)
	}
	if got := client.fetchedPage; len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("fetched TMDB pages = %v, want [1 2 3] (stop at TotalPages)", got)
	}
	if section.TotalPages != 3 {
		t.Fatalf("TotalPages = %d, want TMDB's 3", section.TotalPages)
	}
	// All upstream pages were consumed within budget — nothing left to resume.
	if section.NextPage != 0 {
		t.Fatalf("NextPage = %d, want 0 (exhausted)", section.NextPage)
	}
}

func TestDiscoverBackfillStopsWhenPageFull(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	// Page 1 survives fully; the window must stop after it.
	var results []tmdb.MediaResult
	certs := map[int]string{}
	for i := 0; i < 20; i++ {
		results = append(results, tmdb.MediaResult{ID: 100 + i, MediaType: "movie", Title: "Movie"})
		certs[100+i] = "G"
	}
	client := &pagedCertTMDBClient{
		certTMDBClient: certTMDBClient{certs: certs},
		pages: map[int]*tmdb.MediaPage{
			1: {Page: 1, TotalPages: 500, TotalResults: 10000, Results: results},
		},
	}
	service := NewService(store, client, &fakePresence{})
	service.SetEntitlementResolver(ratedCeiling{rating: "PG"})

	section, err := service.Discover(context.Background(), testViewer(1), "trending_movies", 1)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(section.Results) != 20 {
		t.Fatalf("results = %d, want 20", len(section.Results))
	}
	if got := client.fetchedPage; len(got) != 1 || got[0] != 1 {
		t.Fatalf("fetched TMDB pages = %v, want just [1] (page already full)", got)
	}
}

func TestDiscoverBackfillNextPageResumesWithoutSkippingOrRepeating(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	certs := map[int]string{}
	pages := map[int]*tmdb.MediaPage{}
	for p := 1; p <= 10; p++ {
		id := p * 100
		certs[id] = "G"
		pages[p] = &tmdb.MediaPage{Page: p, TotalPages: 10, TotalResults: 200,
			Results: []tmdb.MediaResult{{ID: id, MediaType: "movie", Title: "Movie"}}}
	}
	client := &pagedCertTMDBClient{
		certTMDBClient: certTMDBClient{certs: certs},
		pages:          pages,
	}
	service := NewService(store, client, &fakePresence{})
	service.SetEntitlementResolver(ratedCeiling{rating: "PG"})

	// One survivor per page and a budget of 5: page 1 consumes TMDB 1-5.
	first, err := service.Discover(context.Background(), testViewer(1), "trending_movies", 1)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if first.NextPage != 6 {
		t.Fatalf("NextPage = %d, want 6", first.NextPage)
	}
	client.fetchedPage = nil
	second, err := service.Discover(context.Background(), testViewer(1), "trending_movies", first.NextPage)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if got := client.fetchedPage; len(got) != 5 || got[0] != 6 || got[4] != 10 {
		t.Fatalf("fetched TMDB pages = %v, want [6 7 8 9 10]", got)
	}
	var ids []int
	for _, r := range append(first.Results, second.Results...) {
		ids = append(ids, r.TMDBID)
	}
	if len(ids) != 10 || ids[0] != 100 || ids[9] != 1000 {
		t.Fatalf("combined ids = %v, want 100..1000 with no gap or repeat", ids)
	}
	if second.NextPage != 0 {
		t.Fatalf("NextPage after last page = %d, want 0", second.NextPage)
	}
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
		section, err := service.Discover(context.Background(), testViewer(1), "trending_movies", page)
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

// countingCeiling counts scope resolutions; the production resolver hits the
// user store and policy engine per call, so call count is a real cost.
type countingCeiling struct {
	rating string
	calls  atomic.Int64
}

func (f *countingCeiling) MaxPlaybackQuality(context.Context, int, string) (string, error) {
	return "", nil
}

func (f *countingCeiling) MaxContentRating(context.Context, int, string) (string, error) {
	f.calls.Add(1)
	return f.rating, nil
}

func TestDiscoverAllResolvesCeilingOnce(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &pagedCertTMDBClient{
		certTMDBClient: certTMDBClient{certs: map[int]string{}},
		pages: map[int]*tmdb.MediaPage{
			1: {Page: 1, TotalPages: 1, Results: nil},
		},
	}
	service := NewService(store, client, &fakePresence{})
	ceiling := &countingCeiling{rating: "PG"}
	service.SetEntitlementResolver(ceiling)

	if _, err := service.DiscoverAll(context.Background(), testViewer(1)); err != nil {
		t.Fatalf("DiscoverAll returned error: %v", err)
	}
	if got := ceiling.calls.Load(); got != 1 {
		t.Fatalf("ceiling resolutions = %d, want 1 for the whole DiscoverAll", got)
	}
}

func TestDiscoverAllUsesSmallerBackfillBudget(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	// Nothing survives filtering, so backfill always runs to its budget.
	pages := map[int]*tmdb.MediaPage{}
	for p := 1; p <= 10; p++ {
		pages[p] = &tmdb.MediaPage{Page: p, TotalPages: 10, TotalResults: 200,
			Results: []tmdb.MediaResult{{ID: p * 100, MediaType: "movie", Title: "Movie"}}}
	}
	client := &pagedCertTMDBClient{
		certTMDBClient: certTMDBClient{certs: map[int]string{}}, // all unrated -> dropped
		pages:          pages,
	}
	service := NewService(store, client, &fakePresence{})
	service.SetEntitlementResolver(ratedCeiling{rating: "PG"})

	sections, err := service.DiscoverAll(context.Background(), testViewer(1))
	if err != nil {
		t.Fatalf("DiscoverAll returned error: %v", err)
	}
	// 6 sections x aggregate budget of 2 pages = 12 list fetches, bounding the
	// cold-path cost the review flagged (vs 6 x 5 = 30 at the single budget).
	want := len(sections) * sectionBackfillBudgetAggregate
	if got := len(client.fetchedPage); got != want {
		t.Fatalf("total TMDB page fetches = %d, want %d", got, want)
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

// TestGetDetailIgnoresForeignDisplayRating pins the round-2 review finding:
// the guard compares the US-only enforcement certification, not the detail
// payload's display rating. A foreign "PG" (same string as US PG) in the
// display field must not admit a title whose US certification is unresolved.
func TestGetDetailIgnoresForeignDisplayRating(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &certTMDBClient{
		fakeTMDBClient: fakeTMDBClient{detail: &tmdb.MediaDetail{
			MediaType:     "movie",
			ID:            77,
			Title:         "Foreign Only",
			ContentRating: "PG", // display fallback from a non-US country
		}},
		certs: map[int]string{77: ""}, // US-only enforcement lookup: unresolved
	}
	service := newRatedService(store, client, nil, "PG")

	if _, err := service.GetDetail(context.Background(), testViewer(1), MediaTypeMovie, 77); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDetail error = %v, want ErrNotFound (foreign display rating must not pass)", err)
	}
}

func TestGetDetailBlocksUnratedTitleUnderCeiling(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &certTMDBClient{
		fakeTMDBClient: fakeTMDBClient{detail: &tmdb.MediaDetail{
			MediaType: "movie",
			ID:        43,
			Title:     "Unrated Movie",
		}},
	}
	service := newRatedService(store, client, nil, "PG")

	if _, err := service.GetDetail(context.Background(), testViewer(1), MediaTypeMovie, 43); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDetail error = %v, want ErrNotFound (fail closed on missing rating)", err)
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

func TestCreateRequestAllowsTitleWithinCeiling(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &certTMDBClient{certs: map[int]string{8: "PG"}}
	service := newRatedService(store, client, nil, "PG")

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    8,
		Title:     "Family Movie",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.TMDBID != 8 {
		t.Fatalf("request tmdb id = %d, want 8", req.TMDBID)
	}
}

func TestBrowsePushesCertificationCeilingToTMDB(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	client := &certTMDBClient{}
	service := newRatedService(store, client, nil, "PG-13")

	if _, err := service.BrowseStudio(context.Background(), testViewer(1), "pixar", "", 1); err != nil {
		t.Fatalf("BrowseStudio returned error: %v", err)
	}
	if got := client.gotDiscoverParams.CertificationLte; got != "PG-13" {
		t.Fatalf("movie certification.lte = %q, want PG-13", got)
	}

	if _, err := service.BrowseNetwork(context.Background(), testViewer(1), "netflix", "", 1); err != nil {
		t.Fatalf("BrowseNetwork returned error: %v", err)
	}
	if got := client.gotDiscoverParams.CertificationLte; got != "TV-14" {
		t.Fatalf("tv certification.lte = %q, want TV-14", got)
	}

	service.SetEntitlementResolver(ratedCeiling{rating: ""})
	if _, err := service.BrowseStudio(context.Background(), testViewer(1), "pixar", "", 1); err != nil {
		t.Fatalf("BrowseStudio returned error: %v", err)
	}
	if got := client.gotDiscoverParams.CertificationLte; got != "" {
		t.Fatalf("certification.lte = %q, want empty without a ceiling", got)
	}
}

func TestCertificationCeilingFor(t *testing.T) {
	cases := []struct {
		ceiling   string
		mediaType string
		want      string
	}{
		{"G", "movie", "G"},
		{"PG", "movie", "PG"},
		{"PG-13", "movie", "PG-13"},
		// Rank 3 maps to the ladder maximum: an R ceiling locally allows
		// NC-17 (same rank), and the pre-filter must stay a superset of the
		// post-filter or allowed titles vanish upstream unrecoverably.
		{"R", "movie", "NC-17"},
		{"NC-17", "movie", "NC-17"},
		{"TV-G", "tv", "TV-G"},
		{"TV-Y7", "tv", "TV-PG"},
		{"TV-14", "tv", "TV-14"},
		{"TV-MA", "tv", "TV-MA"},
		// The ceiling field holds one value spanning both ladders, so a
		// movie-scale ceiling must map onto the TV ladder and vice versa.
		{"PG-13", "tv", "TV-14"},
		{"TV-14", "movie", "PG-13"},
		{"", "movie", ""},
		{"BOGUS", "movie", ""},
		{"", "tv", ""},
	}
	for _, tc := range cases {
		if got := certificationCeilingFor(tc.ceiling, tc.mediaType); got != tc.want {
			t.Errorf("certificationCeilingFor(%q, %q) = %q, want %q", tc.ceiling, tc.mediaType, got, tc.want)
		}
	}
}
