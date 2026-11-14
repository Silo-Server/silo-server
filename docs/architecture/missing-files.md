# Missing files and library membership

A library membership (`media_item_libraries` for items, `episode_libraries`
for episodes) exists while its item or episode has a present file in that
library. Catalog reads hide anything without one, so removing a membership is
how a title with no playable file leaves the library. That includes listings
that name no library, for an administrator too: catalog browse, search, Home
rows, the unmatched-items list, upcoming episodes, people, and collection
members. Counts follow the same rule: collection item counts, a person's
credit counts, and the admin dashboard totals count titles in a library, and
the dashboard's file and storage totals count present files only. Only a read
by ID still opens such an item (or a person credited only on it) for a viewer
whose library access is unrestricted. The scanner marks a vanished file
`missing_since`; membership reconciliation then removes the memberships left
without a present file, in the same pass.

The scanner's file removal grace (`scanner.file_removal_grace`, 24 hours by
default) is how long a removal stays reversible. A replacement file, or the
same file returning, within the grace restores the title as it was:

- **Item hold.** Reconciliation does not delete an orphaned movie, series, or
  audiobook while any of its files was marked missing within the grace
  (`LibraryItemRepository.WithRemovalGrace`). The item is hidden, but its
  collections, manual metadata, artwork, progress, and root claim survive, so a
  replacement file relinks to it without a new match. A reconciliation after
  the grace deletes it. A zero grace deletes at once. Ebook and manga items are
  deleted at once regardless: chapter and series listings read child tables
  (such as `manga_chapters`) that only an item delete clears. Audiobook
  listings (Audiobookshelf lists and the progress they hydrate, a book's
  related rows, work pages) also require a library membership, so a held
  audiobook stays out of its author and series lists. A monitored rename of a book folder can scan the old
  path first; the hold keeps the book until the new path's scan matches it by
  its author, narrator, year, duration, and title tags. An untagged book gets a
  new item.
