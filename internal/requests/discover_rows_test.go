package requests

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/requests/discoverrows"
)

// layoutStore is a fakeStore with a stored Discover layout.
type layoutStore struct {
	*fakeStore
	rows []discoverrows.Row
}

func (s *layoutStore) GetDiscoverLayout(context.Context) (DiscoverLayout, error) {
	return DiscoverLayout{Rows: s.rows, Customized: true, Revision: 1}, nil
}

func (s *layoutStore) ReplaceDiscoverLayoutConditional(context.Context, []discoverrows.Row, int64) (DiscoverLayout, error) {
	return DiscoverLayout{}, errors.New("not used")
}

func (s *layoutStore) ResetDiscoverLayoutConditional(context.Context, int64) error {
	return errors.New("not used")
}

// rowsTMDB serves charts by preset and records discover queries.
type rowsTMDB struct {
	certTMDBClient
	charts      map[string]*tmdb.MediaPage
	chartErr    error
	discoverErr error
	discovered  []tmdb.DiscoverParams
}

func (f *rowsTMDB) ChartPage(_ context.Context, params tmdb.ChartParams, _ int) (*tmdb.MediaPage, error) {
	if f.chartErr != nil {
		return nil, f.chartErr
	}
	if page, ok := f.charts[params.Preset+"/"+params.MediaType]; ok {
		return page, nil
	}
	return &tmdb.MediaPage{Page: 1, TotalPages: 1}, nil
}

func (f *rowsTMDB) DiscoverPage(_ context.Context, _ string, params tmdb.DiscoverParams, page int) (*tmdb.MediaPage, error) {
	f.mu.Lock()
	f.discovered = append(f.discovered, params)
	f.mu.Unlock()
	if f.discoverErr != nil {
		return nil, f.discoverErr
	}
	return &tmdb.MediaPage{Page: page, TotalPages: 1, Results: []tmdb.MediaResult{{ID: 500, MediaType: "movie", Title: "Found"}}}, nil
}

// cardSnapshots stores list rows' cards by source key.
type cardSnapshots map[string][]discoverrows.Card

func (c cardSnapshots) Get(_ context.Context, key string) (discoverrows.Snapshot, bool, error) {
	cards, ok := c[key]
	return discoverrows.Snapshot{Key: key, Cards: cards}, ok, nil
}

func (c cardSnapshots) Statuses(context.Context, []string) (map[string]discoverrows.SourceStatus, error) {
	return nil, nil
}

func movieCards(from, to int) []discoverrows.Card {
	var cards []discoverrows.Card
	for id := from; id <= to; id++ {
		cards = append(cards, discoverrows.Card{ID: id, MediaType: "movie", Title: fmt.Sprintf("Listed %d", id)})
	}
	return cards
}

func newLayoutService(t *testing.T, rows []discoverrows.Row, client *rowsTMDB, presence *fakePresence, ceiling string) *Service {
	t.Helper()
	normalized, errs := discoverrows.NormalizeLayout(rows)
	if len(errs) > 0 {
		t.Fatalf("layout: %v", errs)
	}
	store := &layoutStore{fakeStore: newFakeStore(), rows: normalized}
	store.settings.RequestsEnabled = true
	if presence == nil {
		presence = &fakePresence{}
	}
	service := NewService(store, client, presence)
	service.SetEntitlementResolver(ratedCeiling{rating: ceiling})
	service.SetUserRepository(requestUserRepo{})
	return service
}

func resultIDs(results []MediaResult) []int {
	ids := make([]int, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.TMDBID)
	}
	return ids
}

