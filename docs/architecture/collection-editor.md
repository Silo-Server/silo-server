# Collection editor

The web edits every collection on one page, for server (library) and personal collections alike:
`web/src/pages/CollectionEditorPage.tsx`. This page covers how that page saves, because the
collection's titles and its other fields reach the server by different routes with different
tokens. Every type uses the page: Manual, Smart and Synced list, to create and to edit.

## Routes

`/admin/collections/new`, `/admin/collections/:id/edit`, `/collections/new` and
`/collections/:id/edit` each sit under one pathless layout route per family, so the page stays
mounted from `/new` to `/:id/edit`. After Create the page replaces the URL with the new
collection's edit URL and keeps the same draft, with focus in the title search. The page reports
itself clean until the URL has moved, so the unsaved-changes guard never asks during that step.

Creating starts from one **New collection** button on each collections list, which opens the type
picker (`web/src/components/collections/NewCollectionPicker.tsx`) over the list as
`?dialog=new`. Its Manual, Smart and Synced list cards are links to `/new?type=…`, carrying the
list's selected library on the server list. There they also hand the editor the list's URL,
without the open dialog, so its Back lands on the same view. The Synced list card waits for the scope's
capabilities and is disabled when `import_sources` is empty. A `/new` URL with no `type` redirects
to the list with the picker open, so older links keep working.

A viewer who can't change the collection never sees a form: the page sends them to the
collection's own page with `?notice=read-only`, which shows who can change it. Personal ownership
fails closed, so the page waits for the acting profile before it decides.

## What saves when

- **Titles save as you change them.** Adding, removing and reordering a Manual collection's titles
  call the item routes at once (`PUT …/items/{item_id}`, `DELETE …/items/{item_id}`,
  `PUT …/items/order`). A removal can be undone for six seconds; Undo puts the title back at its
  old position.
- **Everything else waits for Save.** Name, description, libraries, a Smart collection's rules and
  Order, artwork and the switches in Where it shows are a draft (`CollectionDraft`, `web/src/lib/collections/scope.ts`). The save bar
  names the fields that are pending and says titles are already saved. Discard never touches
  titles.
- **Before the collection exists**, picked titles are staged in the draft. Create sends the POST,
  then one `PUT` per staged title in order. Titles that fail stay listed and marked, with Try
  again.
- **Artwork** saves after the collection: a file or link uploads, a staged removal sends `DELETE
  …/image`, and a new file in the same slot replaces the image without a DELETE. A slot whose
  upload fails after the collection saved stays staged and offers Retry.
- **Pin** (`featured`, "Pin to the start of its shelf") is not part of the draft. It is set in
  Arrange. A collection created in the editor is created unpinned, and an editor PATCH leaves
  `featured` out, so a stale editor can't undo a Pin set elsewhere.

## What Pin does

The catalog lists a library's collections pinned first (`ListByLibrary` orders by `featured`,
then position). That order reaches viewers in two places:

- **A shelf set to Your order**, and No heading, shows its pinned collections first, then the rest
  in their stored order. Arrange draws them in a band at the start of the shelf. A move never places
  a card above the band, and the order a move saves is the order Arrange shows. A shelf that sorts
  itself (by name, recently updated or most titles) ignores `featured` (`applyCollectionSort`).
- **The Server collections list** on every profile's Collections page is capped per library
  (`ListServerCollections`, `capServerCollections`), so pinned collections are the ones that lead
  it, whatever their shelf's order. This is why Pin stays available on a shelf that sorts itself,
  and why its help line names that list.

`featured` is a column on the collection, so a Pin set from one library's Arrange applies in every
library the collection is in, while its shelf and position stay per library. Pin's help line says so
when the collection is in more than one library.

Arrange sends a Pin as a PATCH of only `collection_type` and `featured`, with `If-Match` from a fresh
read of the collection.

## Tokens and merging

Guarded writes carry `If-Match`. Every title write changes the collection's revision, and so its
ETag, without the editor's draft changing. To keep Save working:

1. After each title write the editor reads the collection again (`syncWithServer` in
   `useCollectionDraft`, `web/src/hooks/queries/collectionScope.ts`) and keeps the fresh ETag.
2. It merges the fresh copy into the draft with a three-way merge against the copy the draft
   started from (`mergeDraft`, `web/src/lib/collections/draft.ts`). Per field: changed only on the
   server, the draft takes the server's value; changed only in the draft, the draft keeps it;
   changed to the same value, no conflict; changed differently, the draft keeps its value and the
   field is a conflict. The fresh copy becomes the new base. A conflict waiting for Keep mine or
   Use theirs stays a conflict on later reads for as long as the draft and the server still
   differ on that field.
