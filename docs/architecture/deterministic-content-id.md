# Deterministic, Cross-Server `content_id`

**Status:** Implemented (branch `feat/deterministic-content-id`)
**Scope:** catalog / scanner / metadata
**Key code:** `internal/contentid`, `internal/metadata/service.go`,
`internal/catalog/history_source.go`,
`migrations/sql/20260612130000_deterministic_content_id.sql`,
`migrations/sql/20260614120000_content_id_online_reid.sql`

This is the dev-facing reference for *why* `content_id` is what it is and *how*
it works. It merges the original design spec, the implementation notes, and the
PR write-up into one place.

---

## 1. The problem

`content_id` is the anchor almost everything hangs off: artwork, metadata, watch
history, progress, favorites, ratings, collections, credits. Historically it was
a **Sonyflake** — a locally generated, time-ordered random number minted at scan
time.

That means the *same* movie matched on two servers (or re-imported into a fresh
instance) gets a **different** id each time. Consequence: two servers holding the
identical title share *none* of that state. Run more than one Silo, or try to
sync a profile's history between servers, and the same film shows up as a
stranger on the other side — no resume point, no watched flags, no ratings. There
was no stable identity to build any cross-server feature on.

We want: **the same logical title produces the same `content_id` on every server,
every time**, derived from a globally stable anchor (the provider IDs already in
the library), and **stable across file renames/re-encodes** so linked state
survives.

---

## 2. The decision

Make `content_id` a **deterministic, provider-derived, structured natural key**,
stored as **`text COLLATE "C"`**:

| Item | `content_id` |
| ---- | ------------ |
| Movie | `movie-tmdb-228064` (fallback `movie-imdb-tt2413338`) |
| Series | `series-tvdb-296762` |
| Season | `season-tvdb-296762-1` |
| Episode | `episode-tvdb-296762-1-5` |
| Unmatched / local | `local-<128-bit path hash, hex>` |

Why this shape:

- **Collision-free with no hashing.** Provider IDs are unique by construction, so
  this is a natural key, not a surrogate — there is no "how many bits" question
  and no birthday bound.
- **No column-type migration.** It stays `text`, so the wide soft-reference graph
  keeps working; only the *values* are remapped, not the type.
- **Debuggable.** A row's identity is legible in logs and queries, and the
  scanner's match step becomes a direct PK lookup ("do I already have
  `movie-tmdb-228064`?") instead of a join through `media_item_provider_ids`.
- **Stable** across renames/re-encodes — a movie keeps its tmdb id.

### Why `content_id`, not a file-level id

Two distinct identities at different granularities, not to be conflated:

- **`content_id`** — the *logical* item (movie/series/season/episode). One id,
  *many* files (1080p + 4K + cuts; a series owns thousands of episode files).
- **`media_files.id`** — one *physical* file; a per-server serial that only
  answers "which file on disk."

`content_id` is the high-value target (35 FKs + ~25 unconstrained soft references
point at it). Its stability requirement is the *opposite* of a file id's: a file
id *should* change on rename; a `content_id` must *not*, or every server would
drop a title's watch history on a routine rename. That's why it's anchored on
rename-invariant provider IDs, never a path or file hash. File-level dedup is a
separate, deferred concern.

---

## 3. How the id is derived

### Format (SchemeVersion 1)

```text
movie-<provider>-<id>                  movie-tmdb-228064
series-<provider>-<id>                 series-tvdb-296762
season-<provider>-<seriesId>-<n>       season-tvdb-296762-1
episode-<provider>-<seriesId>-<s>-<e>  episode-tvdb-296762-1-5
local-<hex112>                         local-<sha256(path)[:14]>
```

