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
