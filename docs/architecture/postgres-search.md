# PostgreSQL catalog search

The PostgreSQL provider in `internal/catalog` combines media items, provider
aliases, and episode catalog entries. Title words use normalized `simple`
vectors; overview fallback uses English stemming. Completed title words are
ANDed and the final word uses a prefix. Short inputs retain the exact-title or
leading-title admission rules in `search_query.go`.

## Documents and maintenance

`media_items` stores the title vector, overview vector, and normalized original
and sort titles. The title vector preserves the original A/A/B weights and
concatenation positions for title, original title, and sort title. Its source
fields have a synchronous BEFORE trigger, so a committed metadata edit and its
search document become visible together. Unchanged inputs reuse their stored
documents. A content-ID move also fills a missing document, so online re-ID
cannot move an unfilled row behind the backfill's key boundary.

Episode documents live in `episode_catalog_entries`, keyed by library and
episode. Combined episode title/overview edits refresh the entry once.
Overview-only edits update the overview vector directly. File and series facet
refreshes update an existing entry in place and reuse its text documents. An
upsert would fire the BEFORE INSERT trigger, which rebuilds both documents before
ON CONFLICT discards them, so only a new entry builds documents. A moved file
refreshes both entry identities; an attribute edit within one identity refreshes
it once. A refresh writes nothing when the entry already holds the refreshed
values and both documents, so a watched change the entry does not store (a
re-probed track, a series runtime the episode overrides) costs no row or index
write. Entries store no still image; reads join it from `episodes`, so still
edits do not refresh entries. Re-ID, membership, and parent changes must keep
the entry's documents and access fields current.

The stored media document migration adds nullable columns, installs maintenance
before backfill, commits batches of 1,000 rows, then builds GIN indexes
concurrently. Retrying skips populated documents and recovers invalid index
builds. It then drops the predecessor expression indexes, which no current
query reads. A pre-upgrade binary that still issues expression queries gets the
same results without them, only slower. A normalization change must update every
stored representation.

## Ranking, counts, and continuation

The relevance order is exact title, title position, year hint, phrase rank,
title prefix rank, overview rank, lower title, and content ID. Title position
is 2 when a title, original title, sort title, or alias starts with the query,
1 when it contains the query, and 0 otherwise. When a title variant contains
the query, title prefix rank is `1 / (1 + words)` of the shortest such variant,
so shorter titles rank first. Otherwise it is `ts_rank_cd` divided by document
length (normalization 2), as is phrase rank. Cover density adds a cover for
each repeat of a query word, so it ranked "From Star Wars to Star Wars" above
the films and an alias "The Godfather & the Lady" above "The Godfather 2". The
episode exact tier computes the same rank for its constant prefix score. A
media title also counts as exact when it is the query followed by the item's
own year, as providers title remakes and reboots ("Castle (2009)"); short
whole-title admission accepts that form too. Episode titles have no year of
their own and do not use this rule. Access filtering precedes results, and any
accessible title or alias match suppresses the entire overview family.
Overview matches must meet the existing rank floor. This gate applies to the
complete family, including on later cursor pages.

Ranks are `float8`. A cursor carries each key as text and seeks with it cast
to `numeric`; PostgreSQL compares that with a `real` rank as `float8`, so a
`real` key such as 2.4 never equals itself, and continuation skipped or
repeated rank ties. `postgresCursorConfig` names the meaning of the keys;
change it with any ranking change so older cursors are rejected
(`invalid_cursor` in `/api/v2`, `catalog_cursor_changed` in `/api/v1`).

Title and alias lookups produce an indexed union of candidate IDs before media
policy checks and scoring. Media library checks remain correlated with those
identities, avoiding a scan of the entire allowed library for a rare match.
For searches with library restrictions or definition rules, episode title/library
predicates run inside an `OFFSET 0` planning boundary,
so a selective query can narrow entries through GIN before parent admission.
A LEFT JOIN checks the parent ID and series type inside a second boundary;
the outer policy requires its projected parent ID to be nonnull. The unique
parent key makes this equivalent to the original inner join/type guard while
preserving the candidate cardinality for the parent join. PostgreSQL can choose
indexed parent probes or a hash join that spills when needed. Library, maturity,
and definition checks still precede ranking and counting. These boundaries
preserve every admitted candidate. Unrestricted searches use a flattenable inner
join so PostgreSQL can parallelize admission and DISTINCT sorting for broad matches.

