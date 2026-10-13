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
