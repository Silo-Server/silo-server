package models

import "strings"

// Rating sources Silo stores per item in media_item_rating_sources. The names
// are Silo's, matching the keys metadata plugins send under ratings.sources.
const (
	RatingSourceIMDB           = "imdb"
	RatingSourceTMDB           = "tmdb"
	RatingSourceRTCritic       = "rt_critic"
	RatingSourceRTAudience     = "rt_audience"
	RatingSourceMetacritic     = "metacritic"
	RatingSourceMetacriticUser = "metacritic_user"
	RatingSourceTrakt          = "trakt"
	RatingSourceLetterboxd     = "letterboxd"
	RatingSourceRogerEbert     = "rogerebert"
	RatingSourceMyAnimeList    = "myanimelist"
	// RatingSourceMDBList is MDBList's own aggregate score for the title.
	RatingSourceMDBList = "mdblist"
)

// RatingSourceDefinition says how clients show one rating source.
type RatingSourceDefinition struct {
	Source string
	// Name is the plain-text mark clients show next to the score. Silo shows
	// no third-party logos except TMDB's, whose terms allow it.
	Name string
	// Scale is the top of the source's own scale: 10 for IMDb's 8.5, 100 for
	// a Rotten Tomatoes 93%. Stored scores are on 0-100 and are shown on this
	// scale.
	Scale float64
	// Percent shows the score as a percentage.
	Percent bool
}

// ratingSourceDefinitions is the accepted vocabulary, in the order item
// detail lists the sources.
var ratingSourceDefinitions = []RatingSourceDefinition{
	{Source: RatingSourceIMDB, Name: "IMDb", Scale: 10},
	{Source: RatingSourceTMDB, Name: "TMDB", Scale: 10},
	{Source: RatingSourceRTCritic, Name: "RT", Scale: 100, Percent: true},
	{Source: RatingSourceRTAudience, Name: "RT Audience", Scale: 100, Percent: true},
	{Source: RatingSourceMetacritic, Name: "Metacritic", Scale: 100},
	{Source: RatingSourceMetacriticUser, Name: "Metacritic Users", Scale: 10},
	{Source: RatingSourceLetterboxd, Name: "Letterboxd", Scale: 5},
	{Source: RatingSourceTrakt, Name: "Trakt", Scale: 100, Percent: true},
	{Source: RatingSourceRogerEbert, Name: "Roger Ebert", Scale: 4},
	{Source: RatingSourceMyAnimeList, Name: "MyAnimeList", Scale: 10},
	{Source: RatingSourceMDBList, Name: "MDBList", Scale: 100},
}

// RatingSourceDefinitions returns the built-in sources in display order.
func RatingSourceDefinitions() []RatingSourceDefinition {
	return append([]RatingSourceDefinition(nil), ratingSourceDefinitions...)
}

// LookupRatingSource returns a built-in source's definition.
func LookupRatingSource(source string) (RatingSourceDefinition, bool) {
	rank, ok := ratingSourceRanks[source]
	if !ok {
		return RatingSourceDefinition{}, false
	}
	return ratingSourceDefinitions[rank], true
}

// RatingSourceAlwaysShown reports whether clients show a source whatever the
// administrator chose: IMDb and TMDB. Every other source is shown only once an
// administrator turns it on, because its owner's terms restrict display.
func RatingSourceAlwaysShown(source string) bool {
	return source == RatingSourceIMDB || source == RatingSourceTMDB
}

var ratingSourceRanks = func() map[string]int {
	ranks := make(map[string]int, len(ratingSourceDefinitions))
	for i, definition := range ratingSourceDefinitions {
		ranks[definition.Source] = i
	}
	return ranks
}()

// NormalizeRatingSource returns the canonical name of a rating source, or ""
// when Silo does not store that source.
func NormalizeRatingSource(raw string) string {
	source := strings.ToLower(strings.TrimSpace(raw))
	if _, ok := ratingSourceRanks[source]; !ok {
		return ""
	}
	return source
}

// RatingSourceRank orders sources for display. Unknown sources sort last.
func RatingSourceRank(source string) int {
	if rank, ok := ratingSourceRanks[source]; ok {
		return rank
	}
	return len(ratingSourceDefinitions)
}

// ItemRatingSource is a row in media_item_rating_sources: one source's rating
// of an item on a common 0-100 scale.
type ItemRatingSource struct {
	ContentID string
	Source    string
	Score     float64
	// Votes is nil when the provider did not report a vote count.
	Votes *int64
	// Provider is the slug of the metadata provider that supplied the rating.
	Provider string
}