3. A Save that still answers `412` reads and merges the same way. With no conflicting field it
   retries once with the new ETag and no prompt. With a conflict it stops and shows the banner
   "This collection changed since you opened it." with Keep mine and Use theirs.

A reorder uses the item-order ETag from `GET …/items/order`, read again after every title write,
not the collection's ETag.

Because each save merges against a fresh read, two editors on different nodes, or an editor and a
sync, never overwrite each other's fields silently: the later save either carries the other
change forward or stops at the conflict.

## Smart rules and the live preview

A Smart collection's Contents is Add row's rule sentence (`RuleBuilder`), with the libraries
inside it: a server collection needs at least one, and a personal one with none matches every
library the profile can see. Rules about the viewer (Watched and the like) are offered only on a
personal collection. A rule the builder can't show stays as a locked line and is saved unchanged
until someone removes it. The draft's libraries are the rules' `library_ids`; a library change
counts once, as Libraries.

The preview sends the whole draft `query_definition`, across every chosen library, to the scope's
preview route (`POST …/collections/preview`, 24 titles), 300 ms after the last edit. It shows the
unsaved rules, so the save bar says the preview already shows them. A rule set that matches nothing
still saves.

A Smart collection may carry a stored default sort in `sort_config` (`field` and `order`), which
wins over the rules' sort when viewers open it. The Order block names it with Clear. Changing the
sort or direction clears it too, so the new Order takes effect; clearing drops only `field` and
`order` and keeps any other `sort_config` setting. An untouched stored sort is sent back as it was.

## Creating a Synced list

`/new?type=synced` (with `&source=mdblist`, `tmdb_chart` or `tmdb_list` to pick the opening tab)
shows one source panel. Its tabs follow the scope's `import_sources`. The panel's state is the
draft's `synced` member (`web/src/lib/collections/synced.ts`): the list it will follow, the typed
links, max titles and the schedule.

- **Picks fill only untouched fields.** A ready-made pick (a template) or an MDBList search result
  fills Name, Description, max titles, schedule and the poster. The draft remembers what the last
  pick filled; a field that still holds that value (or is blank) takes the next pick's value, and
  a field changed by hand stays and is named under Name ("Kept your name"). A search result brings
  only its own name and description, never a template's.
- **Only creatable templates show.** A template appears when its source is one of the scope's
  `import_sources` and it needs no profile; Discover and Franchise templates come only with Starter
  packs. Templates that ask for a link are left out, because the panel has its own link fields.
- **Links.** A pasted MDBList link loses its `?query`, `#fragment` and trailing slash before it is
  sent; a `/json` ending is kept. The server normalizes and allowlists it again. A TMDB list link
  is sent as typed.
- **TMDB charts** offer only the choices `validateTMDB` accepts (`web/src/lib/collections/tmdbSources.ts`).
- **Libraries.** A list of only movies or only shows leaves out libraries of the other kind and
  unticks them.
- **Create** posts the scope's import route (`POST …/collections/import/{mdblist,tmdb,tmdb-list}`),
  which runs the first sync, then saves artwork as for other collections. A server list is
  imported with `featured: false`. A pick's poster is sent as `poster_url` unless the poster slot
  replaces or removes it. The personal import takes no Collections tab choice, so with the switch
  on the web reads the new list and sends a guarded `PATCH` with `include_in_server_collections`.
  The page then moves to the new list's edit URL.
- **Schedules.** A server list takes a cron schedule, labelled with the answering node's offset
  from `schedule_time_zone`, because cron runs in the node's local zone. A personal list takes a
  named schedule (Manual only, Daily, Weekly, Monthly); a template's cron maps to the nearest one,
  never more often than daily.

## Editing a Synced list

A saved Synced list keeps what it follows in the draft's `list` member (`ListDraft`,
`web/src/lib/collections/scope.ts`): the link, chart or TMDB collection ID, max titles and the
schedule. They save with the rest of the draft and merge three-way like other fields; what the
list follows counts once, as the list.

- **What can change depends on the source.** A server MDBList or TMDB list changes its link; a
  server TMDB chart changes its chart or moves to a TMDB list; a franchise list changes its TMDB
  collection ID. A profile changes its MDBList or TMDB list link; a chart it follows stays as it was
  made. Discover lists (from Starter packs) and legacy Trakt lists show their source read-only.
