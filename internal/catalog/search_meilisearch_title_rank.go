package catalog

import (
	"slices"
	"strings"
)

// A keyword search ranks its kept hits by points, highest first, and keeps
// Meilisearch's order between hits with equal points, so proximity, attribute
// rank, word position and popularity still order those.
//
// Category points come first and always decide: a movie or series whose title
// matches the search ranks above every episode, and an episode whose title
// matches ranks above every hit that matched by anything else (a person's
// name, a studio, a genre, a keyword). Match points then order hits of one
// category by how their title matches: the whole title is the search, then
// the title starts with it, then it holds the search's words elsewhere, less
// for each typo.
//
// Meilisearch alone cannot rank this way. Its last tiebreak is the order
// documents were indexed in, and the index is built in content ID order, so an
// episode titled "Pressure" ties with the movie Pressure on every rule and
// ranks first (episode IDs sort before movie IDs). Popularity does not
// separate them for an item no rating source counts votes for.
const (
	// searchPointsMediaTitle are the category points of a movie or series
	// whose title matches the search.
	searchPointsMediaTitle = 10
	// searchPointsEpisodeTitle are the category points of an episode whose
	// title matches the search. Any other hit has none.
	searchPointsEpisodeTitle = 1
	// searchCategoryWeight multiplies category points so that no match points
	// can make up a category: searchPointsExact stays below it.
	searchCategoryWeight = 100

	// searchPointsExact are the match points of a title that is the search's
	// words and nothing else.
	searchPointsExact = 30
	// searchPointsPrefix are the match points of a title that starts with the
	// search's words.
	searchPointsPrefix = 20
	// searchPointsPartial are the match points of a title that holds the
	// search's words but neither is nor starts with them.
	searchPointsPartial = 10
	// searchPointsPerTypo are taken off a title's match points for each typo
	// Meilisearch corrected, never below zero.
	searchPointsPerTypo = 5
)

// meilisearchTitleSearchAttributes are the attributes a search's title query
// matches: the attributes that name the item.
var meilisearchTitleSearchAttributes = []string{
	"title",          //nolint:goconst // A Meilisearch document attribute, not a sort field or column.
	"original_title", //nolint:goconst // As above.
	"sort_title",     //nolint:goconst // As above.
	"title_variants", //nolint:goconst // As above.
}

// titleSearchRequest returns the title query that goes with a keyword query,
// or false when none is sent. It is the keyword query on the title attributes
// only, so its hits are the keyword hits whose title matches and its ranking
// details say how the title matches; the keyword query's details can describe
// a better match in another attribute. A year hint also searches year, as the
// people query's does (meilisearchPeopleQuery), so "pressure 2026" matches the
// title Pressure released in 2026.
//
// None is sent while keyword names no attributes to search on (the index's
// settings are pending, see peopleSearchRequest) or has no word to match.
func titleSearchRequest(keyword meilisearchSearchRequest) (meilisearchSearchRequest, bool) {
	if len(keyword.AttributesToSearchOn) == 0 {
		return meilisearchSearchRequest{}, false
	}
	words, withYear := meilisearchPeopleQuery(keyword.Query)
	if words == "" {
		return meilisearchSearchRequest{}, false
	}
	title := keyword
	title.AttributesToSearchOn = slices.Clone(meilisearchTitleSearchAttributes)
	if withYear {
		title.AttributesToSearchOn = append(title.AttributesToSearchOn, "year")
	}
	title.Vector, title.Hybrid = nil, nil
	title.ShowRankingScoreDetails = true
	return title, true
}

// meilisearchSearchPoints returns a hit's points: its category points times
// searchCategoryWeight plus its match points. title is the hit's ranking
// details from the title query, nil when its title did not match.
func meilisearchSearchPoints(hit meilisearchSearchHit, title *meilisearchRankingScoreDetails) int {
	if title == nil {
		return 0
	}
	category := searchPointsMediaTitle
	if hit.Type == "episode" { //nolint:goconst // The indexed document type.
		category = searchPointsEpisodeTitle
	}
	match := searchPointsPartial
	if title.Exactness != nil {
		switch title.Exactness.MatchType {
		case "exactMatch":
			match = searchPointsExact
		case "matchesStart":
			match = searchPointsPrefix
		}
	}
	if title.Typo != nil {
		match = max(match-searchPointsPerTypo*title.Typo.TypoCount, 0)
	}
	return category*searchCategoryWeight + match
}

// rankMeilisearchHitsByPoints orders hits by meilisearchSearchPoints, highest
// first, keeping their order on a tie. titleHits are the title query's hits;
// a hit is a title match when it is among them. With no title hits every hit
// scores zero and the order is unchanged.
func rankMeilisearchHitsByPoints(hits, titleHits []meilisearchSearchHit) []meilisearchSearchHit {
	if len(titleHits) == 0 {
		return hits
	}
	titles := make(map[string]*meilisearchRankingScoreDetails, len(titleHits))
	for _, hit := range titleHits {
		id := strings.TrimSpace(hit.ContentID)
		if _, exists := titles[id]; exists || id == "" {
			continue
		}
		details := hit.RankingScoreDetails
		if details == nil {
			details = &meilisearchRankingScoreDetails{}
		}
		titles[id] = details
	}
	points := make(map[string]int, len(hits))
	for _, hit := range hits {
		id := strings.TrimSpace(hit.ContentID)
		points[id] = meilisearchSearchPoints(hit, titles[id])
	}
	ranked := slices.Clone(hits)
	slices.SortStableFunc(ranked, func(a, b meilisearchSearchHit) int {
		return points[strings.TrimSpace(b.ContentID)] - points[strings.TrimSpace(a.ContentID)]
	})
	return ranked
}