The component separator is `-`, an [RFC 3986](https://www.rfc-editor.org/rfc/rfc3986#section-2.3)
*unreserved* character. Every component is `[a-z0-9]+` (or `tt` + digits for
IMDb), so `-` is an unambiguous delimiter, and because it never needs
percent-encoding the id doubles as its own tidy URL path segment
(`/item/series-tvdb-296762`) — identical to what is stored in the database, so
there is no encode/decode boundary and an operator can grep the value straight
out of a URL or log. (`:` would be legal in a path too, but `encodeURIComponent`
escapes it to `%3A`; an unreserved separator sidesteps that entirely.)

- The leading entity-type token domain-separates namespaces, so a movie and an
  episode can never alias on a coincidental number.
- IDs are normalized: lowercase scheme tokens, whitespace trimmed, IMDb keeps its
  `tt` prefix verbatim.

### Frozen provider precedence

Each item anchors on **one** canonical provider, chosen by fixed precedence so
two servers that see the same tags pick the same anchor:

- **Movies:** `tmdb → imdb → tvdb`
- **Series / Season / Episode:** `tvdb → tmdb → imdb` (TVDB is episode-canonical)

Precedence **and** exact string format are frozen behind a code constant
(`contentid.SchemeVersion = 1`), *not* a DB column — any scheme change forces a
full remap that normalizes every row at once, so there is never a mixed-version
population. Staleness is recoverable by recomputing the expected id from the
provider columns and comparing.

### The load-bearing invariant: the show falls out of the episode id

Season/episode ids compose from the **series anchor + season/episode numbers**
(both present in filenames and universally available), *not* their own provider
episode IDs (often missing). Because `episode-<provider>-<seriesId>-<s>-<e>`
embeds the series anchor, the series id is a pure **string transform** of the
episode id — no catalog lookup:

```text
episode-tvdb-296762-1-5   →   series-tvdb-296762
movie-tmdb-228064         →   movie-tmdb-228064   (movies/series: unchanged)
```

This is relied on by the hot watch-history query (§5). The format must never
break it. (`contentid.SeriesIDFromContentID` is the canonical transform.)

### Where the anchor comes from

For tagged libraries the IDs are in the folder/file name
(`{imdb-tt2413338} {tmdb-228064}`), so both servers derive the same id **from the
path string at scan time, with no API call**. The denormalized
`media_items.{tmdb,imdb,tvdb}_id` columns and the `media_item_provider_ids` link
table are the other sources. `content_id` is *derived from* the link table, not a
replacement — keep the full provider-ID set indexed.

---

## 4. What was built

| Area | File(s) | Summary |
| ---- | ------- | ------- |
| Derivation core | `internal/contentid/contentid.go` | `ForMovie`/`ForSeries`/`ForSeason`/`ForEpisode`/`ForLocal`, `SeriesIDFromContentID`, `IsProviderAnchored`/`IsLocal`, `SchemeVersion`, frozen precedence, normalization. |
| Generation wiring | `internal/metadata/service.go` | `deriveLogicalContentID`/`deriveSeasonContentID`/`deriveEpisodeContentID` replace `idgen.NextID()` at every mint site (skeleton, provider-match, explicit/implicit seasons, episodes, scanner fallback). |
| Re-ID on match | `internal/metadata/canonicalize.go` | Promotes a `local-` placeholder to its deterministic id at first confirmed match. |
| Hot query | `internal/catalog/history_source.go` | Watch-history `display_id` resolves the show via the string transform for anchored episodes; skips the `episodes_pkey` probe. |
| Value-remap migration | `migrations/sql/20260612130000_deterministic_content_id.sql` | Collision-safe Sonyflake → structured remap across the whole reference graph + `COLLATE "C"`, with rollback. |
| Online re-ID migration | `migrations/sql/20260614120000_content_id_online_reid.sql` | Adds `silo_rename_content_id` + `ON UPDATE CASCADE` to the content-id FK family. |

### Generation: determinism at scan time

The scanner already parses provider tags from names, so `createOrFindSkeleton`
derives `movie-tmdb-…`/`series-tvdb-…` immediately — two servers seeing the same
tags mint the same id with no provider API call. This is the primary target
(Radarr/Sonarr/Plex-tagged libraries). Untagged new items get a path-derived
`local-` id keyed on the **file path** (not the folder), so distinct pre-merge
skeletons in a shared folder stay separate while being stable across rescans.

Same-server convergence is preserved by the existing dedup
(`findExistingByProviderIDs`) plus `UpsertTx`'s
`ON CONFLICT (content_id) DO UPDATE` backstop.

Non-movie/series items (audiobook/ebook/podcast) and anchorless items **stay on
Sonyflake** — there's no stable cross-server anchor to derive from, so changing
them gains nothing.

### Re-ID on match: untagged items converge later

A tagged file is born with the right id; an *untagged* file gets a `local-` id
and only learns its provider IDs later when the match worker confirms a result. A
single gate in `mergeAndPersist` (`canonicalizeLocalContentID`) promotes the
placeholder at first confirmed match:

```text
SCAN a new file                          ← unchanged, still no network call
      ▼
createOrFindSkeleton ──► tag?  YES ► movie-tmdb-123   NO ► local-<hash>
      ▼
(later) match worker → mergeAndPersist → canonicalizeLocalContentID
      │  Is the id still local- AND do we now have provider IDs?
      │     no  → do nothing
      │     yes → Does a row already sit at movie-tmdb-123?
      │              yes → MERGE this row into it (rebindItemToExistingItem)
      │              no  → RENAME local-<hash> → movie-tmdb-123
      ▼                      └─ DB auto-moves child rows (ON UPDATE CASCADE)
rest of mergeAndPersist runs with the corrected id
```

The guard is a single `IsLocal` prefix check, so tagged content and all refreshes
pay nothing; the promotion is self-healing (a partial run re-runs on the next
match). The rename primitive `silo_rename_content_id` reuses the migration's
catalog-driven column enumeration so the two stay in lockstep.
`ON UPDATE CASCADE` only fires when a `content_id` actually changes (essentially
never outside this promotion), so there is no steady-state cost.

### The migration

The column **type does not change** (`text` → `text COLLATE "C"`); the **values**
are remapped via add-then-swap, not in-place PK mutation. It dynamically
enumerates the reference graph (the three PKs, every FK child from
`pg_constraint`, and soft-reference columns by name — **65 columns** on the real
schema), drops/recreates FKs and triggers, and retains a `content_id_migration_map`
audit table for rollback.

**Collisions are never merged.** If two items derive to the same key, or a key is
already taken, those rows keep their Sonyflake id and are flagged `collision` in
the map — the migration can never violate a PK or silently drop a row. Collision
status **cascades** from a series to its seasons/episodes (otherwise a uniquely
numbered episode under a colliding series would get an `episode-…` key whose
embedded series anchor points at a non-existent row).

`COLLATE "C"` is applied to every referencing column in the same migration so
join keys match on both type and collation.

---

## 5. Why `COLLATE "C"` is mandatory

Provider-derived IDs are pure ASCII, but `content_id` inherits the DB default
`en_US.utf8` collation. The design **regresses without `COLLATE "C"`**:

- For **equality / hash joins** (the watch-history join), `en_US.utf8` is
  deterministic so PostgreSQL already compares raw bytes — collation is
  irrelevant.
- For **ordering** comparisons (B-tree descent on PK probes, `ORDER BY`, range
  scans), `en_US.utf8` invokes libc `strcoll` while `C` is a plain `memcmp`.
- Structured keys share long prefixes (`episode-tvdb-296762-…`), which makes
  `strcoll` *worse* than on 18-digit Sonyflake strings — so a structured key
  under `en_US.utf8` is *slower* than the status quo. Under `C` it's ~3–5×
  faster.

So `COLLATE "C"` is load-bearing, not a free upgrade.

### The hot query

The watch-history page query (`history_source.go`) resolves each history row's
episode key to its series, then inner-joins `media_items`, deduped over the
profile's *entire* history. Two facts:

- `DISTINCT ON` can't short-circuit on `LIMIT` — cost is **O(history size)** with
  PK probes per row, not O(page).
- The structured id lets us **delete the `LEFT JOIN episodes`** that existed only
  to fetch `series_id`: for anchored episode ids the show is a string transform.
  The join is null-poisoned for anchored ids (`= NULL` is an unsatisfiable b-tree
  scan key) so the planner never descends `episodes_pkey` for them. Legacy/`local-`
  ids still fall through the join.

The remaining `JOIN media_items` stays — it's for access/library filtering, not
the show id. Reducing full-history scans to O(page) for the heaviest profiles is
follow-up work (a per-`(profile, display_id)` summary table), not part of this
change.

---

## 6. Performance — measured at production scale

Measured on an isolated, prod-tuned PostgreSQL 18 instance (`shared_buffers=4GB`)
loaded with an **exact-cardinality copy of production**: 184k items, 1.93M
episodes, 775k watch-history rows / 1,622 profiles. "Before" reproduces
production today (Sonyflake, `en_US.utf8`); "after" applies the structured-key
remap + `COLLATE "C"`. **Every metric improves.**

**`content_id` index-probe cost** — 300k forced point lookups over 1.9M keys:

The last three columns are *why* the faster schemes were rejected: speed alone
isn't the goal — the scheme has to carry the properties the whole feature exists
for. ✓ = has the property, ✗ = lacks it.

| Key scheme | Index size | µs / probe | vs today | Cross-server deterministic | Zero-join show transform | Human-readable |
| ---------- | ---------- | ---------- | -------- | :------------------------: | :----------------------: | :------------: |
| Sonyflake text, `en_US.utf8` *(today)* | 74 MB | 5.05 | 1.00× | ✗ | ✗ | ✗ |
| 128-bit hash `bytea(16)` | 74 MB | 1.80 | 2.81× | ✓ | ✗ | ✗ |
| `bigint` surrogate | 41 MB | 1.23 | 4.11× | ✗ | ✗ | ✗ |
| **Structured + `COLLATE "C"` (this PR)** | 84 MB | **1.86** | **2.71×** | ✓ | ✓ | ✓ |

The structured key lands within 3% of a 128-bit hash and is the **only all-✓
row**. The two faster schemes each give up something load-bearing:

- **128-bit hash** is cross-server deterministic (it hashes the same natural
  key), but an opaque digest can't be string-transformed, so episode→series
  needs a catalog join (no zero-join hot path), and it isn't legible in logs,
  URLs, or `psql`.
- **`bigint` surrogate** is just the status quo's narrow integer: fast because
  it's 8 bytes, but per-server (not reproducible elsewhere), no embedded anchor
  to transform, and not meaningful to a human.

Index grows ~14% (84 vs 74 MB), immaterial.

**Real history-page query** — heaviest profile (25,384 rows):

| State | Latency | `episodes_pkey` probes | Speedup |
| ----- | ------- | ---------------------- | ------- |
| Before | 385 ms | 25,348 | 1.00× |
| **This PR** | **150 ms** | **2,775** | **2.57×** |

**Concurrency** (`pgbench`, 150 real profiles): at 100 concurrent users,
**311 ms / 321 tps → 181 ms / 551 tps** — 1.7× throughput, 42% lower tail
latency. Comparison cost is pure CPU, so cheaper `COLLATE "C"` comparisons raise
the throughput ceiling under load.

*Caveat: warmed (dataset fits in `shared_buffers`); the before/after **ratios**
are the robust result, not absolute tps.*

---

## 7. Known limitations / follow-ups

- **Untagged series children.** A series that accumulated season/episode rows
  *before* it matched keeps those children on Sonyflake until a follow-up
  `recomposeSeriesChildIDs` pass re-derives them. Rare at first match (children
  usually don't exist yet); not yet wired.
- **Cross-provider reconciliation.** A series tagged tmdb-only on one server and
  tvdb-only on another still diverges — both paths derive from the denormalized
  provider columns. Proper fix: consult `media_item_provider_ids` before walking
  the precedence list.
- **O(history) query shape.** The watch-history `DISTINCT ON` over full history is
  the real liability at concurrency; the string transform removes a probe but not
  the full scan. The O(page) summary table is the fix, deferred.
- **Migration at scale.** Runs in one transaction holding `AccessExclusive` locks
  for the COLLATE rewrite + remap — run off-peak; very large installs (10–50M
  rows) should use the batched procedure noted in the migration header.
- **`local-` uses the absolute path,** so a same-server remount re-IDs local
  items. Acceptable for the best-effort local namespace. The hash is 112 bits
  (`sha256(path)[:14]`) so a `local-` id packs losslessly into a compat UUID
  (see below).
- **Client-visible id churn.** `jellycompat` encodes `content_id` into the
  client-facing UUID, so item ids seen by Android/Apple/Jellyfin clients change at
  migration (caches/resume keyed on item id reset once). **silo-android** and
  **silo-apple** should be made aware; server-side state is fully remapped. The
  encoding is *reversible* — a structured or local `content_id` is bit-packed
  into the UUID (`contentid.Pack`/`Unpack`), not hashed into a lookup table — so
  a compat item id decodes by pure computation and is stable across server
  restarts and across instances. Only arbitrary names (genres, studios) and the
  rare `content_id` too large to pack still use the in-memory hash map.

---

## 8. Prior art

- **Plex:** logical identity is a provider-namespaced GUID (`plex://movie/…`,
  `com.plexapp.agents.imdb://tt…`) — exactly this shape. (Its per-server integer
  `ratingKey` is the non-portable counterexample we avoid.)
- **Jellyfin / Emby:** store namespaced provider IDs and match on them; item GUIDs
  are deterministic. Same principle — anchor identity on the provider, not a
  per-server sequence.

---

## Appendix — alternatives rejected

Retained so the decision isn't re-litigated:

- **Keep Sonyflake.** Non-deterministic per server — the entire problem.
- **Hash provider tuple → `uuid` (128-bit).** Collision resistance is unnecessary
  (input is already unique), the id becomes opaque, and it forces a `text → uuid`
  migration across 35 FKs + ~25 soft refs whose failure mode is a seq-scan cliff
  (miss one column → `uuid = text` per-row cast → index disabled). Pays a large
  migration cost for an ~8-byte width win that only matters at a 50M-row ceiling.
- **Hash provider tuple → `text` hex.** Opaque like the uuid, large like the
  natural key — no reason to pick it over the legible structured key.
- **Path or content hash.** A title spans many files at many paths (no single path
  to hash), and the hash changes on rename — the opposite of the stability
  requirement. Only appropriate for *file-level* identity.
- **Pack provider + numeric id into `bigint`.** Variable-width provider numbers
  and S/E packing make the encoding brittle; not worth it over a legible key.
- **File-level deterministic id (`media_files`).** Deferred, not rejected — lower
  value (no metadata/watch state hangs off it), with open problems (server-local
  mount anchors, same-file-in-two-libraries). A complement, out of scope here.
