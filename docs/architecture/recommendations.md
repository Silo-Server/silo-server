# Recommendations

Recommendations turn each catalog item into an embedding, build a taste
profile per household profile from its signals, and cache rows of candidates
that reads filter for the viewer. Rows are built in the background, and reads
never call the embedding provider: the only interactive embedding calls are
catalog search queries and the admin connection check.

The code lives in `internal/recommendations`: the embedding backfill
(`embed_backfill.go`, provider client in `embeddings/`), taste profiles
(`taste.go`, `taste_signals.go`, `signals.go`, `cluster.go`), row building
(`personal.go`, `collaborative.go`, `cowatch.go`, `diversity.go`), the worker
and its jobs (`worker.go`, `jobs.go`), the admin reset
(`embedding_reset.go`), and the cached-row reads (`reader.go`). Native reads
go through `internal/api/handlers/recommendations*.go` and
`internal/apiv2/recommendations.go`; home and library sections through
`internal/sections`; Jellyfin's `/Movies/Recommendations` through
`internal/jellycompat`. Admin operations are in
[admin-recommendations-api.md](../admin-recommendations-api.md); the
measurements used to judge the output are in
[recommendations-evaluation.md](recommendations-evaluation.md).

## Jobs

Every API node runs a `Worker`. Each job takes the in-process running flag and
a Postgres advisory lock for its kind before it starts, so one node runs it at
a time across the cluster; a busy job is skipped by cron and answered with
`409` by a manual trigger. Each finished run is recorded in `task_executions`
under `recommendations.embeddings`, `recommendations.taste_profiles`,
`recommendations.cowatch` and `recommendations.cache`, with its counts as
`result_data`. A run that hit partial failures is `completed` and counts them.

| Job | When | What |
| --- | --- | --- |
| Embeddings | `recommendations.embeddings_cron` (03:00) | Pass 1 then Pass 2, below |
| Embedding catch-up | every 15 minutes, and at startup when items need it | Pass 1 only, under the embeddings job's claim; idle runs are not recorded, failed ones are |
| Taste profiles | `recommendations.taste_profiles_cron` (04:00) | Rebuild every profile with signals or a taste row |
| Co-watch | `recommendations.cowatch_cron` (04:30) | Rebuild `item_cowatch`, pruning pairs the run did not rewrite |
| Cache | `recommendations.recommendations_cron` (05:00) | Global rows, then the rows of every profile with a taste-profile row, oldest For You row first (profiles without one first) |
| Stale sweep | every 5 minutes, own lock | Refresh up to 50 stale profiles, oldest mark first |

At startup the worker also builds the global rows when none are cached, so
cold-start rows exist before the first nightly cache run. Recently Added
fills from any matched movie or series; Popular needs viewing by at least two
accounts and stays empty on smaller servers.

## Embeddings

Pass 1 embeds items with no embedding or one from another model. Pass 2 finds
items whose stored canonical text no longer matches `BuildEmbeddingText`; it
rebuilds the text in SQL, so the people ordering there must match Go's
byte-wise sort (`COLLATE "C"` and the full sort key), and every SQL candidate
is re-checked in Go before it counts against the 200-item quota.

The first stored vector writes the embedding lock (base URL, model, source and
storage dimensions) in `server_settings`. Every later vector must match it.

- A vector is checked before the lock is read or written: an empty vector or
  one wider than `embeddingvectors.CanonicalDimensions` (3072) ends the run,
  so an unusable model never pins the lock.
- Vectors are zero-padded to 3072 for storage; cosine is unaffected.
- Changing the model or base URL requires the admin embeddings reset, which
  deletes the lock, embeddings, taste profiles, clusters and per-profile
  cached rows in one transaction while holding the embedding, taste and cache
  job claims and the stale sweep lock.
- Catalog seed imports keep only embeddings from the locked model, or the
  configured one when nothing is locked, and never write the lock.
- The canonical text stored with a vector is always the full text, even when
  the provider only accepted a shortened copy, so Pass 2 does not re-embed it.