- **What a server save sends.** The PATCH carries every field and rebuilds the source whole
  (`source_url` and `source_config`), as the server stores it: a changed MDBList link loses its
  `?query`, `#fragment` and trailing slash, and a TMDB list link becomes its canonical
  `https://www.themoviedb.org/list/{id}`. A Discover list and a franchise list with no ID yet send
  no source unless max titles changed, and then send their stored config with the new limit; every
  Trakt list sends no source, so the server keeps the stored one. A new chart unticks libraries that
  can't hold its titles, as on create, because the server doesn't check library kinds on update. A Trakt list's
  libraries and max titles are locked, and a stopped Trakt schedule can't be turned on, because
  the server refuses those changes. A save never sends `featured`.
- **What a personal save sends.** Name, sharing, libraries, the Collections tab switch and Show
  only always go; description, order, the link (`source_url`), max titles (`max_items`, `0` for the
  whole list) and the schedule go only when they changed.
- **Schedules.** A personal list's schedule is shown by its cadence name (`sync_cadence`), never as
  cron. A cadence the names can't express reads "Custom schedule (current)" and is never sent back;
  picking another sends `sync_schedule`. The picker stays locked until the collections
  capabilities report `sync_schedule_editable`. A server list's cron is labelled with the answering
  node's offset, as when it was created.
- **Sync status.** The panel shows when the list last synced, when it syncs next, and how many of
  the list's titles a sync skipped because they are in none of its libraries. That count comes
  from a sync run (`items_unmatched`); the collection doesn't store it, so it is known only after
  Sync now on this page. A failed last sync shows its reason at the top, with Sync now on server
  lists.
- **Sync now** (header ⋯, server lists only) moves the collection's revision. When it finishes, the
  editor reads the collection again and merges (`syncWithServer`), so the next Save sends the
  current ETag. Personal editors have no Sync now; a profile syncs its lists from their cards on
  the Collections page.

## Title search

The title search uses the viewer-scoped catalog query (`POST /api/v2/catalog/query`), so it offers
only titles the viewer can see: titles from libraries the profile can't open and titles above its
rating ceiling are not returned. A server collection with one library searches that library; with
several, the web searches each (at most four requests at once) and merges the results by rank. A
personal collection searches every library the profile can see. The search is a convenience,
not the access check: the personal item route refuses a title the acting profile can't see, and a
server collection's members are filtered per viewer when they are read.

## Links into the row pages

Both row pages (admin Sections at `/admin/sections`, and `/settings/home-screen`) take link parameters next to
`?page=`, read by `web/src/lib/homeRows/rowLinks.ts` and followed by
`web/src/components/homeRows/useRowLinks.ts`. Each page reads them once and drops them from the
address, so a reload doesn't repeat them.

- `?add=collection:library:<id>` (both pages) or `collection:user:<id>` (Settings > Home Screen
  only) opens Add row at step 2 on that collection. It waits until the page's rows are loaded and
  it is known whether the page can change, then for the page's collection options, and opens only
  when the id is among them. A refresh that fails gets the options-failed toast even when the old
  options hold the id, since they may hold a collection hidden or deleted since. A hidden, deleted, unknown or unshared collection, or a page that
  can't change, gets a toast and nothing opens; options that fail to load get their own toast.
  Options read before a collection was made or deleted are
  refreshed first, and a match or a miss waits for that refresh: the collection scopes'
  `invalidate` marks the admin collection list, the personal list and the library tabs stale, and
  those are what the options read.
- `?edit=<rowId>` opens that row in Edit row; a row that is gone gets a toast.
- `?return=<path>` makes the dialog's back link "Back to *collection*" and, after Add row, goes to
  that path with a toast that offers to move the new row (`?edit=` on the same page). Both replace
  the row page's history entry. Settings > Home Screen queues its saves, so there it goes back only
  once the save lands and the row is on the page, even when that takes a later read because the
  one after the save failed; a failed save keeps the viewer on Home Screen with the save's error
  toast. The toast's position counts the page's rows when the add lands, not when the dialog
  opened. A link that arrives while the dialog is open starts a fresh dialog on its target. After Add row the navigation state carries `addedRow` (the
  `AddedRowState` type: new row id, surface, page and position) so
  the collection's page can show which row is new. The path is honoured only when it starts with
  `/admin/collections/` or `/collections/` and neither it nor its decoded form holds a backslash,
  `//`, a control character or a `.`/`..` segment; anything else is ignored, so the parameter
  can't send a viewer off the site.

## Where it shows: rows

