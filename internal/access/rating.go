package access

import "strings"

var ratingRank = map[string]int{
	"G": 0, "TV-Y": 0, "TV-G": 0,
	"PG": 1, "TV-Y7": 1, "TV-PG": 1,
	"PG-13": 2, "TV-14": 2,
	"R": 4, "NC-17": 4, "TV-MA": 4,
	"AU-G": 0, "AU-PG": 1, "AU-M": 2, "AU-MA15+": 3, "AU-R18+": 4, "AU-X18+": 5,
}

type RatingRankEntry struct {
	Rating string
	Rank   int
}

var ratingRankEntries = []RatingRankEntry{
	{Rating: "G", Rank: 0},
	{Rating: "TV-Y", Rank: 0},
	{Rating: "TV-G", Rank: 0},
	{Rating: "PG", Rank: 1},
	{Rating: "TV-Y7", Rank: 1},
	{Rating: "TV-PG", Rank: 1},
	{Rating: "PG-13", Rank: 2},
	{Rating: "TV-14", Rank: 2},
	{Rating: "R", Rank: 4},
	{Rating: "NC-17", Rank: 4},
	{Rating: "TV-MA", Rank: 4},
	{Rating: "AU-G", Rank: 0},
	{Rating: "AU-PG", Rank: 1},
	{Rating: "AU-M", Rank: 2},
	{Rating: "AU-MA15+", Rank: 3},
	{Rating: "AU-R18+", Rank: 4},
	{Rating: "AU-X18+", Rank: 5},
}

// RatingAllowed reports whether a content rating is visible under the ceiling.
func RatingAllowed(rating, ceiling string) bool {
	if strings.TrimSpace(ceiling) == "" {
		return true
	}
	ratingValue, ok := ratingRank[strings.ToUpper(strings.TrimSpace(rating))]
	if !ok {
		return false
	}
	ceilingValue, ok := ratingRank[strings.ToUpper(strings.TrimSpace(ceiling))]
	if !ok {
		return false
	}
	return ratingValue <= ceilingValue
}

// AllowedRatingsUpTo returns the normalized ratings permitted by the ceiling.
func AllowedRatingsUpTo(ceiling string) []string {
	if strings.TrimSpace(ceiling) == "" {
		return nil
	}
	ceilingValue, ok := ratingRank[strings.ToUpper(strings.TrimSpace(ceiling))]
	if !ok {
		return []string{}
	}

	ratings := make([]string, 0, len(ratingRank))
	for rating, value := range ratingRank {
		if value <= ceilingValue {
			ratings = append(ratings, rating)
		}
	}
	return ratings
}

// RatingRank returns the maturity rank for a normalized content rating.
func RatingRank(rating string) (int, bool) {
	rank, ok := ratingRank[strings.ToUpper(strings.TrimSpace(rating))]
	return rank, ok
}

// RatingRankEntries exposes the canonical maturity ordering used for access and sorting.
func RatingRankEntries() []RatingRankEntry {
	return append([]RatingRankEntry(nil), ratingRankEntries...)
}