A run stops at once on provider limits, rejected credentials, a wrong model or
URL (401/403/404) and unreachable hosts. It also stops after three single-item
failures in a row, and when the first single-item retry after a failed batch
fails before anything was stored. An input the provider refuses at every
length (a 4xx answer other than 401, 403, 404, 408 and 429) counts as skipped
and is not a failure: refused items cannot stop a run, and a run whose only
misses are refused inputs completes. The first refusal before anything is
stored is followed by one call with a fixed test text, and a provider that
refuses that too, as Gemini does a bad API key with 400, stops the run. Each
server remembers in memory the items it saw refused, with a hash of their
text, and its catch-up passes skip them until the text changes; the nightly
or a manual embedding run clears the record and retries them, and so does a
restart. A lock that conflicts with the configuration fails every run,
including the catch-up pass every 15 minutes, until an administrator resets
embeddings or restores the settings.

Search indexes only vectors from the locked model, and semantic search is
ready for a type once a lock exists and at least 85% of that type's eligible
items carry one of its vectors.

## Signals and taste profiles

Signals are ratings, favorites, watchlist entries, video watch progress and
history, and ebook reading progress. The write paths for ratings, favorites,
watchlist and video progress call `Worker.NotifySignalsChanged`, which marks
the profile stale and queues a refresh on that server: native and Jellyfin
handlers, history imports (once per run), watch-provider syncs (once per run
that changed local state) and media-server webhooks. Catalog merges and splits
only mark the affected profiles stale, for the stale sweep. Changes to a
profile's access scope also notify (see Access). Ebook and Audiobookshelf
progress (beta) do not notify; those profiles refresh at the next nightly taste
run or stale sweep.

A taste-seed submission calls `Worker.RefreshProfileNow` instead: it marks the
profile stale, refreshes it on that server at once and waits up to 5 seconds,
so the client's next read shows the picks' effect. The refresh holds the
profile's pending key, so the queue and the sweep do not start a duplicate on
that server, and it runs detached from the request with the usual 2-minute
budget: a slower refresh finishes in the background and is not queued again.
At most 4 run at once per server; past that the refresh is queued.

A refresh records the database time it started, stores the taste profile with
that time as `updated_at`, and clears only stale marks set before it. A
refresh request made on the same server while the refresh runs makes it run
once more as soon as it ends, so a burst of changes is applied within seconds.
A mark set during the refresh from another server survives, and a failed
refresh marks the profile stale again, so the stale sweep retries both.

- Episodes roll up to their series. A series decays once by its most recent
  signal, as a movie decays by its last watch. A series with no signal for
  30 days counts in proportion to its episodes watched with a positive
  weight: in full from 5, and at least a quarter. A series being watched now
  is not scaled, and neither is a negative series score.
- Progress below 15% counts as abandonment only after 14 days.
- The taste vector and clusters average the titles with a positive weight
  only. Dislikes and abandoned titles still count in `signal_counts`, but do
  not pull the vector away from themselves. A 3-star rating adds no weight of
  its own and halves the title's watch and intent weight, so a 3-star
  completion weighs less than an unrated one.
- Clusters split the same titles into interests. A profile under 10 titles
  has one cluster. A larger one gets 2 (10-19 titles), 3 (20-59), 4
  (60-199) or 5 (200 or more), or more, up to 5, when a partition into
  more has a higher silhouette (cosine distance) and one of at least 0.1;
  every cluster holds at least 3 titles. A partition is the best of up to 8
  deterministic k-means++ seedings (fewer above 100 titles, one from 800),
  by inertia, among those leaving no cluster under 3 titles. One seeding
  can put two seeds in one interest and leave a sliver that merging folds
  away together with every other interest. Real distinct
  interests scored 0.13-0.22 and one interest cut in two under 0.08
  (gemini-embedding-001 on a real catalog).
- Watch, rewatch, favorite and watchlist counts in `signal_counts` count each
  canonical title once (a watch in the bucket of its strongest watch), so one
  long series is one title.
