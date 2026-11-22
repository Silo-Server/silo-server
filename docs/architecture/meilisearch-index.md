# Meilisearch catalog index

With `catalog.search.provider` set to `meilisearch`, catalog search ranks with a
Meilisearch index that `internal/catalog` builds from PostgreSQL.
`catalog_search_index_state` names the active index, its schema version, and
the fingerprint of the index settings it was last brought to. The sync task
(`sync_catalog_search_index`, at startup and every minute) maintains it under
a Postgres advisory lock, so one node at a time does. Other nodes' search
providers re-read that row and need nothing else.

## Rebuild or settings update

A change reaches an existing index one of two ways:

- **Rebuild.** The indexed document's shape or the embedder changed: a field of
  `catalogSearchDocument` was added, removed, or computed differently
  (`SearchMeilisearchSchemaVersion`), or the embedder name, vector dimensions,
  semantic search, binary quantization, or indexed media types changed (the
  hash in `catalogSearchMeilisearchSchemaVersion`). The sync task builds a new
  index from PostgreSQL and swaps the state row to it. Until then, searches use
  the old index for keyword search when only semantic settings changed, and
  PostgreSQL otherwise; `index.rebuild_required` in the admin search status is
  true.
- **Settings update in place.** Anything else in
  `catalogSearchMeilisearchIndexSettings`: searchable, filterable, and
  displayed attributes, ranking rules, typo tolerance, pagination. It needs no
  schema version bump. When the recorded fingerprint differs from the
  server's, or is NULL, the sync task sends those settings, without
  `embedders`, to the active index, waits for Meilisearch to finish the task,
  and records the fingerprint. A failure leaves the old fingerprint, so the
  next sync retries; pending catalog events still sync meanwhile. The status
  does not report this as `rebuild_required`.

A rebuild records the fingerprint along with the rest of the state, so it
never also needs an in-place update.

## People match whole words

Cast and crew names (the `people` attribute) match only when the search's
words are whole words of a name: "hanks" finds Tom Hanks, "han" does not, and
"heat" does not find everyone named Heather. Titles keep prefix matching
and typo tolerance; people have typo tolerance off
(`typoTolerance.disableOnAttributes`).

Meilisearch matches the last query word as a prefix in every attribute it
searches, and its `prefixSearch` setting is index-wide, with no per-attribute
or per-query form. So once the index has this server's settings, each keyword
search is two queries in one `/multi-search` request
(`meilisearchKeywordQueries`):

- the keyword query, the search as typed with `attributesToSearchOn` naming
  every searched attribute but `people` (a two-term search names only the
  title attributes, `studios`, `networks`, and `year`);
- the people query (`peopleSearchRequest`), the search's words as one quoted
  phrase, which Meilisearch matches whole-word with no prefix and no typos,
  on `people` only, with the same filter, limit, and matching strategy. The
  search's exclusions (`-word`, `-"phrase"`) follow the phrase so they still
  apply, and a year hint stays outside it and also searches `year`. A search
  with no word left to match sends no people query.

`combineMeilisearchKeywordHits` merges the two: it cuts both lists against the
better of their two first hits (see below), then puts the keyword query's
hits ahead of the people query's, as attribute ranking orders titles ahead of
names within one query. An item in both keeps its first place.
Because the combined order depends on both lists from their first hit, a
search that pages by offset ranks both from the first hit up to the end of
each batch, as a hybrid search does.

## Keyword hits kept

Every keyword query of a catalog search (keyword-only, or the keyword half of
a hybrid search, and the people query with it) asks for
`showRankingScoreDetails` and keeps a hit only while it misses no more query
words than the best hit, with no more typos, and with a proximity score of at
least `meilisearchKeywordProximityShare` of the best hit's
(`meilisearchKeywordMatchCount`). The best hit is the better of the keyword
and people queries' first hits on those three rules, so an exact name match
drops title matches with a typo and an exact title match drops scattered name
matches. Words compare as missed words because a quoted phrase need not count
as many words as the same words unquoted. Each list is cut at its first hit
that fails, which is correct only because the index's `rankingRules` begin
with `words`, `typo`, `proximity`; `TestMeilisearchRankingRulesLeadWithWordsTypoProximity`
fails if they stop doing so. A paged keyword search without a people query
compares later pages with the first page's first hit, and a cut page ends the
ranking.

## Episodes rank after movies and series

An episode often has the same title as a film or series ("Pressure",
"Apocalypse Now"), and the two tie on every other ranking rule. Popularity
does not separate them for items no rating source counts votes for, so
Meilisearch's last tiebreak, the order documents were indexed in, decides,
and the episodes, which far outnumber the film, can fill the first page.

Each document carries `type_rank` (`catalogSearchTypeRank`): 1 for an
episode, 0 for every other type. The `type_rank:asc` rule sits after
`attributeRank` and `sort` and before `wordPosition`, `exactness`, and
`popularity:desc`:

- Among hits that match in the same attribute with the same words, typos, and
  proximity, a movie or series ranks ahead of an episode, even one whose match
  is more exact: "pilot" lists the film Bush Pilot ahead of the episodes titled
  "Pilot".
- A title match still ranks ahead of a match on genres or keywords, because
  `attributeRank` comes first. So does an episode's `title` match ahead of a
  movie that matches only through `original_title`, `sort_title`, or
  `title_variants`.
- `words`, `typo`, and `proximity` still lead, so the cut above holds.

The ordering happens inside Meilisearch, before the hit limit, so it costs no
extra query. A custom rule does not change `_rankingScore`. Hybrid search
already searches media and episodes separately and merges them by that score,
so the rule does not change hybrid ranking.
`TestMeilisearchRankingRulesRankEpisodesAfterMediaInOneAttribute` fails if the
rule moves out of that position.

## Searches while settings are pending

Until the fingerprint is recorded, which happens only after Meilisearch has
applied the settings, searches keep using Meilisearch but do not depend on the
new settings: they omit `attributesToSearchOn`, which Meilisearch rejects when
it names an attribute the index does not search, and search every attribute
the index searches instead. They send no people query either, so people keep
prefix matching until then. Any other request the old settings reject, such as
a filter on an attribute that is not yet filterable, gets a 4xx answer; that
request falls back to PostgreSQL and does not trip the Meilisearch circuit.

Servers of two versions with different index settings sharing one index (a
rolling upgrade) each apply their own settings when they win the lock, until
only one version runs.
