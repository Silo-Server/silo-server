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
(`meilisearchKeywordQueries`), plus, outside hybrid search, the title query
described under [Title matches rank first](#title-matches-rank-first):

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

## Title matches rank first

Meilisearch's last tiebreak is the order documents were indexed in, and the
index is built in content ID order. An episode titled "Pressure" ties with the
film Pressure on every ranking rule, so without help the dozens of episodes
with that title rank ahead of the film (`episode-` IDs sort before `movie-`).
The `popularity` rule does not separate them for items no rating source
counts votes for.

So each keyword-only search also sends a title query (`titleSearchRequest`):
the search as typed, with the keyword query's filter, limit and matching
strategy, on `title`, `original_title`, `sort_title` and `title_variants`
only, plus `year` when the search carries a year hint, as the people query
does. Its hits are the keyword hits whose title matches, and its ranking
details say how the title matches. A title hit that matches fewer of the
search's words than the keyword hit does (`last` and `frequency` drop words)
is not a title match.

`combineMeilisearchKeywordHits` then ranks the cut, combined hits by points
(`meilisearchSearchPoints`), highest first, keeping Meilisearch's order on a
tie:

- **Category points decide first.** A movie or series whose title matches
  scores `searchPointsMediaTitle` (as does any other non-episode type), an
  episode whose title matches scores
  `searchPointsEpisodeTitle`, and every other hit (matched by a person, a
  studio, a genre, a keyword) scores nothing. They are multiplied by
  `searchCategoryWeight`, which is larger than any match points, so no title
  match quality can lift an episode above a movie or series.
- **Match points order one category**, from the title query's `exactness`
  detail: the title is the search's words and nothing else
  (`searchPointsExact`), the title starts with them (`searchPointsPrefix`),
  or it holds them elsewhere (`searchPointsPartial`). Each corrected typo
  takes off `searchPointsPerTypo`. Meilisearch reports every match with a
  typo as `noExactMatch`.

The points rank the hits before the result limit applies, and
`keywordRanking` fetches the whole candidate window
(`meilisearchCandidateScanCap`) whatever page it serves, so a movie that
Meilisearch placed behind more episodes than one page holds still ranks
first, and an offset-paged search's later rounds do not reorder the pages it
already served. The ranking needs the `type` attribute on each hit, which
the keyword queries retrieve; it is already displayed, so the change needs no
index rebuild. While settings are pending no title query is sent and the
order is Meilisearch's own.

A hybrid search sends no title query and keeps Meilisearch's keyword order.
It searches media and episodes separately and merges their hits with the
meaning hits by ranking score (`mergeMeilisearchHybridHits`), so points
within one keyword search could not keep an episode below a movie across
the merge, and a promoted hit with a lower score would lower every score
after it (`meilisearchNonIncreasingScores`).

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
