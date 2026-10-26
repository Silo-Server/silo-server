# Seasonal Picks

A `seasonal_themed` row shows titles for the holiday in season: curated public
MDBList lists, ranked by IMDb popularity, optionally topped up with the
library's closest matches by recommendation embedding. The code owners are
`internal/sections/recipes` (catalog, params, selection),
`internal/sections/seasonal_lists.go` (snapshots and refresh) and
`fetchSeasonalThemed` in `internal/sections/fetcher.go`.

## Themes and vibes

`recipes.SeasonalThemes` is the built-in catalog, in calendar order (the
editor's order). Each theme
has a date predicate (`recipes.SeasonalPredicates`) and one or more vibes. Most
themes declare a yearly `Start`–`End` window (MM-DD, wrapping across New
Year when End comes first) and the predicate is built from it; Easter computes
its window, and weekly themes (Saturday Morning, Family Movie Night) use a
weekday-and-hour rule. A row may replace any non-weekly theme's window with its
own in `theme_dates`; a moved Easter stays on those dates every year. A
vibe is a curated MDBList list with a name and an in-season row title. The
first vibe is the default. Theme and vibe keys are stored in section configs,
so they are never renamed or reused. `GET /api/v2/sections/seasonal-themes`
serves the catalog to the editor; hidden themes (`summer`, kept for old
configs) are left out.

Prefer small hand-curated lists. Large "set during the holiday" keyword lists
drift off theme (Christmas lists that include Mean Girls).

## Picking what a row shows

`recipes.ActiveSeasonalPick` decides, from the row config and the time:

1. The first custom holiday in season (`custom_holidays`, a yearly MM-DD
   window that may wrap across New Year, and an MDBList list URL).
2. Otherwise the first enabled built-in theme in `recipes.SeasonalThemeOrder`
   (short holidays, then month-long ones, weekly themes, summer last) that is in
   season (the row's `theme_dates[theme]` when set, else the predicate), with
   the vibe from `vibes[theme]` or the default.
3. Otherwise a legacy single-theme row (`theme`) when in season or pinned.

Nothing in season means an empty row, which the home API drops. The row title
in season is `theme_titles[theme]`, else the vibe's title (a custom holiday's
`title`, else its name).

## Lists

`seasonal_list_snapshots` keeps one row per canonical MDBList `/json` URL with
the list's external IDs (TMDB, IMDb, TVDB) in IMDb popularity order
(`?sort=imdbpopular&sortorder=asc`). It deliberately stores no `content_id`:
the read path resolves entries against the catalog each time, so titles added
to the library appear without a refresh and content-ID renames need no
handling here.

`SeasonalListRefresher` downloads the lists every enabled seasonal row (server
sections and profile overrides, found by `SectionDemandLister`) will show now
or within 14 days, skipping copies younger than 20 hours. The
`refresh_seasonal_lists` task runs at startup and every 6 hours; saving a
seasonal section also refreshes its lists in the background. Claims use a
lease like trending snapshots. A failed download keeps the last good entries.

## Ranking and the recommendation fill

The row shows the list's titles the viewer can see, in list order. Unless the
row sets `source: "lists"`, a row with fewer titles than its limit is filled
from `recommendations.Repo.ThemeNeighbors`: the average embedding of up to 40
of the list's most popular owned titles, nearest movies and series first,
keeping only titles no farther from that average than the median seed. The
cutoff is relative, so it holds whichever embedding model the server uses.
Fewer than three embedded seeds, or no embeddings at all, means no fill. The
fill is best effort: an error leaves the list titles.
