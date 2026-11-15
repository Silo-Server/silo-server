# Audiobook folder layout

Audiobooks are a beta library kind. This note records how the scanner decides
which audio files form one book and how it keeps a book's identity. The code
is in `internal/scanner/audiobook_scan.go` (`walkAudiobookDirectories`) and
`internal/scanner/audiobook_layout.go` (`audiobookBookParts`,
`audiobookScopeRoot`).

## Book roots

Every book has a root: a directory, or, for a loose file in a library root, the
file itself. The walk classifies each directory by what it holds:

- **A folder's own book.** A folder's own audio files and the audio directly in
  its disc subfolders are one book, whatever else the folder holds. A
  subfolder named like `Disc 1`, `Disk 02`, `CD1`, `CD 2`, `Part 3`, or `Pt 4`
  that holds audio is a disc. Parts follow their disc and track tags when the
  tags are complete, with a disc folder's number standing in for a missing disc
  tag; otherwise they are ordered by disc folder (the folder's own files
  first), then by natural filename order. Subfolders of a disc are still
  walked for books of their own. A disc-named folder with no audio of its own,
  `Disc 0`, one directly in a library root, or one that is itself a configured
  library root is walked like any other folder. Part collection reads only the
  disc folders the walk accepted, so a disc folder the ignore files exclude
  adds no parts.
- **Subfolders.** A folder's other subfolders are walked, and each that holds
  audio is a book of its own. A folder's own book does not depend on what its
  subfolders hold.
- **Extras.** Below a folder that holds a book, other than a library root,
  folders named `Bonus`, `Bonus Material`, `Extra`, `Extras`, `Sample`,
  `Samples`, `Supplement`, or `Supplements` are not walked. Their audio is
  neither a part of the book nor a book of its own.
- **Library roots.** A configured library root is never a book: its loose
  audio files are one book each, rooted at the file, and its disc- and
  extras-named folders are ordinary folders.

A configured library root must not be placed inside a book folder or one of
its disc folders. Adding one there after the book was indexed can leave the
book and the new root's loose files writing the same item.

Hidden folders and NAS housekeeping folders are skipped as everywhere else,
and [ignore files](scanner-ignore-files.md) apply to every folder and file the
walk reads, disc folders included. An admin's explicit scan of a file the
ignore files exclude admits that one file and the folders on its path, so it
joins its book; every other file and folder they exclude stays out. If an
ignore file on the way cannot be read, the rules are unknown and that scan
reads no ignore files, the documented full bypass.

### Known limits and follow-up

Several single-file books kept loose in one folder other than a library root
are indexed as one multi-part book. Give each its own folder, or keep them in
the library root. Telling such books apart belongs to their tags (parts of one
book share an album; separate books do not), not to the folders around them,
and is left to a later change.

## Identity

A book is keyed by its root's resolved path: `media_item_roots.canonical_root_path`
and `media_files.canonical_root_path`, with `media_files.observed_root_path`
holding the logical path the scan saw. A file root is keyed the same way, so
the unchanged-book skip, missing-file reconciliation, and alias handling treat
both kinds of root alike. Two logical paths that resolve to the same root
(symlinked folders or files) are one book.

When no file is yet stored under a book's root, the scanner reuses the item
whose first part is one of the book's files, preferring the earliest part.
Paths match exactly; a row whose path differs only in case is reused only when
both paths reach the same file or the row's path is gone (a case-only rename).
A library scanned under different grouping rules therefore converges without
two books claiming one item: separate disc items merge into the item of the
first disc, and an item made of a library root's loose files splits so the
first file keeps it. A folder's own book keeps its root, item, and parts.
Items left without files are removed by the usual membership reconciliation.

## Scoped scans

A file scan or subtree scan walks a folder W chosen so that every book the
scope touches is classified inside W exactly as a library scan classifies it
(`audiobookScopeRoot`). A file is scanned through its folder, whose book it is
part of. Let B be the deepest configured library root that holds the scope; it
is also the root of the walk that discovers those books. Every folder from the
scope's folder up to, but not including, B is checked: a disc or extras folder
belongs to, or is excluded by, the book above it, so W widens to its parent. No
other folder needs widening, because a folder's own book does not depend on its
subfolders.

W never crosses B. It is B only when the scope already is B, or is a file
directly in it; widening never moves W up to B or past it, so a scope that is
itself a configured root does not widen. Ancestors are read once, outermost first, with the
ignore files applied, and a scope the library walk only reaches through a
symlink back to a folder above it is not widened.

## Incomplete walks

A folder's own book is uncertain when something directly inside it could not
be read: an entry that would not resolve, or a disc folder not fully read. Such
a folder is held. The walk records it as a walk failure, which protects its
whole subtree from missing-file reconciliation and the trash sweep for that
scan, and adds no candidates from the folder or below it, so everything indexed
there stays as it is until the folder reads cleanly. A subfolder that cannot be
listed is a walk failure of its own and does not hold its parent. A library
root is never held. A dangling symlink that never resolves holds its folder
too, and the scan reports the partial walk until it is fixed or removed.
