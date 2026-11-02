package recipes

// SeasonalVibe is one curated take on a seasonal theme: a public MDBList list
// whose titles the row shows, most popular on IMDb first. Keys are stored in
// section configs, so never rename or reuse one.
type SeasonalVibe struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ListURL     string `json:"list_url"`
}

// SeasonalTheme is a holiday or recurring moment the seasonal row can follow.
// The first vibe is the default. Hidden themes are kept for configs saved
// before they were folded into another theme; the editor does not offer them.
//
// Start and End are the theme's yearly window (MM-DD, inclusive; End before
// Start wraps across New Year). A theme without them has its own rule in
// SeasonalPredicates: Easter moves, and Weekly themes recur on weekdays. A row
// may give any non-weekly theme its own dates (SeasonalThemedParams.ThemeDates).
type SeasonalTheme struct {
	Key    string         `json:"key"`
	Name   string         `json:"name"`
	When   string         `json:"when"`
	Start  string         `json:"start,omitempty"`
	End    string         `json:"end,omitempty"`
	Weekly bool           `json:"weekly,omitempty"`
	Hidden bool           `json:"-"`
	Vibes  []SeasonalVibe `json:"vibes"`
}

// Built-in theme keys. They are stored in section configs; never rename one.
const (
	ThemeValentines        = "valentines"
	ThemeStPatricks        = "st_patricks"
	ThemeEaster            = "easter"
	ThemeThanksgiving      = "thanksgiving"
	ThemeNewYear           = "new_year"
	ThemeChristmas         = "christmas"
	ThemeHalloween         = "halloween"
	ThemeSaturdayMorning   = "saturday_morning"
	ThemeFamilyMovieNight  = "family_movie_night"
	ThemeSummerBlockbuster = "summer_blockbuster"
	ThemeSummer            = "summer"
)

// Vibe keys several themes share.
const (
	vibeFavorites = "favorites"
	vibeFamily    = "family"
)

const familyMovieNightTitle = "Family Movie Night"

func mdblist(path string) string { return "https://mdblist.com/lists/" + path }

var summerVibes = []SeasonalVibe{
	{Key: "blockbusters", Name: "Blockbusters", Title: "Summer Blockbusters", Description: "The biggest box-office hits.", ListURL: mdblist("drovur/box-office-giants")},
	{Key: "classic_summer", Name: "Classic summer movies", Title: "Classic Summer Movies", Description: "Summer releases that defined the season, from Jaws to Independence Day.", ListURL: mdblist("heavymgs/summer-movies")},
	{Key: "nostalgia", Name: "Summer nostalgia", Title: "Summer Nostalgia", Description: "Long days, summer camp and coming of age.", ListURL: mdblist("reachingout/summer-movies")},
}

