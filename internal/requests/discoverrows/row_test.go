package discoverrows

import (
	"fmt"
	"strings"
	"testing"
)

type layoutCase struct {
	name string
	rows []Row
	// wantErr names the field that must be refused; "" means the layout is
	// accepted.
	wantErr string
	check   func(t *testing.T, rows []Row)
}

func runLayoutCases(t *testing.T, tests []layoutCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, errs := NormalizeLayout(tt.rows)
			if tt.wantErr == "" {
				if len(errs) > 0 {
					t.Fatalf("refused: %v", errs)
				}
				if tt.check != nil {
					tt.check(t, rows)
				}
				return
			}
			if _, ok := errs[tt.wantErr]; !ok {
				t.Fatalf("errors = %v, want one on %s", errs, tt.wantErr)
			}
		})
	}
}

func chartRow(id, preset, mediaType, window string) Row {
	return Row{ID: id, Type: TypeTMDBChart, Title: "Chart", Enabled: true, Chart: &ChartSpec{Preset: preset, MediaType: mediaType, TimeWindow: window}}
}

// TestNormalizeLayoutRows covers the layout's own rules: IDs, built-in and
// brand rows, titles and limits.
func TestNormalizeLayoutRows(t *testing.T) {
	builtIn, _ := LegacyRow("trending_movies")
	retitled := builtIn
	retitled.Title, retitled.Limit, retitled.HideAvailable = "What's hot", 40, true

	runLayoutCases(t, []layoutCase{
		{name: "default layout", rows: DefaultRows()},
		{name: "built-in row keeps its chart but takes a new title, limit and hide", rows: []Row{retitled}},
		{name: "built-in row cannot change its chart", rows: []Row{chartRow("trending_movies", "popular", "movie", "")}, wantErr: "rows[0].chart"},
		{name: "built-in id cannot become a list row", rows: []Row{{ID: "popular_series", Type: TypeMDBList, Title: "x", List: &ListSpec{URL: "https://mdblist.com/lists/a/b"}}}, wantErr: "rows[0].id"},
		{
			name: "new row gets an opaque id and the default limit",
			rows: []Row{chartRow("", "trending", "all", "day")},
			check: func(t *testing.T, rows []Row) {
				if !strings.HasPrefix(rows[0].ID, "r_") || !rowIDPattern.MatchString(rows[0].ID) || rows[0].Limit != DefaultLimit {
					t.Fatalf("row = %+v, want an r_ id and limit %d", rows[0], DefaultLimit)
				}
			},
		},
		{name: "duplicate id", rows: []Row{chartRow("mine", "popular", "movie", ""), chartRow("mine", "popular", "series", "")}, wantErr: "rows[1].id"},
		{name: "reserved id", rows: []Row{chartRow("browse", "popular", "movie", "")}, wantErr: "rows[0].id"},
		{name: "brand id is its type", rows: []Row{{ID: "genres_2", Type: TypeGenres, Title: "Genres"}}, wantErr: "rows[0].id"},
		{name: "a brand row appears once", rows: []Row{{Type: TypeStudios, Title: "A"}, {Type: TypeStudios, Title: "B"}}, wantErr: "rows[1].id"},
		{
			name: "brand row drops limit and hide",
			rows: []Row{{Type: TypeNetworks, Title: "Networks", Limit: 50, HideAvailable: true}},
			check: func(t *testing.T, rows []Row) {
				if rows[0].ID != "networks" || rows[0].Limit != 0 || rows[0].HideAvailable {
					t.Fatalf("row = %+v", rows[0])
				}
			},
		},
		{name: "brand row takes no spec", rows: []Row{{Type: TypeGenres, Title: "G", Chart: &ChartSpec{Preset: "popular", MediaType: "movie"}}}, wantErr: "rows[0].chart"},
		{name: "unknown type", rows: []Row{{Type: "live_tv", Title: "x"}}, wantErr: "rows[0].type"},
		{name: "blank title", rows: []Row{{Type: TypeGenres, Title: "  "}}, wantErr: "rows[0].title"},
		{name: "limit above the maximum", rows: []Row{{Type: TypeTMDBChart, Title: "x", Limit: MaxLimit + 1, Chart: &ChartSpec{Preset: "popular", MediaType: "movie"}}}, wantErr: "rows[0].limit"},
		{name: "too many rows", rows: make([]Row, MaxRows+1), wantErr: "rows"},
	})
}

