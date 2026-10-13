package models

import (
	"regexp"
)

// Silo's own rating sources are IMDb and TMDB, the two its title pages always
// show. Every other source is declared by a metadata plugin (see
// metadata.extractRatingSources), which names it and sets its scale.
const (
	RatingSourceIMDB = "imdb"
	RatingSourceTMDB = "tmdb"
	// RatingSourceRTCritic and RatingSourceRTAudience name the Rotten Tomatoes
	// scores stored in media_items.rating_rt_critic and rating_rt_audience. A
	// plugin that fills those columns declares these ids to show them.
	RatingSourceRTCritic   = "rt_critic"
	RatingSourceRTAudience = "rt_audience"
)

// RatingSourceDefinition says how clients show one rating source.
type RatingSourceDefinition struct {
	Source string
	// Name is the plain-text mark clients show next to the score. Silo shows
	// no third-party logos except TMDB's, whose terms allow it.
	Name string
	// Label names the source in full where there is room, such as the
	// administrator's list of sources to show.
	Label string
	// Scale is the top of the source's own scale: 10 for IMDb's 8.5, 100 for
	// a Rotten Tomatoes 93%. Stored scores are on 0-100 and are shown on this
	// scale.
	Scale float64
	// Percent shows the score as a percentage; its Scale is 100.
	Percent bool
}

// ratingSourceDefinitions are Silo's own sources, in the order title pages
// list them, before any source a plugin declares.
var ratingSourceDefinitions = []RatingSourceDefinition{
	{Source: RatingSourceIMDB, Name: "IMDb", Label: "IMDb", Scale: 10},
	{Source: RatingSourceTMDB, Name: "TMDB", Label: "TMDB", Scale: 10},
}

// RatingSourceDefinitions returns Silo's own sources in display order.
func RatingSourceDefinitions() []RatingSourceDefinition {
	return append([]RatingSourceDefinition(nil), ratingSourceDefinitions...)
}

// ratingSourceIDPattern is the shape of every rating source name, Silo's own
// and those metadata plugins declare.
var ratingSourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// ValidRatingSourceID reports whether id is a well-formed rating source name.
func ValidRatingSourceID(id string) bool {
	return ratingSourceIDPattern.MatchString(id)
}

// IsBuiltinRatingSource reports whether id is one of Silo's own sources, which
// a plugin may report but not redefine.
func IsBuiltinRatingSource(id string) bool {
	_, ok := ratingSourceRanks[id]
	return ok
}

// RatingSourceAlwaysShown reports whether clients show a source whatever the
// administrator chose: Silo's own, IMDb and TMDB. A plugin's source shows only
// once an administrator turns it on.
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

// RatingSourceRank orders sources for display: Silo's own first, then every
// other source.
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
