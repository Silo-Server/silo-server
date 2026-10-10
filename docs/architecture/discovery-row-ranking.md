# Discovery row ranking

The rating-led Home rows (Critically Acclaimed, Hidden Gems, Forgotten
Favorites, Short & Sweet, Genre Roulette and the mood rows) choose titles by
TMDB rating. A rating alone is not enough on a large library: thousands of
titles carry a perfect 10 from one to three votes, and they would fill every
row. These rows therefore require a minimum TMDB vote count and rank by a
vote-weighted rating.

## Vote counts

Metadata providers report TMDB's score and vote count as the `tmdb` rating
source, which the server stores in `media_item_rating_sources`. The TMDB
plugin sends it from silo.tmdb 1.2.25; MDBList sends it too. A trigger on that
table (`trg_media_item_rating_sources_tmdb_votes`) copies the row's count and
average (score / 10) onto `media_items.tmdb_vote_count` and
`tmdb_vote_average` in the same statement as every insert, update or delete of
a `tmdb` row, so the rows can filter and index them without a join and no
writer can leave them stale. Because the trigger updates the item after the
source row, every writer of rating sources locks the item first
(`RatingSourceRepository` does it explicitly; catalog import and content-ID
renames already do), so writers wait for one another instead of deadlocking. A
missing source or a zero count leaves both NULL, and a NULL count never
qualifies: an item without a known count is left out until a refresh supplies
one. Catalog transfer carries each item's `tmdb` rating source, so the trigger
derives the pair on import as well; a bundle exported before that leaves the
target's sources in place.

The pair always comes from one row. `rating_tmdb` is not used for ranking: a
scheduled refresh never overwrites it, so it can predate the stored count, and
a stale 10.0 weighted by a newer count would rank as acclaimed.

A manual or scheduled refresh overwrites the rating sources the refresh chain
reports, so counts keep moving after release; the chain's provider order picks
one entry per source, so the same provider wins each time. The bulk enrichment
pass carries one provider's answer and only fills sources an item lacks, so
MDBList cannot replace the TMDB plugin's `tmdb` row.

## Weighted rating

Rows order by `(votes × average + 500 × 6.5) / (votes + 500)`: the average pulled
toward 6.5 as if 500 more people had voted 6.5. A title rated 10 by three people
ranks near 6.5; one rated 8.4 by 20,000 keeps about 8.4.

`catalog.TMDBWeightedRatingSQL` builds the expression and
`catalog.DiscoveryRatingOrder` the full order (`DESC NULLS LAST`, then
`content_id`). The partial index `idx_media_items_tmdb_weighted_rating` covers
that exact expression and order; queries require `tmdb_vote_count` so the
partial index applies. Change the expression, the index migration and
`TestTMDBWeightedRatingMatchesItsIndex` together.

All of these rows go through `catalog.DiscoveryRepository`, which intersects a
section's libraries with the viewer's allowed libraries and applies the access
filter's content allow-list and name prefix (`catalog.AppendContentScope`).

Rows that sort by rating without requiring a vote count (format showcases,
anniversaries, seasonal keyword picks) use `catalog.RatedOrder`: the weighted
rating first, then titles with no known count by `rating_tmdb` and then
`rating_imdb`, so a perfect score from a few votes never leads them either.
The default Top Rated library rows are rule rows and keep the sort their
configuration names.

## Minimums

`recipes.AcclaimedMinVotes` (500) applies to Critically Acclaimed and
`recipes.DiscoveryMinVotes` (100) to the other rows. Each row's own TMDB rating
floor (for example 8.0 for Critically Acclaimed) applies to the unweighted
`tmdb_vote_average`. Preset descriptions state both numbers, and the web's Add
row preview shows the description when nothing matches. The web row list and
picker repeat the rule; a test ties their text to the server's descriptions.

## Daily mix

Critically Acclaimed, Hidden Gems, Forgotten Favorites, Short & Sweet and the
mood rows fetch five times their item limit (at most 250 titles, never fewer
than the limit) by weighted rating and show a deterministic daily pick from
that pool, in pool order (`sections.dailyBestOf`). The pick is keyed by row kind
(and mood) and the UTC day, so viewers of the same pool see the same titles all
day. The shared rows carry the day in their resolved-list cache key, so every
node switches at the same UTC midnight. A profile that hides watched titles
fetches a larger window, so its pick can differ. Genre Roulette already rotates
its genre and shows that genre's best titles without a daily pick; it picks
from genres among the titles the row could show (movies and series that meet
its rating floor and the vote minimum), so the chosen genre can fill the row.
