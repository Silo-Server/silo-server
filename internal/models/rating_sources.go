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

// ratingSourceOrder is the accepted vocabulary, in the order item detail
// lists the sources.
var ratingSourceOrder = []string{
	RatingSourceIMDB,
	RatingSourceTMDB,
	RatingSourceRTCritic,
	RatingSourceRTAudience,
	RatingSourceMetacritic,
	RatingSourceMetacriticUser,
	RatingSourceLetterboxd,
	RatingSourceTrakt,
	RatingSourceRogerEbert,
	RatingSourceMyAnimeList,
	RatingSourceMDBList,
}

var ratingSourceRanks = func() map[string]int {
	ranks := make(map[string]int, len(ratingSourceOrder))
	for i, source := range ratingSourceOrder {
		ranks[source] = i
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
	return len(ratingSourceOrder)
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
