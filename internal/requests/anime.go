package requests

// animeKeywordID is TMDB's "anime" keyword id. Matches Seerr's ANIME_KEYWORD_ID
// exactly (server/api/themoviedb/constants.ts). Detection is keyword-id only —
// no genre/language fallback — to mirror upstream behavior.
const animeKeywordID = 210024

func detectAnime(keywordIDs []int) bool {
	for _, id := range keywordIDs {
		if id == animeKeywordID {
			return true
		}
	}
	return false
}
