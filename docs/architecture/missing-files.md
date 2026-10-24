# Missing files and library membership

A library membership (`media_item_libraries` for items, `episode_libraries`
for episodes) exists while its item or episode has a present file in that
library. Catalog reads hide anything without one, so removing a membership is
how a title with no playable file leaves the library. The scanner marks a
vanished file `missing_since`; membership reconciliation then removes the
memberships left without a present file, in the same pass.

The scanner's file removal grace (`scanner.file_removal_grace`, 24 hours by
default) is how long a removal stays reversible. A replacement file, or the
same file returning, within the grace restores the title as it was:

- **Item hold.** Reconciliation does not delete an orphaned movie or series
  while any of its files was marked missing within the grace
  (`LibraryItemRepository.WithRemovalGrace`). The item is hidden, but its
  collections, manual metadata, artwork, and root claim survive, so a
  replacement file relinks to it without a new match. A reconciliation after
  the grace deletes it. A zero grace deletes at once. Book items are deleted
  at once regardless: chapter and series listings read child tables (such as
  `manga_chapters`) that only an item delete clears.
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

Arr webhooks report an upgrade as a delete followed by an import, in either
order and possibly in separate deliveries. Neither order changes the outcome;
the title is only hidden between the two.

A file arr deleted itself skips the grace once a scan has found it gone. The
webhook parser marks a change `Removed` for an import's `deletedFiles` (unless
the import reused that path) and for a file-delete event whose reason is
`Upgrade`, `Manual` or `ManualOverride`. `MissingFromDisk` means arr found the
file already gone, as a lost mount would look, so it keeps the grace. Autoscan
confirms a removed path only when nothing is at the path and its directory
still exists; `FileRepository.ConfirmFilesRemoved` sets
`removal_confirmed_at` and leaves `missing_since` to the scanner. The scan that
finds the file gone marks it missing and reconciles memberships as usual, and
the trash sweep then deletes the row whatever its age, except under a
protected root or while it holds first-seen data. A held row waits for the
replacement to claim the date, so in practice the shortcut applies to
upgrades; deleting a title's only file still takes the full grace. A scan
that finds a confirmed file on disk clears the confirmation.

Deleting a whole series or movie in arr with its files sends `SeriesDelete` or
`MovieDelete` with `deletedFiles: true`, which the parser turns into one
`Removed` subtree change for the folder. Autoscan confirms every cataloged file
beneath it with `FileRepository.ConfirmFilesRemovedUnder` once the folder's
parent directory exists. Arr sends these events while it is still removing the
folder, so the folder may still be on disk; the confirmations are dated a few
minutes ahead, and scans that still find the files in that window keep them.
A scan whose
walk then fails on that folder would normally protect it as a possible lost
mount; when every cataloged file beneath it is confirmed, the scan reconciles
it instead, so the title leaves the library at once. Its rows hold the added
date like any title's only file, so they still wait out the grace in the
trash. A folder that vanished without arr's confirmation keeps the
protection, and a delete that left the files on disk is a no-op.

## Renamed and moved files

A video file that appears at a new path takes over the row of the file it was
(`Scanner.relinkMovedFile`), so everything keyed to `media_files.id` follows
it: markers and their edits, subtitles and timing corrections, trickplay,
analysis artifacts, and users' downloads. Two things still key on the path and
start over: marker thumbnails (their trigger treats a new path as a new file)
and download preparations, whose recipe fingerprint includes the source path.
The row qualifies when it is in the same library with the same size, OSHash,
and modification time; the new path parses to the same title (content group,
filename hints such as season, episode, and air date, the episode range, and
every provider ID tag on the path); and its old path is gone under a library
root that answers and is not empty. Exactly one row may qualify. A copy whose
original is still in place, a rename to another title, and a file under an
offline or suspect-empty root get a new row as before, so renaming to fix a
wrong match still matches afresh. The update is fenced on the row's path,
bytes, and links, and `MarkMissing` only marks a row still at the path its
caller saw, so a scan holding the old path cannot hide the moved row. OSHash
samples the head and tail only, so the byte comparison is a strong heuristic,
not proof.
