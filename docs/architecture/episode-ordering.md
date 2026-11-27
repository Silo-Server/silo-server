# Episode ordering

How a series' seasons and episode numbers are chosen, how stored episodes
follow a change of order, and how files find their episodes. The code is the
source of truth; the main pieces are `internal/metadata/series_structure.go`,
`episode_order.go`, `episode_order_custom.go` and `episode_renumber.go`.

## Who numbers a series

One provider numbers a series: its **structure provider**. A metadata refresh
fetches every remote provider's seasons and episodes and keeps them apart until
the structure is chosen (`mergeSeriesStructure`):

1. The preferred provider is the series' own choice
   (`series_episode_orders.provider`), else its library's
   `media_folders.episode_order_provider`, else the first remote provider of
   the library's Episodes chain. Every library holding the series shares its
   episode rows, so the library setting is read from the first of its enabled
   libraries, in library order, that sets one.
2. If the preferred provider has no episodes for the series, the next remote
   provider in chain order that has some numbers it. A provider that failed
   (not a 404) stops the search: falling past it would renumber the show for as
   long as it is down, so the refresh keeps the stored episodes.
3. Other providers only fill empty fields of the structure's episodes, through
   the **bridge** (`bridgeEpisodes`): a shared provider ID, then equal-size
   runs of consecutive same-day episodes, then the same numbers on the same
   day, then the gaps between matched neighbours. A bridged episode keeps the
   structure provider's season and number. Local NFO data merges by number.

The structure provider can serve one of its **episode orders**: TVDB season
types (aired, DVD, absolute, alternate) or TMDB episode groups, listed by the
plugin's `EpisodeOrders.ListEpisodeOrders` and requested with the `ordering`
field of `GetSeasons`/`GetEpisodes` (silo-plugin-sdk, capability metadata key
`episode_orders`). The order's numbers are then laid out:

- `seasons` keeps them;
- `absolute` numbers every regular episode 1…N in one season, specials apart;
- `custom` places each episode where an admin put it
  (`series_custom_seasons`, `series_custom_episode_slots`), keyed by
  `<provider>:<provider episode id>` so the arrangement survives renumbering.
  An episode the arrangement does not place goes after the last custom
  season's episodes. A custom season takes the title, description, artwork
  and air date of the order's season with the same number unless the admin
  wrote its own title or description.

## Stored episodes follow the order

A refresh never deletes episode rows and writes them by
`(series_id, season_number, episode_number)`, so a change of order would leave
the old numbering behind. Before the writes, `renumberSeriesEpisodes` makes the
stored rows match the new numbering in one transaction, under the series row
lock:

1. Each target claims the row with its structure provider's episode ID, else a
   row sharing another provider ID, else the unclaimed row at its numbers.
2. A row nobody claims that shares a provider ID with a target duplicates that
   target's row (an earlier numbering left it): its watch state and files merge
   into the claimed row and it is deleted.
3. Claimed rows whose numbers differ move. Content IDs are renamed with
   `silo_rename_content_ids` through temporary IDs, so swaps cannot collide,
   and every user-state table follows (watch progress, history, favorites,
   ratings). A row whose composed ID another row holds takes a fresh ID.
4. A scanner placeholder in a moving row's way merges into it; any other
   unclaimed row on a target slot is parked after its season's last episode.
5. Season rows the new order empties and does not list are deleted.

Content IDs stay `episode-<anchor>-<s>-<e>` with the shown numbers, so the
series is still a string transform of the episode ID
([deterministic-content-id.md](deterministic-content-id.md)). Jellyfin item IDs
of renumbered episodes change with them, so Jellyfin clients re-sync those
episodes once.

## Files find their episodes by their names

A file name such as `S02E05` is season 2, episode 5 of the order the files are
named in: one provider's default order, the structure provider's unless the
series sets `file_order`. Each episode records the numbers its file would carry
in `episodes.file_season_number`/`file_episode_number` (NULL when they are its
own numbers): the provider's default numbers when the series shows another of
its orders, or the bridged episode's numbers in the other provider's order.
Linking matches files on those numbers.

A renumber moves rows with their file links, which is right for files linked
by air date or title and wrong for number-named files. After the moves,
`relinkSeriesFiles` re-reads every number-named file's path and points it at
the episode its numbers name; a file whose numbers no episode has is unlinked,
unless it sits on a scanner placeholder. A placeholder whose files all moved
to one real episode merges into it.

A bare number (`Show - 045`) is the 45th regular episode of the provider's
default order once an admin chose the series' order; otherwise it is read only
within season 1, where per-season and absolute numbering agree.

## Services that address episodes by number

Watch sync scrobbles and intro marker lookups send season and episode numbers
next to provider IDs. A series shown in another order keeps each episode's
place in its provider's default order in `episodes.provider_season_number`,
`provider_episode_number` and `provider_numbering`, and those are sent.

## Applying a change

Saving a series' order (`PUT /api/v2/admin/items/{id}/episode-order` or
`/custom-seasons`) stores it with a revision and queues the series' metadata
refresh, which applies it as above; a library change queues a full library
refresh. A refresh is idempotent: it computes the target numbering and
converges, so a node that dies mid-refresh is fixed by the next one.
`POST …/episode-order/preview` runs the same fetch, structure and layout
without writing, and counts the files each file order would place.