A server collection's Where it shows panel lists the administrator Home and library page rows that
show it, from `listAdminCollectionSections`, and offers **Add as a row** (Home, then the library
pages of the collection's own libraries, then the other libraries). The same rows give the header
its "On Home and the _Kids_ page" (turned-off rows left out). Rows profiles add to their own Home
are not listed, because the route reads the administrator page layouts only. When the collections
capabilities don't report `section_references`, the list is left out and Add as a row stays.

- **Add as a row** goes to admin Sections with `?page=…&add=collection:library:<id>` and, from the
  editor, `?return=` set to the editor's own URL (see Links into the row pages). The List's Add to Home
  and Add to a library page send no `return`, because the List's address is not a collection page
  the allowlist accepts; Sections keeps its own after-add step. With unsaved changes the editor
  asks first: Save and continue saves and goes only once nothing is left unsaved (artwork that
  failed to upload stays in the editor with its error), Discard changes drops the draft (titles
  stay), Cancel stays. Discard changes is off when the collection is saved as hidden, since only
  saving would show it. Save and continue is off whenever Save is, including while a field
  changed in both places waits for Keep mine or Use theirs. A collection not created yet, or one hidden from its Collections tab,
  can't be added, because a row couldn't open its See all; the button says why.
- **Back from Sections**, the `addedRow` state names the new row, which is highlighted once the
  rows list includes it. The editor sends Sections the List view it was opened from as history
  state, and Sections hands that state back with `addedRow`, so the editor's Back still reaches
  that view.
- **Hiding** a collection rows show asks first, naming the pages the rows are on, as the List does
  with its `row_count`. While the rows list hasn't loaded (or failed), the List's `row_count` for
  the collection still triggers the question, without naming pages. The switch stays part of the
  draft until Save.
- **Deleting** a collection rows show lists them, each with Open row, and offers to delete the
  rows and then the collection. Before any row goes, the editor (and the List) reads the collection
  again: when its ETag no longer matches the one the delete would send, nothing is deleted (the
  collection's `DELETE` would answer `412` after its rows were gone) and the new copy is taken,
  so the next Delete sends the current token. Each row is read again for its ETag right before its `DELETE`; a
  row that is already gone, or that now shows another collection, is left alone (the list of rows
  can be stale, and the collection's own `DELETE` still refuses while any row uses it). The first
  row that fails stops the run before the collection is touched, and the dialog names the rows
  that still show it. When the rows list fails to load, the delete waits for Retry only if the
  List's `row_count` says rows show it; otherwise the plain confirm deletes and the server's
  `409` (problem type `conflict`) guards a row added since. On the List, that `409` is shown as "Rows
  still use it. Remove them first." without `section_references`, and with it the List falls back
  to the rows list for a collection its counts showed as unused. The editor shows the server's
  message and reads the rows again (only when the server reports rows).
- **Row places** ("Home · row 6 of 9") are each row's rank in its page's order
  (`getAdminSectionOrder`, read once per page alongside the rows), because stored positions skip
  numbers after a row is deleted.

## Where it shows: personal rows

A personal collection's Where it shows panel lists the rows on the viewer's own Home and on the
library page of every library the profile sees that show it, read from
`GET /api/v2/profile/sections/settings`, one read per page. Every page is read, not only those of
the libraries the collection matches, because Home Screen offers a personal collection on any page
and its libraries can change after a row is added. That is the read Settings > Home Screen makes, so
both share one cache and a row added there is listed on the way back. Pages are read only once the
panel comes near the screen, and at most four at once (the poster peeks' limit), and not before the
profile's hidden libraries are known. The header's "On my Home…" line comes from the same read, so
it appears only after the panel has been near the screen. A row's place ("My Home · row 4 of 8") is
its rank on its page, hidden rows counted; a row the profile hid reads "Hidden" and is left out of
the header's "On my Home". Rows other profiles make from a shared collection are not listed.

- **Add as a row** offers My Home and the matched library pages, and goes to Settings > Home Screen
  with `?page=…&add=collection:user:<id>&return=<editor path>`, asking first about unsaved changes
  as the server editor does. It is off until the collection is created. The editor never writes a
  profile's rows itself; only Home Screen's Add row does.
- **Elsewhere:** an own card's ⋯ on the Collections page has Add to my Home…. A collection page the
  viewer can't change (shared with them, or a server collection for a viewer who isn't acting as
  admin) shows Add to my Home, with the viewer's library pages in its ⋯. These links carry no
  `return`, because neither page is a path the allowlist accepts. Shared with me cards keep no
  buttons (#195 W7).
