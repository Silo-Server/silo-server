package requests

import "strings"

// animeKeywordID is TMDB's "anime" keyword id. Matches Seerr's ANIME_KEYWORD_ID
// exactly (server/api/themoviedb/constants.ts).
const animeKeywordID = 210024

// animationGenreID is TMDB's "Animation" genre id, shared across movie and TV.
const animationGenreID = 16

// detectAnime reports whether the TMDB metadata classifies the title as anime.
// Detection uses a keyword-first strategy: if the TMDB "anime" keyword (210024)
// is present the title is anime regardless of genre or language. When the keyword
// is absent, a fallback checks for Animation genre (16) combined with an
// original language of ja, zh, or ko (case-insensitive, trimmed).
func detectAnime(keywordIDs []int, genreIDs []int, language string) bool {
	for _, id := range keywordIDs {
		if id == animeKeywordID {
			return true
		}
	}

	lang := strings.ToLower(strings.TrimSpace(language))
	if lang == "" || len(genreIDs) == 0 {
		return false
	}

	if lang != "ja" && lang != "zh" && lang != "ko" {
		return false
	}

	for _, id := range genreIDs {
		if id == animationGenreID {
			return true
		}
	}
	return false
}
