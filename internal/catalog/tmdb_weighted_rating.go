package catalog

import "fmt"

// TMDBWeightedRatingSQL is the vote-weighted TMDB rating discovery rows rank
// by: the vote average pulled toward 6.5 as if 500 more people had voted 6.5,
// (votes × average + 500 × 6.5) / (votes + 500). A title rated 10 by three
// people ranks near 6.5; one rated 8.4 by 20,000 keeps about 8.4. The pair
// (media_items.tmdb_vote_count, tmdb_vote_average) is kept in step with the
// item's 'tmdb' rating source by a database trigger.
//
// Migration 20261008200123_index_tmdb_weighted_rating indexes this exact
// expression; change both together. Order by it DESC NULLS LAST, then
// content_id, and require tmdb_vote_count so the partial index applies.
func TMDBWeightedRatingSQL(alias string) string {
	return fmt.Sprintf("((%[1]s.tmdb_vote_count * %[1]s.tmdb_vote_average + 3250) / (%[1]s.tmdb_vote_count + 500))", alias)
}