func TestDiscoverRowHidesAvailableTitlesAndResumes(t *testing.T) {
	listRow := discoverrows.Row{ID: "r_list", Type: discoverrows.TypeTMDBList, Title: "List", Enabled: true, HideAvailable: true, List: &discoverrows.ListSpec{URL: "https://www.themoviedb.org/list/7"}}
	shortRow := discoverrows.Row{ID: "r_short", Type: discoverrows.TypeTMDBList, Title: "Short", Enabled: true, HideAvailable: true, List: &discoverrows.ListSpec{URL: "https://www.themoviedb.org/list/8"}}
	rows := []discoverrows.Row{
		listRow,
		shortRow,
		{Type: discoverrows.TypeGenres, Title: "Genres", Enabled: true},
		{ID: "r_off", Type: discoverrows.TypeTMDBChart, Title: "Off", Chart: &discoverrows.ChartSpec{Preset: "popular", MediaType: "movie"}},
	}
	owned := map[int]bool{}
	for id := 1; id <= 15; id++ {
		owned[id] = true
	}
	service := newLayoutService(t, rows, &rowsTMDB{}, &fakePresence{available: map[MediaType]map[int]bool{MediaTypeMovie: owned}}, "")
	service.SetDiscoverLists(cardSnapshots{"tmdb_list:7": movieCards(1, 45), "tmdb_list:8": movieCards(1, 30)}, nil)

	// Page 1 of the list keeps 16-20; page 2 fills the page; page 3 waits.
	first, err := service.DiscoverRow(t.Context(), testViewer(1), "r_list", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := resultIDs(first.Results); len(got) != 25 || got[0] != 16 || got[24] != 40 || first.NextPage != 3 {
		t.Fatalf("first = %v next %d, want 16..40 and next page 3", got, first.NextPage)
	}
	if first.TotalPages != 3 || first.TotalResults != 45 {
		t.Fatalf("totals = %d/%d, want the list's 3/45", first.TotalPages, first.TotalResults)
	}
	rest, err := service.DiscoverRow(t.Context(), testViewer(1), "r_list", first.NextPage)
	if err != nil {
		t.Fatal(err)
	}
	if got := resultIDs(rest.Results); len(got) != 5 || got[0] != 41 || rest.NextPage != 0 {
		t.Fatalf("rest = %v next %d, want 41..45 and no more pages", got, rest.NextPage)
	}
	// A first read that uses up a short list says so with a cursor past the
	// end, so the client does not read page 2 again.
	short, err := service.DiscoverRow(t.Context(), testViewer(1), "r_short", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := resultIDs(short.Results); len(got) != 15 || short.TotalPages != 2 || short.NextPage != 3 {
		t.Fatalf("short = %v pages %d next %d, want 16..30 and a cursor past page 2", got, short.TotalPages, short.NextPage)
	}
	for _, id := range []string{"genres", "r_off", "missing"} {
		if _, err := service.DiscoverRow(t.Context(), testViewer(1), id, 1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("row %s: err = %v, want not found", id, err)
		}
	}
}

// The default layout leads with personal rows, which never fail; a TMDB
// outage must still fail the page so clients offer a retry.
func TestDiscoverRowsFailWhenEveryTMDBRowFails(t *testing.T) {
	outage := errors.New("tmdb down")
	service := newLayoutService(t, discoverrows.DefaultRows(), &rowsTMDB{chartErr: outage}, nil, "")
	if _, err := service.DiscoverRows(t.Context(), testViewer(1)); !errors.Is(err, outage) {
		t.Fatalf("err = %v, want the TMDB error", err)
	}
}

// failingRecommendations is a recommendations reader whose reads fail.
type failingRecommendations struct{ err error }

func (failingRecommendations) ExternalRecommendationsEnabled() bool { return true }

func (f failingRecommendations) ExternalRecommendations(context.Context, int, string, string, catalog.AccessFilter) ([]recommendations.ExternalRecommendation, error) {
	return nil, f.err
}

// A layout of recommended rows only must not answer an outage with an
// empty page.
func TestDiscoverRowsFailWhenEveryPersonalRowFails(t *testing.T) {
	outage := errors.New("database down")
	var rows []discoverrows.Row
	for _, row := range discoverrows.DefaultRows() {
		if row.Type == discoverrows.TypeRecommended {
			rows = append(rows, row)
		}
	}
	service := newLayoutService(t, rows, &rowsTMDB{}, nil, "")
	service.SetRecommendations(failingRecommendations{err: outage})
	if _, err := service.DiscoverRows(t.Context(), testViewer(1)); !errors.Is(err, outage) {
		t.Fatalf("err = %v, want the read error", err)
	}
}

func TestDiscoverRowSendsTheStricterCertification(t *testing.T) {
	for _, tt := range []struct {
		rowCeiling, viewerCeiling, want string
	}{
		{rowCeiling: "G", viewerCeiling: "PG", want: "G"},
		{rowCeiling: "R", viewerCeiling: "PG", want: "PG"},
		{rowCeiling: "R", viewerCeiling: "", want: "R"},
		{rowCeiling: "", viewerCeiling: "PG-13", want: "PG-13"},
	} {
		t.Run(tt.rowCeiling+"/"+tt.viewerCeiling, func(t *testing.T) {
			client := &rowsTMDB{certTMDBClient: certTMDBClient{certs: map[int]string{500: "G"}}}
			row := discoverrows.Row{ID: "r_kids", Type: discoverrows.TypeTMDBDiscover, Title: "Kids", Enabled: true, Discover: &discoverrows.DiscoverSpec{MediaType: "movie", SortBy: "popularity.desc", CertificationLte: tt.rowCeiling}}
			service := newLayoutService(t, []discoverrows.Row{row}, client, nil, tt.viewerCeiling)
			if _, err := service.DiscoverRow(t.Context(), testViewer(1), "r_kids", 1); err != nil {
				t.Fatal(err)
			}
			if got := client.discovered[0].CertificationLte; got != tt.want {
				t.Fatalf("certification.lte = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDiscoverLayoutDatabase(t *testing.T) {
	repo := editorTestRepository(t)
	ctx := t.Context()
	initial, err := repo.GetDiscoverLayout(ctx)
	if err != nil || initial.Customized || initial.Revision != 0 || len(initial.Rows) != len(discoverrows.DefaultRows()) {
		t.Fatalf("initial = %+v %v, want the default rows at revision 0", initial, err)
	}
	rows, errs := discoverrows.NormalizeLayout(discoverrows.DefaultRows()[:2])
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	// Two editors saving over the default: the conflict predicate lets one in.
	raceEditorWrites(t, func() error {
		_, err := repo.ReplaceDiscoverLayoutConditional(ctx, rows, 0)
		return err
	}, 1)
	saved, err := repo.GetDiscoverLayout(ctx)
	if err != nil || !saved.Customized || saved.Revision == 0 || len(saved.Rows) != 2 || saved.Rows[1].ID != rows[1].ID {
		t.Fatalf("saved = %+v %v", saved, err)
	}
	raceEditorWrites(t, func() error {
		_, err := repo.ReplaceDiscoverLayoutConditional(ctx, rows[:1], saved.Revision)
		return err
	}, 1)
	if _, err := repo.ReplaceDiscoverLayoutConditional(ctx, rows, 0); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("save over a stored layout as the default: %v, want stale", err)
	}
	if err := repo.ResetDiscoverLayoutConditional(ctx, saved.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("reset at a replaced revision: %v, want stale", err)
	}
	overwritten, err := repo.ReplaceDiscoverLayoutConditional(ctx, rows, -1)
	if err != nil || overwritten.Revision <= saved.Revision {
		t.Fatalf("overwrite = %+v %v", overwritten, err)
	}
	if err := repo.ResetDiscoverLayoutConditional(ctx, overwritten.Revision); err != nil {
		t.Fatal(err)
	}
	if reset, err := repo.GetDiscoverLayout(ctx); err != nil || reset.Customized || reset.Revision != 0 {
		t.Fatalf("after reset = %+v %v, want the default layout", reset, err)
	}
}

type declinedLayoutStore struct {
	*layoutStore
	declined map[int]bool
}

func (s *declinedLayoutStore) ListDeclinedByViewerTMDB(_ context.Context, _ Viewer, _ MediaType, tmdbIDs []int) (map[int]bool, error) {
	out := map[int]bool{}
	for _, id := range tmdbIDs {
		if s.declined[id] {
			out[id] = true
		}
	}
	return out, nil
}

type staticRecommendations struct {
	titles []recommendations.ExternalRecommendation
}

func (r staticRecommendations) ExternalRecommendationsEnabled() bool { return true }

func (r staticRecommendations) ExternalRecommendations(context.Context, int, string, string, catalog.AccessFilter) ([]recommendations.ExternalRecommendation, error) {
	return r.titles, nil
}

func TestPersonalRowDropsDeclinedAndDemotesActiveRequests(t *testing.T) {
	row := discoverrows.Row{
		ID:            "recommended_movies",
		Type:          discoverrows.TypeRecommended,
		Title:         "Recommended movies for you",
		Enabled:       true,
		Limit:         20,
		HideAvailable: true,
		Recommended:   &discoverrows.RecommendedSpec{MediaType: "movie"},
	}
	normalized, errs := discoverrows.NormalizeLayout([]discoverrows.Row{row})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	base := &layoutStore{fakeStore: newFakeStore(), rows: normalized}
	base.settings.RequestsEnabled = true
	// Title 101 has an active request; title 102 was declined only; title 103
	// has both an earlier declined request and an active request (active wins);
	// titles 104 and 105 are fresh.
	req101 := &Request{ID: "req-101", MediaType: MediaTypeMovie, TMDBID: 101, Status: StatusPending, Outcome: OutcomeActive}
	req103 := &Request{ID: "req-103", MediaType: MediaTypeMovie, TMDBID: 103, Status: StatusApproved, Outcome: OutcomeActive}
	base.requests[req101.ID] = req101
	base.requests[req103.ID] = req103
	base.active[MediaTypeMovie][101] = req101
	base.active[MediaTypeMovie][103] = req103
	store := &declinedLayoutStore{
		layoutStore: base,
		declined:    map[int]bool{102: true, 103: true},
	}
	svc := NewService(store, &rowsTMDB{}, &fakePresence{})
	svc.SetUserRepository(requestUserRepo{})
	var recs []recommendations.ExternalRecommendation
	for _, id := range []int{101, 102, 103, 104, 105} {
		recs = append(recs, recommendations.ExternalRecommendation{
			Key:   recommendations.ExternalTitleKey{MediaType: recommendations.ExternalMediaMovie, TMDBID: id},
			Title: fmt.Sprintf("Movie %d", id),
		})
	}
	svc.SetRecommendations(staticRecommendations{titles: recs})

	section, err := svc.DiscoverRow(t.Context(), testViewer(1), "recommended_movies", 1)
	if err != nil {
		t.Fatal(err)
	}
	// Fresh titles (104, 105) lead in original order, active requests (101, 103)
	// follow in original order, and declined-only title 102 is dropped.
	if got := resultIDs(section.Results); !slices.Equal(got, []int{104, 105, 101, 103}) {
		t.Fatalf("ordered personal IDs = %v, want [104 105 101 103]", got)
	}
}
