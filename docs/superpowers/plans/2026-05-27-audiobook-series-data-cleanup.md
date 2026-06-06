# `audiobook_series` data cleanup

**Status:** Draft. Findings + options; no implementation yet.

## Problem

Migration `145_audiobook_series.up.sql` (which created the
`audiobook_series` table) shipped with a regex backfill that
over-matched. On this server's 220k-book audiobook library it produced
**162,322 distinct series names**, of which **141,591 (87%) are
singletons** — books that are the only "member" of their named series.

Spot checks make the failure mode obvious: rows like

- `series_name = "Computer Programming: This Book Includes: SQL, …"` for a standalone book whose subtitle happened to contain `"#2022 Version"`
- `series_name = "Reparación de crédito [Credit Repair]"` (the title verbatim)
- `series_name = "Cómo Atraer a las Mujeres … [How to Attract Women …]"` (the title verbatim)

The regex (`'^.+[^\s-]\s+\d+(?:\.\d+)?\s*-\s*.+$'`) treats any numeric
chapter / edition / part token followed by ` - ` as series-N-of-book
syntax. The real series (Star Wars, Warhammer 40k, Animorphs, Redwall,
*In Death*, etc.) live in the 733-row "large series" bucket; the
9,075 pairs and 10,923 small clusters mix real and false-positive.

The cap added in `b95494c` keeps the user-facing dropdown manageable
(top 1000 alphabetically), but the underlying data is still polluted —
sorting by series, the Series filter dropdown, and any future
"recommended" surfacing of series will all surface noise until the
backfill is repaired.

Commands assume the repository root is the cwd.

## Options

### A — delete singletons (destructive)

```sql
DELETE FROM audiobook_series
WHERE series_name IN (
  SELECT series_name FROM audiobook_series GROUP BY series_name HAVING COUNT(*) = 1
);
```

- **Pros:** simple, removes the bulk of the noise in one shot, no
  schema change.
- **Cons:** also deletes legitimate "first book of a not-yet-complete
  series" rows. Reversal requires a re-scan.

### B — filter singletons at query time, leave data alone

In `listDistinctAudiobookSeriesWithSource`, group by series name and
`HAVING COUNT(*) >= 2`.

- **Pros:** non-destructive; the table keeps full information for
  detail-page lookups and series-detail surfaces.
- **Cons:** hides legitimate one-book-only series from the filter
  dropdown. Series-detail and sort behaviour unchanged (still surface
  singletons). The 11.7 MB payload was already capped to 1000 in
  `b95494c`, so the size win from this option is small.

### C — mark backfill provenance, delete only backfilled singletons

Add a `source` column to `audiobook_series` (`'scanner' | 'backfill'`)
in a new migration. Migration 145's backfill rows get `source =
'backfill'`; scanner-written rows get `'scanner'`. Then delete only the
backfilled singletons.

- **Pros:** distinguishes legitimate from spurious without guessing.
- **Cons:** retroactive — we'd have to assume all current rows are
  backfill (which they mostly aren't anymore — the scanner has been
  writing too). Without a marker added at backfill time, can't tell
  what's what.

### D — wipe + re-scan (clean slate)

```sql
TRUNCATE audiobook_series;
```

Then trigger a full library re-scan so the scanner writes only the
real series_name values it extracts from tags.

- **Pros:** the cleanest end state; the scanner is authoritative.
- **Cons:** long re-scan window (the user already noticed 219k books
  is slow to scan); any books not currently scannable lose their
  series info entirely. Destructive.

## Recommendation

**Option B as a quick win** — non-destructive, ships as a one-line SQL
change in the facet helper, makes the Series filter dropdown useful
immediately. The dropdown stops showing 87% noise.

**Option D as the eventual fix** — once the scanner has been verified
to write series_name authoritatively (including the series_index from
real tags), `TRUNCATE audiobook_series` + re-scan is the right end
state. Need to confirm scanner behaviour before pulling the trigger.

Option A is too blunt (drops real first-books-of-series). Option C is
over-engineered without a marker added at backfill time.

## What this plan does not cover

- Whether the scanner currently writes `audiobook_series` rows
  authoritatively for every audiobook it touches, or only when tags
  contain explicit series metadata. Need to check
  `internal/scanner/audiobook.go` (or wherever the audiobook_series
  upsert lives) before proposing option D as the long-term path.
- Whether *Search Series* (the user-facing series-detail page, if
  any) would also benefit from the same cleanup.
- Sort-by-series on the library page — even with option B in place,
  the sort still groups by every series_name including singletons,
  so books in fake "series" appear under their fake series name.
  Acceptable for now; revisit alongside option D.

## Verification (once an option is picked)

- Re-run the data sanity query in this doc's intro and confirm the
  singleton count dropped where expected.
- Open the Series filter dropdown on the audiobook library page and
  confirm only multi-book series show up.
- `go test ./internal/catalog/...` + `cd web && pnpm vitest run` still
  pass.
