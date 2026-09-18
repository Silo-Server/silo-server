# Scanner ignore files

Media folders honor two file-based ignore mechanisms during scans. Both are
evaluated by every library-kind walker, so series, movie, audiobook, podcast,
ebook, and manga libraries behave the same way.

## Marker files: `.ignore` and `.nomedia`

A directory containing a plain regular file named `.ignore` or `.nomedia` is
skipped entirely, together with everything under it. The marker must be a
regular file — a directory or symlink named `.ignore` is ordinary content.
The marker means "this folder is not media", so it has no pattern semantics
and cannot be overridden by a deeper `.siloignore`.

## Pattern file: `.siloignore`

A `.siloignore` file holds one glob pattern per line and follows the same
principles as Plex's `.plexignore`:

- The file must be a regular file in the directory it applies to; symlinks
  and directories do not count.
- Patterns are matched against paths relative to the directory holding the
  file.
- The file's patterns apply to that directory and every descendant. Nested
  `.siloignore` files stack on top of the inherited ones; nothing un-inherits
  a parent rule.
- A pattern that matches a directory name prunes the whole subtree, matching
  directories included.
- Blank lines and lines starting with `#` are comments. Surrounding
  whitespace is trimmed.
- Globbing uses Go's `filepath.Match`: `*` matches within one path segment and
  does not cross `/`. A pattern therefore only matches files directly inside
  the directory unless it contains an explicit `/` path, e.g. `Season 2/*.mkv`.

## Scan behavior

- Ignored content simply never appears in walk results. Missing-file
  reconciliation then retires anything previously cataloged there, which is
  the intended outcome of adding an ignore file to an already-scanned folder.
- Ignored entries do not count as walk failures. They never suppress
  missing-file reconciliation the way unreadable paths do.
- An unreadable `.siloignore` is treated as absent; a broken ignore file never
  aborts a library walk.
- An explicit single-file scan (`ScanFile`) ignores ignore files: an
  explicitly requested file is scanned even if a pattern would exclude it.
  Ignore files apply to folder walks only.