// TestNormalizeLayoutSpecs covers each row type's spec, including the list
// URL allowlists that keep a fetch off other hosts.
func TestNormalizeLayoutSpecs(t *testing.T) {
	discover := func(d DiscoverSpec) Row {
		return Row{Type: TypeTMDBDiscover, Title: "Filter", Enabled: true, Discover: &d}
	}
	list := func(rowType Type, url string) Row {
		return Row{Type: rowType, Title: "List", Enabled: true, List: &ListSpec{URL: url}}
	}
	runLayoutCases(t, []layoutCase{
		{name: "chart needs its spec", rows: []Row{{Type: TypeTMDBChart, Title: "x"}}, wantErr: "rows[0].chart"},
		{name: "chart with a second spec", rows: []Row{{Type: TypeTMDBChart, Title: "x", Chart: &ChartSpec{Preset: "popular", MediaType: "movie"}, List: &ListSpec{URL: "1"}}}, wantErr: "rows[0].list"},
		{name: "upcoming is a movie chart", rows: []Row{chartRow("", "upcoming", "series", "")}, wantErr: "rows[0].chart"},
		{name: "all is trending only", rows: []Row{chartRow("", "popular", "all", "")}, wantErr: "rows[0].chart"},
		{
			name: "time window dropped off a non-trending chart",
			rows: []Row{chartRow("", "top_rated", "series", "week")},
			check: func(t *testing.T, rows []Row) {
				if rows[0].Chart.TimeWindow != "" {
					t.Fatalf("time window kept: %+v", rows[0].Chart)
				}
			},
		},
		{
			name: "discover row canonical",
			rows: []Row{discover(DiscoverSpec{MediaType: "series", SortBy: "first_air_date.desc", KeywordIDs: []int{210024, 210024}, OriginCountries: []string{"jp", "JP"}, WatchProviderIDs: []int{8}, WatchRegion: "us", CertificationLte: "tv-14"})},
			check: func(t *testing.T, rows []Row) {
				d := rows[0].Discover
				if fmt.Sprint(d.KeywordIDs, d.OriginCountries) != "[210024] [JP]" || d.WatchRegion != "US" || d.CertificationLte != "TV-14" {
					t.Fatalf("discover = %+v", d)
				}
			},
		},
		{name: "watch providers need a region", rows: []Row{discover(DiscoverSpec{MediaType: "movie", SortBy: "popularity.desc", WatchProviderIDs: []int{8}})}, wantErr: "rows[0].discover.watch_region"},
		{name: "networks filter series only", rows: []Row{discover(DiscoverSpec{MediaType: "movie", SortBy: "popularity.desc", NetworkIDs: []int{213}})}, wantErr: "rows[0].discover.network_ids"},
		{name: "series do not sort by revenue", rows: []Row{discover(DiscoverSpec{MediaType: "series", SortBy: "revenue.desc"})}, wantErr: "rows[0].discover.sort_by"},
		{name: "unknown sort", rows: []Row{discover(DiscoverSpec{MediaType: "movie", SortBy: "random"})}, wantErr: "rows[0].discover"},
		{name: "rating on the other ladder", rows: []Row{discover(DiscoverSpec{MediaType: "movie", SortBy: "popularity.desc", CertificationLte: "TV-MA"})}, wantErr: "rows[0].discover.certification_lte"},
		{name: "years out of order", rows: []Row{discover(DiscoverSpec{MediaType: "movie", SortBy: "popularity.desc", YearFrom: 1999, YearTo: 1990})}, wantErr: "rows[0].discover.year_to"},
		{name: "non-positive id", rows: []Row{discover(DiscoverSpec{MediaType: "movie", SortBy: "popularity.desc", GenreIDs: []int{0}})}, wantErr: "rows[0].discover.genre_ids"},
		{
			name: "mdblist url stored as the list page",
			rows: []Row{list(TypeMDBList, " https://mdblist.com/lists/garycrawfordgc/top-watched-movies/json/ ")},
			check: func(t *testing.T, rows []Row) {
				if got := rows[0].List.URL; got != "https://mdblist.com/lists/garycrawfordgc/top-watched-movies" {
					t.Fatalf("url = %q", got)
				}
				if got := SourceKey(rows[0]); got != "mdblist:https://mdblist.com/lists/garycrawfordgc/top-watched-movies/json" {
					t.Fatalf("source key = %q", got)
				}
			},
		},
		{name: "mdblist url on another host", rows: []Row{list(TypeMDBList, "https://169.254.169.254/lists/a/b")}, wantErr: "rows[0].list.url"},
		{name: "mdblist url outside lists", rows: []Row{list(TypeMDBList, "https://mdblist.com/admin")}, wantErr: "rows[0].list.url"},
		{
			name: "tmdb list keyed by id",
			rows: []Row{list(TypeTMDBList, "themoviedb.org/list/8504994-best-of")},
			check: func(t *testing.T, rows []Row) {
				if rows[0].List.URL != "https://www.themoviedb.org/list/8504994" || SourceKey(rows[0]) != "tmdb_list:8504994" {
					t.Fatalf("row = %+v key %q", rows[0].List, SourceKey(rows[0]))
				}
			},
		},
		{name: "tmdb list on another host", rows: []Row{list(TypeTMDBList, "https://example.com/list/1")}, wantErr: "rows[0].list.url"},
	})
}
