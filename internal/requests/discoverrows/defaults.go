package discoverrows

// Chart presets the defaults and checks name.
const (
	presetTrending = "trending"
	presetPopular  = "popular"
)

// legacyRows are the six rows the Discover page showed before it could be
// changed, in that order. Their IDs are the section keys the frozen /api/v1
// discovery serves and shipped clients merge on (trending_* into one row,
// popular_* into another), so a layout keeps each one's chart.
var legacyRows = []Row{
	{ID: "trending_movies", Title: "Trending Movies", Chart: &ChartSpec{Preset: presetTrending, MediaType: MediaMovie, TimeWindow: "week"}},
	{ID: "trending_series", Title: "Trending Series", Chart: &ChartSpec{Preset: presetTrending, MediaType: MediaSeries, TimeWindow: "week"}},
	{ID: "popular_movies", Title: "Popular Movies", Chart: &ChartSpec{Preset: presetPopular, MediaType: MediaMovie}},
	{ID: "popular_series", Title: "Popular Series", Chart: &ChartSpec{Preset: presetPopular, MediaType: MediaSeries}},
	{ID: "upcoming_movies", Title: "Upcoming Movies", Chart: &ChartSpec{Preset: "upcoming", MediaType: MediaMovie}},
	{ID: "on_air_series", Title: "On Air Series", Chart: &ChartSpec{Preset: "on_the_air", MediaType: MediaSeries}},
}

// brandRows follow the charts on the default page.
var brandRows = []Row{
	{ID: string(TypeStudios), Type: TypeStudios, Title: "Studios"},
	{ID: string(TypeNetworks), Type: TypeNetworks, Title: "Networks"},
	{ID: string(TypeGenres), Type: TypeGenres, Title: "Genres"},
}

// DefaultRows is the layout of a server whose admin never changed it: the six
// built-in charts, then the studio, network and genre carousels.
func DefaultRows() []Row {
	out := make([]Row, 0, len(legacyRows)+len(brandRows))
	for _, r := range legacyRows {
		out = append(out, legacyRow(r))
	}
	for _, r := range brandRows {
		r.Enabled = true
		out = append(out, r)
	}
	return out
}

// LegacyRow returns the built-in row with the given ID, as the default
// layout has it.
func LegacyRow(id string) (Row, bool) {
	for _, r := range legacyRows {
		if r.ID == id {
			return legacyRow(r), true
		}
	}
	return Row{}, false
}

// IsLegacyID reports whether id names one of the six built-in rows.
func IsLegacyID(id string) bool {
	_, ok := LegacyRow(id)
	return ok
}

// LegacyIDs returns the built-in rows' IDs in default order.
func LegacyIDs() []string {
	ids := make([]string, len(legacyRows))
	for i, r := range legacyRows {
		ids[i] = r.ID
	}
	return ids
}

func legacyRow(r Row) Row {
	chart := *r.Chart
	r.Chart = &chart
	r.Type, r.Enabled, r.Limit = TypeTMDBChart, true, DefaultLimit
	return r
}

// defaultTitleKeys maps the ID of each built-in row to the shared
// translation catalog key of its default title, which every client can show
// in its own language while the row still holds that title.
var defaultTitleKeys = map[string]string{
	"trending_movies":    "home.section.discoverTrendingMovies",
	"trending_series":    "home.section.discoverTrendingSeries",
	"popular_movies":     "home.section.discoverPopularMovies",
	"popular_series":     "home.section.discoverPopularSeries",
	"upcoming_movies":    "home.section.discoverUpcomingMovies",
	"on_air_series":      "home.section.discoverOnAirSeries",
	string(TypeStudios):  "home.section.discoverStudios",
	string(TypeNetworks): "home.section.discoverNetworks",
	string(TypeGenres):   "home.section.discoverGenres",
}

// TitleKey is the translation catalog key of the row's title while the row is
// a built-in one still holding its default title, and "" for any other title,
// which an admin typed. A title an admin typed that equals the default's
// English gets the key too; the English is the same.
func TitleKey(id, title string) string {
	key, ok := defaultTitleKeys[id]
	if !ok {
		return ""
	}
	for _, rows := range [][]Row{legacyRows, brandRows} {
		for _, r := range rows {
			if r.ID == id && r.Title == title {
				return key
			}
		}
	}
	return ""
}