- **Added date.** Deleting a membership holds its `first_seen_at` (and an
  episode's `first_seen_scan_run_id`) on the title's missing file rows in that
  library (`media_files.held_*`). Inserting the membership again, or moving
  one onto the title (metadata matching rebinds a replacement file's
  provisional item this way), takes the earlier of the held and new values;
  the held ones are cleared once the row is written. Triggers on both
  membership tables do this, so every write path is covered. They check
  again after the write, so an import that lands while the removal is still
  committing keeps the date too.

A held value lives as long as its missing rows stay in the trash. It is
dropped when the row is relinked to another title or library, and when the
item is deleted: a later item with the same content ID is a new arrival. Only
removals that leave a missing file behind are held, so relinking a present
file to another title does not carry the old date over.

The trash sweep (`FileRepository.DeleteMissingByFolder`) keeps the rows of an
item that exists without any membership, whatever their age. Reconciliation
runs first and deletes every orphan it may, so such an item is held or under
an unreachable root, and its next orphan check needs those rows to find it.

A scan never marks files missing where its walk failed: an entry it could not
read or resolve (permission or I/O error, dangling symlink) protects what is
cataloged at or under it, and an unreachable or suspect-empty library root
protects its whole tree. One case is an absence, not a failure: a scan scoped
to a folder below a library root (a monitor or webhook scan of a deleted movie
or series folder) whose folder no longer exists (`ENOENT`) walks as empty when
its parent is still a directory, so its titles leave the library as they would
on a full library scan (`scopeRootVanished`). A folder whose parent is gone too,
or that sits below a dangling symlink, stays protected, and the library root's
own probes still apply to that scan.

Arr webhooks report an upgrade as a delete followed by an import, in either
order and possibly in separate deliveries. Neither order changes the outcome;
the title is only hidden between the two.

Sonarr's `SeriesDelete` and Radarr's `MovieDelete` with `deletedFiles: true`
become a scoped scan of the series or movie folder. Arr sends them before it
deletes the folder, so the delivery waits in the webhook queue, rechecked
every 10 to 25 seconds for two minutes and about once a minute after that,
until the folder is gone (`ENOENT`) while its parent
is still a directory, and only then scans. A scan any earlier would find the
files and nothing would scan again. Each check is a bounded stat, so a stalled
mount keeps the delivery waiting. A folder not confirmed gone an hour after
the delivery arrived is scanned as it stands. Deleting the last folder under a
library root leaves that root suspect-empty, so its titles stay protected
like any other emptied root.

## Files without video

A movie, series, or mixed library picks its files by extension, so it also
walks files that hold no video stream: a soundtrack saved as `.mkv`, an
audio-only transport stream. Such a file is not cataloged
(`internal/scanner/no_video.go`). The playback planner has no route for a
movie row without video, and the probe repair checks would ask for a new probe
of it on every scan and every playback decision.

- **The verdict needs two successful probes.** The first lists no video stream
  (embedded cover art is not one). The second reads further, up to 120 seconds
  or 100 MB (`deepProbeLimits`), because MPEG transport and program streams
  and FLV announce a stream only when its first packet arrives. When the
  second probe finds video, the row is written from it. A probe that fails,
  times out, or is rejected decides nothing, and the file is cataloged as
  before. A later scan probes it again and judges it then, except after a
  rejection: a scan does not probe rejected bytes again until the file's
  size or modification time changes.
- **The verdict is stored by path** in `scanner_no_video_files`, with the
  file's size, modification time, and OSHash. It holds only while the file at
  the path still has that size and modification time, so replacing the file,
  or touching it, has it judged again. Rows are removed only with their
  library; a stale one costs a stat per scan.
- **Later scans drop the path before anything else** (`dropNoVideoPaths`): it
  takes no part in root or group inference and is neither hashed nor probed.
  The scan that first finds the file has already inferred from its path, and
  the next complete scan prunes what that left.
- **A row already written for the file is marked `missing_since`**, although
  the file is still on disk, and from there it is a removed file: membership
  reconciliation, the item hold, and the trash sweep above apply unchanged,
  and the scan counts it under `Missing` so the change is published. The
  verdict is written before the row is marked, so a scan cut short between
  the two leaves a present row that the next scan marks. Marking is fenced to
  the size and modification time the scan read, and a row already missing
  keeps its `missing_since`.
- **A failed probe does not bring the file back.** The path is dropped before
  any probe runs. A renamed file is not relinked to a row whose stored probe
  or verdict shows no video; it is probed at the new path. If that probe
  returns nothing and the library holds a verdict for the same OSHash, size,
  and modification time, the file waits for a later scan instead of being
  cataloged unprobed. OSHash samples the head and tail only, so a match never
  stands in for a successful probe.

Extras, podcast, audiobook, and ebook libraries are not judged. Nothing here
touches the file on disk.

## Renamed and moved files

A video file that appears at a new path takes over the row of the file it was
(`Scanner.relinkMovedFile`), so everything keyed to `media_files.id` follows
it: probe data, chapters and chapter images, markers and their edits, marker
thumbnails, subtitles and timing corrections, trickplay, analysis artifacts,
and users' downloads. Download preparations start over, because their recipe
fingerprint includes the source path. Marker thumbnail identities hash the
path, so the relink rewrites them for the new path in the same update, and
their trigger ignores a path change.

Whether a row is the file does not depend on what the path says about the
title. A row qualifies when it is in the same library, has no extra link, and
recorded the same size, OSHash, and modification time as the file, and its old
path is gone under a library root that answers and is not empty. A file whose
bytes are unchanged but whose modification time differs does not qualify. One
qualifying row is the file. When several qualify, the one whose old path
parses to the same title as the new path (content group, filename hints such
as season, episode, and air date, the episode range, and every provider ID tag
on the path) is the file, but only when exactly one does; otherwise the file
gets a new row, since picking between equal claims could hand one file's
state to another. A copy whose original is still in place, a file under an
offline or suspect-empty root, and a file whose bytes match no vanished row
also get a new row, and so does a file whose only matching rows describe a
file without video (see above). Each library root is probed once per lookup, same-title
rows are probed first, and probing stops once the pick is settled.

What the new path says about the title decides the row's links:

- **Same title.** The row keeps its links, as before. A row stored under an
  older group key scheme cannot be compared by key with the current scan's, so
  for it the path identity alone decides.
- **Matching would find the linked title.** Otherwise the row keeps its links
  exactly when matching the new path would link it to the title it is linked
  to, as far as the catalog can tell without asking a provider. The scanner
  follows matching's own order (`Scanner.matchKeepsLinkedTitle`): a confirmed
  claim on the new content group, then on the new content root; for a series,
  a title already linked to another file under the new observed root; then the
  provider IDs the scan stores on the file's group (the folder's tags win and
  the file's fill the rest, `naming.GroupIdentity`). With those, the first
  catalog item they name counts when it is matched and agrees on the first ID
  both carry in the type's anchor precedence (TMDB, IMDb, TVDB for a movie;
  TVDB, TMDB, IMDb for a series; `catalog.IdentityProviderIDsContradict`), and
  otherwise a matched item at the tags' anchored content ID. So `{tvdb-…}` or
  `{imdb-…}` tags on a movie matched by TMDB keep its links, and a series whose
  TVDB tag names another show is rematched even when its TMDB tag agrees. An
  operator override on the new path or group, a series file directly in the
  library root, a path with neither tags nor claims, and a different season,
  episode, air date, or episode range all mean a rematch. The movie or series
  must be unchanged. The stored group and identity columns move to the new
  key, as when a group key scheme changes in place, and detected markers and
  marker thumbnails stay.
