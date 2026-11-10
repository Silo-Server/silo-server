package requests

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
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
	discoverErr error
	discovered  []tmdb.DiscoverParams
}

func (f *rowsTMDB) ChartPage(_ context.Context, params tmdb.ChartParams, _ int) (*tmdb.MediaPage, error) {
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

func moviePage(ids ...int) *tmdb.MediaPage {
	page := &tmdb.MediaPage{Page: 1, TotalPages: 5, TotalResults: 100}
	for _, id := range ids {
		page.Results = append(page.Results, tmdb.MediaResult{ID: id, MediaType: "movie", Title: fmt.Sprintf("Movie %d", id)})
	}
	return page
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

func TestDiscoverRowsFollowsTheLayout(t *testing.T) {
	listRow := discoverrows.Row{ID: "r_list", Type: discoverrows.TypeMDBList, Title: "My List", Enabled: true, HideAvailable: true, List: &discoverrows.ListSpec{URL: "https://mdblist.com/lists/u/l"}}
	rows := []discoverrows.Row{
		{ID: "r_pop", Type: discoverrows.TypeTMDBChart, Title: "Popular", Enabled: true, Limit: 2, Chart: &discoverrows.ChartSpec{Preset: "popular", MediaType: "movie"}},
		{Type: discoverrows.TypeStudios, Title: "Studios", Enabled: true},
		{ID: "trending_movies", Type: discoverrows.TypeTMDBChart, Title: "Off", Chart: &discoverrows.ChartSpec{Preset: "trending", MediaType: "movie", TimeWindow: "week"}},
		{ID: "r_broken", Type: discoverrows.TypeTMDBDiscover, Title: "Broken", Enabled: true, Discover: &discoverrows.DiscoverSpec{MediaType: "movie", SortBy: "popularity.desc"}},
		{ID: "r_empty", Type: discoverrows.TypeTMDBChart, Title: "Empty", Enabled: true, Chart: &discoverrows.ChartSpec{Preset: "top_rated", MediaType: "movie"}},
		listRow,
	}
	client := &rowsTMDB{
		charts:      map[string]*tmdb.MediaPage{"popular/movie": moviePage(1, 2, 3)},
		discoverErr: errors.New("tmdb rejected the filter"),
	}
	presence := &fakePresence{available: map[MediaType]map[int]bool{MediaTypeMovie: {11: true}}}
	service := newLayoutService(t, rows, client, presence, "")
	service.SetDiscoverLists(cardSnapshots{discoverrows.SourceKey(listRow): movieCards(10, 12)}, nil)

	page, err := service.DiscoverRows(t.Context(), testViewer(1))
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, ref := range page.Order {
		order = append(order, ref.Key+":"+string(ref.Type))
	}
	if want := []string{"r_pop:tmdb_chart", "studios:studios", "r_broken:tmdb_discover", "r_empty:tmdb_chart", "r_list:mdblist"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v (disabled row left out, brand row placed)", order, want)
	}
	// The broken and empty rows are left out of the titles; the chart is cut
	// to its limit; the list drops the title the library has.
	if len(page.Sections) != 2 || page.Sections[0].Key != "r_pop" || page.Sections[1].Key != "r_list" {
		t.Fatalf("sections = %+v", page.Sections)
	}
	if got := resultIDs(page.Sections[0].Results); !slices.Equal(got, []int{1, 2}) || page.Sections[0].NextPage != 0 {
		t.Fatalf("chart row = %v next %d, want [1 2] with no resume cursor", got, page.Sections[0].NextPage)
	}
	if got := resultIDs(page.Sections[1].Results); !slices.Equal(got, []int{10, 12}) || page.Sections[1].Title != "My List" {
		t.Fatalf("list row = %v %q, want [10 12]", got, page.Sections[1].Title)
	}

	onlyBroken := newLayoutService(t, rows[3:4], client, nil, "")
	if _, err := onlyBroken.DiscoverRows(t.Context(), testViewer(1)); err == nil {
		t.Fatal("every title row failed, want the error")
	}
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

func TestDiscoverAllKeepsTheBuiltInRowsWhateverTheLayout(t *testing.T) {
	// /api/v1 is frozen: a layout without the built-in rows does not change it.
	rows := []discoverrows.Row{{ID: "r_mine", Type: discoverrows.TypeTMDBChart, Title: "Mine", Enabled: true, Chart: &discoverrows.ChartSpec{Preset: "top_rated", MediaType: "series"}}}
	client := &rowsTMDB{charts: map[string]*tmdb.MediaPage{"trending/movie": moviePage(1)}}
	service := newLayoutService(t, rows, client, nil, "")
	sections, err := service.DiscoverAll(t.Context(), testViewer(1))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, s := range sections {
		keys = append(keys, s.Key+"="+s.Title)
	}
	want := []string{"trending_movies=Trending Movies", "trending_series=Trending Series", "popular_movies=Popular Movies", "popular_series=Popular Series", "upcoming_movies=Upcoming Movies", "on_air_series=On Air Series"}
	if !slices.Equal(keys, want) {
		t.Fatalf("v1 sections = %v, want %v", keys, want)
	}
	if got := resultIDs(sections[0].Results); !slices.Equal(got, []int{1}) {
		t.Fatalf("trending_movies = %v, want the trending/movie chart", got)
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
	if err != nil || !saved.Customized || saved.Revision == 0 || len(saved.Rows) != 2 || saved.Rows[1].ID != "trending_series" {
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