Ranked search uses pgx `QueryExecModeCacheDescribe`: it caches parameter and
result descriptions and executes an unnamed statement. PostgreSQL plans each
execution for the bound term and access scope. Named prepared statements can
eventually reuse a generic plan despite large differences between rare and
broad terms. The execution mode applies to cursor and legacy pages, exact
probes, counts, and fuzzy queries without changing connection-wide settings.

GIN supplies matching rows; it cannot supply this relevance order. A broad
prefix can still require scoring and sorting many matches. Candidate caps
before ranking would lose valid results. One allowed library makes episode
entries unique by episode and avoids deduplicating the full relevance tuple;
broader scopes still deduplicate memberships.

An episode-only relevance page in one allowed library can use the exact-title
tier when it has no total, year/phrase hint, grouping, rules, or seek. Equal
normalized titles share the ranking scores in this case. The index orders the
remaining lower-title/ID ties. Its four-byte normalized-title hash keeps the
tuple width at the predecessor folder/year/sort-key/ID index's shape; normalized
equality is rechecked before LIMIT to handle hash collisions. The tier returns
only when it fills the requested probe, including the has-more row. Otherwise,
the general query runs in the same snapshot. Continuations check the complete
ranking tuple before using the indexed tie boundary. FTS admission uses the
constant normalized title vector; a leading short-title lookup instead runs as a
row predicate beside the equality recheck. Correlated parent and policy checks
keep the ordered index scan ahead of LIMIT; hydration looks up only the bounded
page. The tier reuses the general query's parameter numbering, so it must
reference every bound argument: PostgreSQL rejects an unreferenced parameter
with SQLSTATE 42P18.

Exact totals use unranked candidate identities with the same admission, access,
overview gate, and overview floor. Work grouping retains scored counts because
its source cap applies before grouping. Sparse first-page FTS probes carry their
complete results and cursor keys into fuzzy augmentation, avoiding another FTS
query.

Adjacent pages use `page.next_cursor` without `seek`. Distant virtualized windows
use the root `window_cursor` with `seek`, which may scan the ordered prefix and
requires another boundary query. The wire contract and live-data continuation
rules are documented in `docs/catalog-api.md`.

## Typo fallback

A first page with fewer than five FTS hits adds trigram typo candidates for
queries with a word of four or more characters. PostgreSQL matches the query's
words other than stopwords (the whole query when none has four characters)
against title and alias trigrams and returns up to 500 candidates by trigram
rank. With "the", "the godfathr" admitted a quarter of a 190,000-title catalog
and ran close to the three-second search deadline. Whole-query similarity
favors short titles, so a long title containing every word can rank in the
hundreds. The whole-title similarity floor below still compares the full query.
The server scores each candidate's title and up to eight aliases word by word:
the four most similar to the query and the four covering the most query words,
since similarity also ranks a long alias with every word below short aliases
with one. It allows 0 edits for words of up to two characters, 1 for three to
five, and 2 for longer words (Elasticsearch's fuzziness AUTO). A word may also
match two adjacent title words joined, at one extra edit for ranking only.
Stopwords ("the", "a", "of", and similar) take no edits and need not appear. A
candidate is kept only when it matches every other query word, as
Elasticsearch's AND operator and Meilisearch's `all` strategy require. When no
candidate matches every word and FTS found nothing, the candidates missing the
fewest words remain, as Typesense's token dropping does; a candidate matching
only stopwords never does. When FTS found hits, candidates also need
whole-title similarity of at least 0.45, and none is kept unless it matches
every word. At most 50 typo rows follow the FTS hits; the total is inexact when
candidates or kept rows were capped. The offset and cursor paths share this
scoring and admission.

## Card enrichment

Catalog and section badges select a file winner in SQL and decode only that
file's track metadata. Their historical tie orders differ and remain distinct.
SQL resolution and HDR ordering must match `internal/overlays`, including Go's
Unicode whitespace rules.

For a PostgreSQL progress store sharing the catalog pool and account, series
and season play targets select the validated anchor, newest visible resumable
episode, first unwatched episode, or first available episode in SQL. The store
supplies the visible-progress relation, so profile history hiding and the
second-precision `updated_at` used for resume ties stay defined in `pgstore`.
Availability, history hiding, library access, and quality limits apply to every
branch. First-episode selection compares indexed winners from each
scope, with regular seasons before specials. Completed-progress checks keep
their unique-key lookup when statistics lag a bulk import, avoiding repeated
scans of a materialized profile history. Other stores retain their own progress
reads, and leaf-only cards retain the existing query.

Database tests in `scripts/ci/db-pins.txt` enforce document and ordering parity,
cursor behavior, winner accuracy, and query/row/function-call budgets. Timing
comparisons need an isolated dataset with representative statistics and load;
unit timings do not establish production capacity.