// SeasonalThemes lists every theme in calendar order, the order the editor
// shows them: dated holidays by their usual start, then the weekly themes.
// SeasonalThemeOrder decides which wins when several are in season at once.
var SeasonalThemes = []SeasonalTheme{
	{Key: ThemeValentines, Name: "Valentine's Day", When: "Feb 7–14", Start: "02-07", End: "02-14", Vibes: []SeasonalVibe{
		{Key: "romcoms", Name: "Rom-coms", Title: "Valentine's Rom-Coms", Description: "The best-reviewed romantic comedies.", ListURL: mdblist("hdlists/romantic-comedies-rotten-tomatoes-top-rated")},
		{Key: vibeFavorites, Name: "Valentine's favorites", Title: "Valentine's Favorites", Description: "Popular date-night picks.", ListURL: mdblist("linvo/valentines-day-popular-movies")},
		{Key: "romantic_dramas", Name: "Romantic dramas", Title: "Romantic Dramas", Description: "Sweeping love stories with a few tears.", ListURL: mdblist("themtsil/romantic-dramas")},
		{Key: "love_stories", Name: "Classic love stories", Title: "Classic Love Stories", Description: "Casablanca, Titanic and the other all-time romances.", ListURL: mdblist("tob-t/love-story")},
	}},
	{Key: ThemeStPatricks, Name: "St. Patrick's Day", When: "Mar 15–17", Start: "03-15", End: "03-17", Vibes: []SeasonalVibe{
		{Key: "irish_film", Name: "Irish film", Title: "Irish Film", Description: "Films from and about Ireland.", ListURL: mdblist("irljidel/irish-interest-movies")},
		{Key: "irish_tv", Name: "Irish TV", Title: "Irish TV", Description: "Series from and about Ireland.", ListURL: mdblist("irljidel/irish-interest-tv-shows")},
	}},
	{Key: ThemeEaster, Name: "Easter", When: "Palm Sunday to Easter Monday", Vibes: []SeasonalVibe{
		{Key: vibeFamily, Name: "Easter movies", Title: "Easter Movies", Description: "Springtime family favorites and Easter classics.", ListURL: mdblist("bsm-venger/easter-movies-e78nzwjk7k")},
		{Key: "faith", Name: "Faith and Easter stories", Title: "Easter Stories", Description: "Biblical epics and stories of faith.", ListURL: mdblist("bluejack92/easter-by-antares")},
	}},
	{Key: ThemeSummerBlockbuster, Name: "Summer", When: "June to August", Start: "06-01", End: "08-31", Vibes: summerVibes},
	{Key: ThemeSummer, Name: "Summer", When: "June to August", Start: "06-01", End: "08-31", Hidden: true, Vibes: summerVibes},
	{Key: ThemeHalloween, Name: "Halloween", When: "October", Start: "10-01", End: "10-31", Vibes: []SeasonalVibe{
		{Key: vibeFavorites, Name: "Halloween favorites", Title: "Halloween Favorites", Description: "The best-loved Halloween movies, spooky and scary.", ListURL: mdblist("hdlists/halloween-movies-the-best-of-all-time")},
		{Key: vibeFamily, Name: "Family Halloween", Title: "Family Halloween", Description: "Spooky fun for all ages.", ListURL: mdblist("purple_smurf/halloween-family2")},
		{Key: "horror", Name: "Halloween horror", Title: "Halloween Horror", Description: "Slashers, hauntings and horror classics.", ListURL: mdblist("swansongfunds/horror-halloween")},
		{Key: "animated", Name: "Animated Halloween", Title: "Animated Halloween", Description: "Animated spooky favorites.", ListURL: mdblist("swansongfunds/halloween-animation")},
		{Key: "tv", Name: "Halloween TV", Title: "Halloween TV", Description: "Spooky series to binge.", ListURL: mdblist("rootednode/halloween-tv")},
	}},
	{Key: ThemeThanksgiving, Name: "Thanksgiving", When: "Nov 22–30", Start: "11-22", End: "11-30", Vibes: []SeasonalVibe{
		{Key: vibeFavorites, Name: "Thanksgiving movies", Title: "Thanksgiving Movies", Description: "Films set around the Thanksgiving table.", ListURL: mdblist("bluejack92/thanksgiving-by-antares")},
		{Key: "feel_good", Name: "Feel-good favorites", Title: "Feel-Good Favorites", Description: "Heartwarming picks for the long weekend.", ListURL: mdblist("netthier/thanksgiving-movies")},
	}},
	{Key: ThemeChristmas, Name: "Christmas", When: "December", Start: "12-01", End: "12-31", Vibes: []SeasonalVibe{
		{Key: vibeFavorites, Name: "Christmas favorites", Title: "Christmas Favorites", Description: "The most-watched Christmas movies.", ListURL: mdblist("jbeasley74/christmas-movies")},
		{Key: vibeFamily, Name: "Family Christmas", Title: "Family Christmas", Description: "Christmas movies for the whole family.", ListURL: mdblist("paperarmour/christmas-kids-movies")},
		{Key: "classics", Name: "Christmas classics", Title: "Christmas Classics", Description: "Golden-age holiday films.", ListURL: mdblist("momanch/oldies-christmas-classics")},
		{Key: "romcoms", Name: "Christmas rom-coms", Title: "Christmas Rom-Coms", Description: "Romantic comedies set at Christmas.", ListURL: mdblist("momanch/christmas-romantic-comedies")},
		{Key: "comedies", Name: "Christmas comedies", Title: "Christmas Comedies", Description: "Holiday laughs.", ListURL: mdblist("momanch/christmas-movie-comedies")},
		{Key: "hallmark", Name: "Hallmark Christmas", Title: "Hallmark Christmas", Description: "Hallmark's original Christmas movies.", ListURL: mdblist("vectrexgamer/hallmark-original-christmas-movies")},
	}},
	{Key: ThemeNewYear, Name: "New Year's", When: "Dec 26–Jan 1", Start: "12-26", End: "01-01", Vibes: []SeasonalVibe{
		{Key: vibeFavorites, Name: "New Year's movies", Title: "New Year's Movies", Description: "Films that ring in the new year.", ListURL: mdblist("netthier/new-year-movies")},
	}},
	{Key: ThemeSaturdayMorning, Name: "Saturday Morning", When: "Saturdays before 1pm", Weekly: true, Vibes: []SeasonalVibe{
		{Key: "cartoons", Name: "Saturday morning cartoons", Title: "Saturday Morning Cartoons", Description: "Classic weekend cartoons.", ListURL: mdblist("arnx1922/saturday-morning-cartoons-b75bnxjjej")},
		{Key: "nineties", Name: "90s cartoons", Title: "90s Saturday Mornings", Description: "The cartoons of the 1990s.", ListURL: mdblist("smackdaddy17/saturday-morning-cartoons")},
		{Key: "modern", Name: "Modern cartoons", Title: "Saturday Morning Favorites", Description: "Animated series from Batman to Gravity Falls.", ListURL: mdblist("savini-andrew-gmail-com/saturday-morning-all-time")},
	}},
	{Key: ThemeFamilyMovieNight, Name: familyMovieNightTitle, When: "Friday and Saturday evenings", Weekly: true, Vibes: []SeasonalVibe{
		{Key: "new", Name: "New and popular", Title: familyMovieNightTitle, Description: "Recent family hits.", ListURL: mdblist("josh_bergeron/sunday-night-family-movies")},
		{Key: vibeFavorites, Name: "Family favorites", Title: "Family Favorites", Description: "Family films from The Goonies to Coco.", ListURL: mdblist("jaredmecham/top-family-movies-1980")},
		{Key: "live_action", Name: "Live-action family", Title: familyMovieNightTitle, Description: "Family films without the animation.", ListURL: mdblist("paperarmour/family-movies-animation-excluded")},
	}},
}

// SeasonalThemeByKey returns the catalog entry for a theme key.
func SeasonalThemeByKey(key string) (SeasonalTheme, bool) {
	for _, theme := range SeasonalThemes {
		if theme.Key == key {
			return theme, true
		}
	}
	return SeasonalTheme{}, false
}

// Vibe returns the vibe with the given key, or the theme's default (first)
// vibe when key is empty or unknown.
func (t SeasonalTheme) Vibe(key string) SeasonalVibe {
	for _, vibe := range t.Vibes {
		if vibe.Key == key {
			return vibe
		}
	}
	return t.Vibes[0]
}

func (t SeasonalTheme) hasVibe(key string) bool {
	for _, vibe := range t.Vibes {
		if vibe.Key == key {
			return true
		}
	}
	return false
}
