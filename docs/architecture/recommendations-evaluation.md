# Evaluating recommendations

This document defines the measurements used to judge recommendation output
against the Recommendations acceptance criteria in issue #1139: AC1 (a
personalized, varied set per profile, refreshed in the background, with
sensible choices for a new profile) and AC2 (clients render the output, and a
default row instead of an empty shelf when there is none). It elaborates AC1
and AC2 and does not change them. It sets no pass or fail values: the
validators agree those on the validation tasks before they test.

Every metric is computed from `/api/v2` responses as the acting profile sees
them, so it measures what clients receive rather than internal state, and it
does not depend on the engine's weights. `scripts/recs-probe/capture.sh`
captures the responses and prints the numbers; see [Probe](#probe). How rows
are built and served is in [recommendations.md](recommendations.md).

## Terms

- **Served window.** The first 20 cards of a row as v2 returns it with
  `limit=20`, the default. Section pages (`getRecommendationSection`) return up
  to 60 cards and are not served windows.
- **Personal row.** A row whose `type` is `cluster` (the main For You row from
  `getForYouMain` and the "Because you enjoy …" rows) or
  `similar_users_liked`. Other row types are server-wide. `getForYouMain`
  answers `200` with `items: []` when the profile has no row, so a check of
  the main row's `type` or `title` must also check that it has cards.
- **Upcoming card.** A Discover card with an `upcoming_event` announces an
  airing; it is not a recommendation and every metric leaves it out.
- **History.** The profile's items in `listHistory` (`GET /api/v2/history`)
  whose `watch.completed` is true, plus its `listFavorites` items, each counted
  once. Taste-seed picks are favorites. History cards for episodes are their
  series, as recommendation cards are.
- **History genre share.** For genre g, the share of history items that carry
  g. The profile's top genres are those with the largest shares.
- **Accessible catalog.** What `listCatalogItems` (`GET /api/v2/catalog`)
  returns for the profile, which applies its access. Its `total` counts it;
  `type` and `genre` narrow it, one media type and one genre per request.

## Personalization

**Overlap between profiles.** For two profiles on one account, the Jaccard
index of their served main rows: shared cards divided by distinct cards, by
`content_id`. The same index over the union of each profile's personal rows
(the main row, `listForYouRows` and the personal rows of `getDiscover`) shows
whether the rest of the page is shared. #1139 notes that profiles need not
have disjoint sets.

**On-taste share and lift.** For each of the profile's top two history genres
g, compute per media type (movie, series):

- served share: the share of served main-row cards of that type that carry g;
- catalog share: `total` of `GET /api/v2/catalog?type=<type>&genre=<g>&limit=1`
  divided by `total` of `GET /api/v2/catalog?type=<type>&limit=1`;
- lift: served share divided by catalog share.

A lift near 1 means the row carries g no more often than the profile's
catalog does. A genre that most of the catalog carries cannot reach a high
lift, so read the served share against the history share as well.

## Variety

**Genre shares.** The share of served main-row cards carrying each genre,
against the history genre shares.

**Amplification.** The served share of the served row's top genre minus that
genre's history share. Positive values mean the row concentrates on the top
genre more than the history does.

**Taste coverage.** For each top history genre, the number of served main-row
cards carrying it, and whether a cluster row on the page names it in its
title. A profile with two distinct tastes shows whether both reach the page.

## Repetition

**Within one load.** Cards that appear in two or more Discover rows, and cards
that appear in two or more home recommendation sections (`section_type`
`recommended_for_you`, `because_you_watched`, `similar_users_liked` or
`taste_match`, from `listHomeSections`).

**Across loads.** For the same profile, the number of shared cards between
two loads of the served main row, over the first 10 and the first 20 cards.
Measure it for two loads on one day and, for a profile with no new signals,
for loads on consecutive days. Rows change between loads when a refresh wrote
new rows (see [Refresh window](#refresh-window)), and rows chosen by date, such
as Discover's genre rows, change with the date.

## Leakage

Count the served cards, in every row of `getForYouMain`, `listForYouRows`,
`getDiscover` and the home recommendation sections, that are:

- **finished:** in the history with `watch.completed` true, or flagged
  `user_state.played`;
- **favorited:** in `listFavorites`, including taste-seed picks, or flagged
  `user_state.is_favorite`;
- **rated low:** rated 1 or 2 in `listRatings` (`GET /api/v2/ratings`);
- **not accessible:** answered `404` by `getCatalogItem`
  (`GET /api/v2/catalog/items/{id}`) for the profile, or rated above the
  profile's `max_content_rating` (`listProfiles`);
- **out of 1.0 scope:** of a type other than `movie` or `series`, in a personal
  row of a profile with no book activity.

Watchlist titles are not leakage: they stay recommendable.

## New-profile usefulness

**Before any signal.** For a profile with no history: the number of
`getDiscover` rows and the cards in each, on the first request, without a
nightly run or an admin action in between. AC2 asks that clients then show a
default row rather than an empty shelf.

**After the taste seed.** After `createTasteSeed`
(`POST /api/v2/recommendations/taste-seed`) with a few picks:

- whether the next `getDiscover` and `listForYouRows` reads hold a personal
  row, and how long after the submission it appeared;
- how many picks appear in any row (they are favorites, so this is leakage);
- the share of personal-row cards sharing a genre with at least one pick.

The submission answers once the profile's refresh has finished, or after
about 5 seconds; a slower refresh finishes in the background.

## Refresh window

The refresh window is the time from a signal (a rating, favorite, watchlist
entry, watch progress or history import) to the first read whose rows reflect
it. `getTasteProfile`'s `updated_at` moves past the time of the signal when the
profile's refresh has stored its taste profile; the refresh writes the
profile's rows next. The schedule that bounds it:

| Path | When it runs |
| --- | --- |
| Signal write (native and Jellyfin handlers, history imports, watch-provider syncs, media-server webhooks) | At once, on the server that took the write, after any refresh already queued there; one at a time per server, each with a 2-minute budget |
| Taste seed | At once, on the server that took it; the request waits up to about 5 seconds |
| Stale sweep | Every 5 minutes, up to 50 profiles with a taste profile marked stale, oldest mark first; picks up a refresh whose server dropped it |
| Read of a profile whose rows are missing | Requests a refresh at most once per 15 minutes per profile on each server |
| Nightly jobs | Embeddings 03:00, taste profiles 04:00, co-watch 04:30, rows of every profile with a taste profile 05:00, in server-local time (the `recommendations.*_cron` settings) |

Cached rows expire 26 hours after the run that wrote them. Ebook and
Audiobookshelf progress (beta) do not request a refresh; those profiles
refresh at the next nightly taste run or stale sweep.

## Probe

`scripts/recs-probe/capture.sh` sends only GET requests. It takes the server
URL and a token from the environment and, per profile, saves the
`getTasteProfile`, `getForYouMain`, `listForYouRows`, `getDiscover`,
`listHomeSections`, `listHistory`, `listFavorites` and `listRatings` responses
as JSON. It prints one line per check: signal counts, main row and Discover
row sizes, leakage counts, repeats within the load, card types in personal
rows, and genre shares with amplification. It then prints the overlap between
each pair of profiles and, given an earlier capture in `PREV_DIR`, the
day-over-day overlap of each main row. It captures the first 200 entries of
the history, favorites and ratings and marks a list that has more. It prints
numbers, never a verdict. Run it with `--help` for its settings.

Catalog shares for lift, access checks of single cards and timings of the
refresh window are taken by hand with the requests named above.