- **Another title.** Any other change (a different title, year, or tag, a
  corrected air date, a new episode range) retitles the row
  (`FileRepository.RetitleMovedFile`) and queues it like a new file, so
  renaming to fix a wrong match still matches afresh. The row drops its
  content and episode links, takes its season and episode numbers and its
  edition and episode range from the new path alone (an import edition
  belonged to the old title), loses match suppression and its last match
  attempt, and drops its marker thumbnails. It also counts as first seen now
  (`created_at`, `first_seen_scan_run_id`), since episode memberships date
  from their files' first sighting and the new title must not inherit the old
  one's added date. The marker trigger drops markers not set by hand, as on
  any rematch; everything derived from the bytes stays.

  In the same statement a stand-in row is left at the old path: missing, with
  the old links, root, group, numbers, first sighting, `missing_since` (or
  now), and held first-seen dates, no OSHash, and `stand_in_for_file_id`
  naming the moved row. To the old title this is exactly a file going missing:
  it is hidden, held for the removal grace, keeps its added date if a rematch
  lands on it again, and is deleted after the grace if nothing does. A row
  that had no links leaves no stand-in.

  Offline downloads follow the title they were authorized for, not the
  bytes: a download whose title is not the row's new one moves to the
  stand-in (and a row with no links but such downloads leaves a stand-in for
  them). A download is then never served or prepared from a file that has
  become another title: the stand-in is missing, so serving an original, its
  subtitles, or its embedded tracks is refused as for any missing file, and
  the download is deleted with the stand-in after the grace, as it was with
  the missing row before rows were reused. Registration cannot slip a
  download past the move: every statement that writes a download's file (in
  `internal/downloads/registration_fence.go`) locks the file row
  `FOR SHARE` in that statement and writes only while the file is present
  and linked to the download's own content and episode, answering the
  missing-file not-found otherwise. A request that registers several
  downloads at once (a season, a series, or a managed batch) writes none of
  its new downloads when one is refused; a monitor sync skips the refused
  episode and registers it on a later sync, as it does an episode with no
  file. A managed request replaces entries that already exist before it
  inserts new ones, in separate statements, so a replacement made earlier in a
  refused request stays, as it does when the insert fails for any other
  reason. The retitle, and a reclaim of a stand-in, lock the row
  `FOR UPDATE` in a statement of their own first, before the statement that
  moves downloads, in one transaction. A registration that holds the row
  first commits before that statement takes its snapshot, so its download
  moves with the others; one that waits rereads the retitled row and writes
  nothing. A single statement would not do: under READ COMMITTED its snapshot
  predates its wait for the lock. The locks are database row locks, so this
  holds across API servers. A prepared (remux or transcode) copy is served,
  and prepared again, only for a download on the file row it was made from,
  so a parked download is not given the copy of the row it left; a missing
  row's own copies are served as before. Root and group claims are untouched:
  claims are kept by present files, so the old path's claims age out as they
  do when a file goes missing, and the new path is claimed by matching as for
  a new file.

  A match job that read the row before the retitle must not link it back to
  the old title. The queues cannot stop that job: the movie worker loads a
  claimed batch up front and can hold a snapshot for minutes, and the
  retitle's re-enqueue only sets `rerun_requested` on the leased queue row.
  So the matcher's per-file link writes apply only while the row still has
  the path the match read: `FileRepository.UpdateContentID` (every link write
  in `MetadataService.createOrFindSkeleton`, in
  `internal/metadata/service.go`) and `FileRepository.UpdateEpisodeLink` (the
  series episode linker) both take that path, and a write that finds the row
  at another path changes nothing and returns `scanner.ErrFileMoved`. For a
  skeleton the refusal is the job's error: the queue records it and, with
  `rerun_requested` set, runs the job again at once against the row as it is
  now, and a skeleton item the job had just created for the link is deleted
  again while nothing references it. When a step after a successful skeleton
  link fails, the undo (`FileRepository.UnlinkContentID`) is keyed on the
  link instead of the path: it clears the row while it still carries the
  skeleton, even if a same-title move changed its path, and leaves a link
  another writer put there. When the episode linker's write is refused it
  reloads the row: a retitled row has no series link and is left for its
  rematch, but a row moved within its title (`RelinkMovedFile`) keeps the
  series link, so the series queue queues nothing for it, and the linker
  retries once at the row's current path when that path names the same
  episode. The fence is part of the `UPDATE`, which waits on the retitle's
  row lock, so it holds between API servers too. Writes keyed by the row's
  current columns (`UpdateContentIDByObservedRootPath`,
  `BulkLinkEpisodesBySeries`, group relinks) need no fence: a retitled row
  no longer has the root, group, or content link they select by.