- A refresh also stores `positive_titles` in `signal_counts`: the titles with
  an embedding and a positive weight, each once, leaving out titles that are
  only on the watchlist. The cold-start level counts them: 0 titles is
  level 0, global rows only; 1-2 is level 1, global rows then one personal
  row; 3-9 (three is the taste-seed picker's minimum) is level 2, personal and
  global rows interleaved, personal first; 10 or more is level 3, personal
  rows first. A row without the entry, stored before it existed, uses the sum
  of its positive signal counts until its next refresh. The taste-profile
  summary leaves `positive_titles` out, so its `signal_counts` holds only
  kinds of signal.
- A profile with no positively weighted title that has an embedding keeps
  its row with a `NULL` taste vector and loses its clusters and cached
  personal rows. Vector reads treat it as
  having no taste profile. A read of a profile with positive signals but no
  personal rows asks for a refresh even at level 0, since its titles may have
  gained embeddings since.
- Clusters are seeded from item IDs only, so decaying weights do not reshuffle
  them between rebuilds. A cached cluster row carries the title of the build
  that produced it.
- The taste-profile summary's genres come from the clusters: the first
  dominant genre of each cluster, heaviest first, then the second of each,
  without repeats, at most five, so a second interest shows next to the
  heaviest. A profile without clusters has none. Favorite directors still
  come from titles rated 4 stars or more and favorites.

## Rows

Personal rows leave out the profile's exclusion set: watched titles and
favorites (which include taste-seed picks), canonicalized episode to series.
Watchlist titles stay recommendable. The set is applied when rows are built
and again when cached rows are read.

Similar Users needs at least 3 peer accounts and at least 2 supporting
accounts per title, counted by account, so no single household's ratings are
shown on their own. Below the floor the row is cached empty.

Every row, personal or not, and the taste-seed picker offer matched movies
and series only, the types in `recommendableMediaTypes` (see Media types
under Ranking). Popular counts login accounts, not profiles: a title needs
at least 2 accounts that watched it in the last 90 days, so a
single-account server has no Popular row, and the cached row keeps 200 titles
for reads to filter. Genre rows rank a genre's titles by catalog rank, with
watching accounts only as a tie-break. Catalog rank (`catalogRatingOrderSQL`)
puts notable titles first (a logo and at least five keywords, since most
titles have no recorded vote count and an obscure title's rating rests on a
handful of votes), then rating reliability (an IMDb rating below 9.6, then a
TMDB rating below 9.5; higher scores are nearly always a few votes or a
copied score), then that rating. Highly Rated, the picker and the quality
prior use the same ratings. The 16 cached genres are those most accounts watched, once at
least 2 share one, then the largest. The picker interleaves its best 600
candidates by first genre, then continues in rank order.

Discover and its section pages serve two default rows live: Highly Rated in
Your Library (a catalog rating of at least 7.0, movies and series
interleaved in proportion to how many of each the viewer can see) and
Recently Added (by the title's or its latest episode's addition, with no
window). Reads query them under the viewer's access filter and exclusion
set, so they show on a fresh server, with recommendations disabled, and for
a restricted profile. Highly Rated is never cached. The cached global
Recently Added row uses the same query without the access filter and also
has no window, so on a server with no Popular row a new profile's cached
reads still get a row.

Both queries read each media type from its own index
(`idx_media_items_type_added_at`, `idx_media_items_type_catalog_rank`)
only as deep as the row needs, so a read's cost does not grow with the
catalog. The index expressions repeat `addedAtSQL` and
`catalogRatingOrderSQL` and must change with them. Each read is planned for
its own exclusion set rather than reusing a generic plan. The per-type
title counts Highly Rated interleaves by are the one part that reads every
title, so each server reuses an access scope's counts for 10 minutes.
Discover reads the profile's watched set once per request and shares it
between its rows and the airings it blends in.

Cache rows expire 26 hours after the run that wrote them, past the next daily
cache run. A global row whose rebuild fails keeps its last good version until
the new run's expiry; a global row whose rebuild finds nothing, and a genre row
whose genre left the menu, is deleted. A personal row whose build fails keeps
its cached version and the refresh is retried; a main or Because You Watched
row whose rebuild finds nothing is deleted, and a cluster row that rebuilds
empty is cached empty (see Cluster rows).

## Access

Rows are built under the scope the API resolves for the profile (the policy
viewer resolver, with `access.Resolver` as the legacy fallback): account and
access-group libraries, profile restrictions, hidden libraries and maturity
limits, with PIN verification skipped. A profile
whose scope cannot be resolved is not cached. Profile restriction, hidden
library, account and access-group changes trigger a rebuild; account- and
group-wide changes mark the affected profiles stale in one statement for the
stale sweep.

Every read filters again with the viewer's access filter. List endpoints that
return bare identifiers filter before answering, and a list anchored on an
item the viewer cannot see answers as one anchored on an unknown item.
Deleting a profile purges its ratings, taste profile, clusters and cached rows
on both user-store backends. A refresh that runs afterwards writes no taste row
for a profile the user store no longer lists, and on the Postgres store the
nightly taste job skips such profiles, as the purge migration does.

## Ranking

A cached personal row holds `CacheCandidateLimit` (60) titles; a public read
serves the first `ServedRowSize` (20) by default. Ranking shapes those 20
for the viewer, and the rest is headroom for read-time filters and for
library sections, which scope the whole row to their libraries.

- **Main row.** A profile with at least 10 positive titles and at least two
  clusters gets a row composed per cluster, each cluster an anchor. Anchors
  are not merged by centroid cosine: a centroid averages away its titles'
  differences, so centroids of large clusters of different genres sit
  above 0.9 (most pairs with 50 or more titles on a real catalog), while
  centroids of 3-title halves of one genre sit near 0.82. The clustering
  decides what is one interest. Anchors get slots in proportion to
  their weight by largest remainder, each at least `min(3, limit/anchors)`.
  Each anchor fetches 3 candidates per slot around its centroid and ranks
  them by MMR, and a smooth weighted round-robin interleaves the anchors'
  rankings without repeats, so every prefix of the row holds each interest's
  share. Any other profile, or one whose anchors find nothing, gets the
  candidates nearest its averaged taste vector, ranked by MMR.
- **Genre pass.** In a row from the averaged taste vector, a stable reorder
  of the MMR order lets no genre hold more than half of the served window
  while the row has other titles to offer. It deletes nothing and moves
  nothing past the window. A composed row skips it: its slots already
  spread the window over the interests by weight, and the cap would cut an
  interest heavier than half the profile below its share.
- **Type supplements.** The main row holds at least a fifth of its length of
  each of `recommendableMediaTypes` the viewer can see, so a library section
  fills. The extra titles go after the served window, replacing tail titles
  of types above that floor; a type the viewer has no titles of is not
  queried.
- **Cluster rows.** A cluster row is built `max(3, 60 × weight share) + 20`
  long (at most 60) from candidates sharing one of its dominant genres, then
  loses the main row's first 30, every title its daily rotation can serve,
  so the page does not repeat the main row.
  A row left with fewer than 10 titles is cached empty, and a row cached
  empty counts as built: reads do not ask for a rebuild of it.
- **Quality prior.** Before MMR a candidate's score becomes
  `score + 0.5 × sd(pool scores) × clamp(z, −1, 1)`, where `z` standardizes
  its catalog rating (trusted IMDb, else trusted TMDB) among the pool's rated
  candidates of the same media type. A type with fewer than 10 rated
  candidates or no spread in ratings is left alone, and cluster and Because
  You Watched pools under 30 candidates get no prior.
- **Freshness.** Before MMR, on the main row's pools and the cluster pools,
  the positive score of a title added in the last 14 days is multiplied by up
  to 1.05, less the older it is. A pool with more than a quarter of its
  candidates inside the window, such as a freshly imported library, gets no
  boost, so it never ranks by scan order. The date is the title's own, so a
  new episode of an older series does not count, and the boost cannot reach
  a title its candidate query did not retrieve.
- **Labels.** A cluster's label names the genres that set it apart: those
  that at least 40% of its titles carry, and carry at least 1.5 times as
  often as the profile's positive titles do, most distinctive first, with
  its most common genre (a tie goes to the genre of the heavier titles)
  always among them, at most two in all. Genres are joined with ", ", since
  TMDB genres such as "Sci-Fi & Fantasy" contain "&". A cluster with no such
  genre, such as a profile's only cluster, is labeled by its most common
  genre, and one whose titles carry no genres has an empty label, titled
  "Picked from your history". The dominant genres that drive retrieval, the
  Discover genre exclusion and the taste-match section stay the top three
  by count. The thresholds were tuned on synthetic profiles.
- **Repeated titles.** A read never shows two cluster rows under one title
  it can tell apart. Visiting the heaviest cluster first, a row titled like
  a heavier one is hidden when their first served items overlap by more
  than half (Jaccard index), and otherwise takes the first of its dominant
  genres its title does not name yet. A row whose cached title its cluster
  no longer has keeps it. A hidden row was built, so it does not count as
  missing. A main or cluster row's "see all" page reads the profile's page
  rows the same way, for a 20-item window and the day's rotation, and takes
  the title and order they give its row, so it opens with the titles of the
  row it was opened from; a row they hide keeps its cached title and order.
- **Rotation.** Reads rotate the main row and the cluster rows daily, after
  filtering and before trimming; Because You Watched, Similar Users, Watch
  Tonight and the global and default rows stay in rank order. For a window of `limit` items, when the row is longer and `limit`
  is above 10, the first `min(10, limit/2)` stay, and the other
  `take = limit − pin` are drawn from the next `2 × take`, the title at tail
  index `i` with weight `1/(i+5)`: the draw keeps the `take` largest
  `ln(u)·(i+5)`, with `u` from `sha256(userID|profileID|rowKey|date|itemID)`.
  The draws keep rank order and the titles not drawn follow them, so
  nothing is lost. `rowKey` is the row's cache key, so a title two rows
  share is drawn independently in each, and `date` is the server's local
  `YYYY-MM-DD`, so rows change overnight. The draw needs no shared state:
  every node serves the same rotation, provided all nodes run in the same
  time zone. Home and library sections rotate the main and taste-match rows
  like a 20-item page.
- **Titles.** Row titles come from one vocabulary in
  `internal/recommendations/titles.go`: For You, `Because you enjoy
  <label>`, Because You Watched, Profiles Like You Enjoyed, Popular on This
  Server, Recently Added, Highly Rated in Your Library and `Top <genre>`. A
  For You or taste-match section whose heading is still a default
  ("Recommended for You", "Top Picks Today" or empty) takes the title of the
  row it serves when that row is one every profile is offered, so a new
  profile's section reads "Popular on This Server". A heading an admin chose
  is never replaced.
- **Media types.** Every row offers only `recommendableMediaTypes`, movies
  and series: Popular, the genre rows, the live default rows (Highly Rated
  in Your Library, Recently Added), the taste-seed picker, and the personal
  rows. For personal rows the list gates the taste candidates (the main
  row, cluster rows, type supplements and Watch Tonight's discover
  candidates), Because You Watched anchors (a finished audiobook is passed
  over for the next movie or series), co-watch neighbors and Similar Users
  candidates. Reads drop other types from cached rows in the access check
  they already run, so rows cached before a type left the list stop
  showing it at once. Books still shape the taste vector. An item's own
  "More like this" list keeps to the item's type, co-watch neighbors
  included, so an audiobook's page can still list audiobooks. See Rows for
  how each row ranks.

## Reads

With recommendations disabled the Reader serves no personal rows (the main
For You row, cluster rows, Because You Watched, Similar Users), even ones
still cached from before, and reads fall back to the global and default rows.

The Reader serves cached rows, except the default rows on Discover and their
section pages; home and library sections keep the cached Recently Added row
instead, since a library has its own shelves. The other live query
is the server's top genre, for a taste-match section with no genre and no
matching cluster. The standalone popular and recently-added list endpoints
query the catalog live rather than reading the cache. Discover leaves out of
each row the items earlier rows show, but not those they cut at the row limit.
A read that
finds a profile's rows missing asks for a refresh at most once per profile per
15 minutes on each server; a profile with signals but no taste-profile row yet
asks too, so a lost first refresh recovers. Because You Watched anchors are the
profile's three most recent completed titles of `recommendableMediaTypes` that
still exist in the catalog, taken from its latest ten such completions and
passing over those it rated 2 stars or lower; the worker, the reads and
Watch Tonight choose them the same way. A read asks for a refresh only when
there are anchors and none has a cached row.

List reads return at most 50 items per row (default 20); the v1 for-you and
similar-users reads keep 20, and section "see all" reads return up to 60, a
whole cached row. Home and library sections read the whole cached pool,
scope it to the section's libraries, then trim to the section's size. The
main row is ranked for every title the profile can see, so a library with a
small share of the catalog can hold few of its titles. A library's For You
section that the main row leaves short continues with the library's titles
from the profile's other personal rows: the cluster rows, heaviest cluster
first, then the Because You Watched rows, most recent anchor first, then
Similar Users, each title once. The fill is read only when the section is
short, and a new profile's global row is never filled: the section is titled
after it.
Jellyfin's `/Movies/Recommendations` sends only rows its headings describe
truthfully: up to two Because You Watched rows under their anchor's title,
dropped when the viewer cannot see the anchor, and the taste-cluster rows
under their genre label (see [jellycompat-api.md](../jellycompat-api.md)). A
per-section items request that fails to load answers `500`; the aggregate
sections endpoints still degrade to empty rows.
