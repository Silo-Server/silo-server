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
[admin-recommendations-api.md](../admin-recommendations-api.md).

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
cold-start rows exist before the first nightly cache run. Each global row
needs activity to fill (watches, ratings, or titles added in the last 14
days), so on an idle server the build can write none, and it is not retried
until the next cache run.

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
fails before anything was stored, unless the provider refused that input. An
input refused at every length counts as skipped and only counts toward the
consecutive limit, so one bad item cannot stop every run. A lock that conflicts
with the configuration fails every run, including the catch-up pass every 15
minutes, until an administrator resets embeddings or restores the settings.

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

A refresh records the database time it started, stores the taste profile with
that time as `updated_at`, and clears only stale marks set before it. A mark
set during the refresh survives, and a failed refresh marks the profile stale
again, so the stale sweep retries both.

- Episodes roll up to their series. A series decays once by its most recent
  signal, as a movie decays by its last watch.
- Progress below 15% counts as abandonment only after 14 days.
- Watch and rewatch counts in `signal_counts` count each canonical title once,
  in the bucket of its strongest watch, so one long series is one title.
- A profile with no positive signal, or none whose titles have an embedding
  yet, keeps its row with a `NULL` taste vector and loses its clusters and
  cached personal rows. Vector reads treat it as having no taste profile. Its
  cold-start level comes from `signal_counts`, so it is level 0 only when it
  has no positive signal; level 0 is served global rows only.
- Clusters are seeded from item IDs only, so decaying weights do not reshuffle
  them between rebuilds. A cached cluster row carries the title of the build
  that produced it.

## Rows

Personal rows leave out the profile's exclusion set: watched titles and
favorites (which include taste-seed picks), canonicalized episode to series.
Watchlist titles stay recommendable. The set is applied when rows are built
and again when cached rows are read.

Similar Users needs at least 3 peer accounts and at least 2 supporting
accounts per title, counted by account, so no single household's ratings are
shown on their own. Below the floor the row is cached empty.

Cache rows expire 26 hours after the run that wrote them, past the next daily
cache run. A global row whose rebuild fails keeps its last good version until
the new run's expiry; a global row whose rebuild finds nothing, and a genre row
whose genre left the menu, is deleted.

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
on both user-store backends.

## Reads

The Reader serves cached rows; the one live query is the server's top genre,
for a taste-match section with no genre and no matching cluster. The
standalone popular and recently-added list endpoints query the catalog live
rather than reading the cache. A read that
finds a profile's rows missing asks for a refresh at most once per profile per
15 minutes on each server; a profile with signals but no taste-profile row yet
asks too, so a lost first refresh recovers. Because You Watched anchors are the
profile's three most recent completed titles that still exist in the catalog,
and a read asks for a refresh only when there are anchors and none has a
cached row.

List reads return at most 50 items per row (default 20); the v1 for-you and
similar-users reads keep 20, and section "see all" reads return up to 60, a
whole cached row. Home and library sections read the whole cached pool,
scope it to the section's libraries, then trim to the section's size. A
per-section items request that fails to load answers `500`; the aggregate
sections endpoints still degrade to empty rows.