- **Back to a stand-in.** A file that appears at a stand-in's path is checked
  against the moved-file rows before anything probes it onto the stand-in.
  When it is the row the stand-in stands for, with the same bytes and its new
  path gone, the rename is undone (`FileRepository.ReclaimStandIn`): in one
  transaction the stand-in is deleted and the row takes the path back with the
  stand-in's links, numbers, edition and presentation (an import edition
  included), first sighting, and held dates, and the stand-in's downloads
  return to the row. If a match had linked the row to
  another title meanwhile, that title gets a stand-in at the path the row
  leaves. A match that changes the row during the reclaim fails its fence, and
  the reclaim reads the row again and retries once. If the stand-in is gone by
  then (the trash sweep took it as the file came back), the row takes the free
  path through the ordinary moved-file rules. A file that is certainly not the
  stand-in's row (other bytes or modification time, or a copy whose original
  is still in place) is written onto the stand-in like any file appearing at a
  missing row's path, and the stand-in stops being one. When the scan cannot
  tell (a database error, an old path under an unreachable or empty root, or a
  row that changes under the fence twice), it fails that file and leaves both
  rows for a later scan, since writing onto the stand-in would leave the row
  missing for good. `stand_in_for_file_id` is a plain column, not a
  foreign key: ids are never reused, so a stand-in whose row was deleted only
  declines the reclaim, since the moved-file lookup cannot return that row.

Every write is fenced on the row's path, bytes, and links, and `MarkMissing`
only marks a row still at the path its caller saw, so a scan holding the old
path cannot hide the moved row. A relink that loses the new path to another
scan's write leaves that row alone. OSHash samples the head and tail only, so
the byte comparison is a strong heuristic, not proof. The same rules apply to
full, subtree, and single-file scans (watcher and arr webhooks), because they
all reach the relink through the scanner's per-file step.
