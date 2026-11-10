package sections

// A section title the server wrote carries the key of its message in the
// shared translation catalog (silo-translations), and the values of the
// message's placeholders, so a client can show the title in its own
// language. A title an admin or profile typed carries no key.
//
// page_sections stores the key beside the title. The default rows get it
// when they are built here, and a database trigger clears it when an update
// changes the title, so telling a default from a rename never compares a
// stored title with the English in this file: changing a default's English
// leaves the rows that already hold it keyed.
const (
	// TitleKeyNextUp titles the Next Up row a profile's separate next-up
	// mode adds after Continue Watching.
	TitleKeyNextUp = "home.section.nextUp"

	titleKeyContinueWatching       = "home.section.continueWatching"
	titleKeyRecommendedForYou      = "home.section.recommendedForYou"
	titleKeyBecauseYouWatched      = "home.section.becauseYouWatched"
	titleKeyBecauseYouWatchedTitle = "home.section.becauseYouWatchedTitle" // {title}

	titleKeyRecentlyAddedIn            = "home.section.recentlyAddedIn"            // {library}
	titleKeyRecentlyReleasedIn         = "home.section.recentlyReleasedIn"         // {library}
	titleKeyRecentlyReleasedEpisodesIn = "home.section.recentlyReleasedEpisodesIn" // {library}

	// The placeholders: a generated library row's library name, and the
	// title a Because You Watched row was built from.
	titleArgLibrary = "library"
	titleArgTitle   = "title"
)

// defaultTitleKeys maps the text of each default row title to its key. The
// page_sections title-key migration keyed existing rows from the same pairs.
var defaultTitleKeys = map[string]string{
	titleContinueWatching:          titleKeyContinueWatching,
	titleContinueListening:         "home.section.continueListening",
	titleContinueReading:           "home.section.continueReading",
	titleTrendingThisWeek:          "home.section.trendingThisWeek",
	titleRecentlyAdded:             "home.section.recentlyAdded",
	titleRecentlyAddedMovies:       "home.section.recentlyAddedMovies",
	titleRecentlyAddedTV:           "home.section.recentlyAddedTv",
	"Recently Added Audiobooks":    "home.section.recentlyAddedAudiobooks",
	"Recently Added Ebooks":        "home.section.recentlyAddedEbooks",
	titleRecentlyAddedManga:        "home.section.recentlyAddedManga",
	"Recently Released":            "home.section.recentlyReleased",
	"Recently Released Movies":     "home.section.recentlyReleasedMovies",
	"Recently Released Episodes":   "home.section.recentlyReleasedEpisodes",
	"Recently Released Audiobooks": "home.section.recentlyReleasedAudiobooks",
	"Recently Released Ebooks":     "home.section.recentlyReleasedEbooks",
	titleRecentlyReleasedManga:     "home.section.recentlyReleasedManga",
	titleRecommendedForYou:         titleKeyRecommendedForYou,
	defaultBecauseWatchedTitle:     titleKeyBecauseYouWatched,
	titleReturningShows:            "home.section.returningShows",
	titleTopRated:                  "home.section.topRated",
	"Top Rated Movies":             "home.section.topRatedMovies",
	"Top Rated TV":                 "home.section.topRatedTv",
	titleHiddenGems:                "home.section.hiddenGems",
	"Seasonal Picks":               "home.section.seasonalPicks",
	titleRandomPicks:               "home.section.randomPicks",
	"Next in Your Series":          "home.section.nextInYourSeries",
}

// withDefaultTitleKeys keys every section whose title is a default row
// title, and returns sections.
func withDefaultTitleKeys(sections []*PageSection) []*PageSection {
	for _, s := range sections {
		if key, ok := defaultTitleKeys[s.Title]; ok {
			s.TitleKey = key
		}
	}
	return sections
}

// generatedHomeLibraryRecentTitleKey is the key of a generated home library
// row's title, as generatedHomeLibraryRecentTitle words it, or "" for a
// row that is not one.
func generatedHomeLibraryRecentTitleKey(s *PageSection) string {
	switch generatedHomeLibraryRecentKindForSection(s) {
	case generatedHomeLibraryRecentKindReleasedEpisodes:
		return titleKeyRecentlyReleasedEpisodesIn
	case generatedHomeLibraryRecentKindAdded:
		return titleKeyRecentlyAddedIn
	case generatedHomeLibraryRecentKindReleased:
		return titleKeyRecentlyReleasedIn
	}
	return ""
}

// keepTitleKeyForOwnTitle drops the key fetched took over from section when
// the fetch replaced the title without setting a key of its own, as a
// seasonal row's in-season title or an editorial subject does: the key
// describes the stored title, not the replacement.
func keepTitleKeyForOwnTitle(section ResolvedSection, fetched *SectionWithItems) {
	if fetched.Title != section.Title && fetched.TitleKey == section.TitleKey {
		fetched.TitleKey, fetched.TitleArgs = "", nil
	}
}
