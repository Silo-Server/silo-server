# Scanner ignore files

Media folders honor three file-based ignore mechanisms during scans. Series,
movie, audiobook, podcast, ebook, and manga walkers apply these rules to the
paths they discover. Existing library layouts still apply: podcast episodes
must be directly inside a show folder, and audiobook discovery stops below a
book folder containing audio.

Every ignore file must be a regular file in the directory it applies to. A
directory or symlink with one of these names is ordinary content.

## Marker file: `.nomedia`

A directory containing `.nomedia` is skipped entirely, together with
everything under it, whatever the file contains. The marker means "this
folder is not media", so it has no pattern semantics and cannot be
overridden by a deeper ignore file.

## Jellyfin-compatible file: `.ignore`

`.ignore` follows Jellyfin's rules, so libraries shared with Jellyfin behave
the same in both servers:

- An empty `.ignore`, or one without a valid pattern (only blank lines,
  comments, or malformed patterns), is a marker like `.nomedia`: the directory
  and everything under it are skipped.
- Otherwise each line is a gitignore pattern, and only matching entries are
  skipped. A `.ignore` that lists `.downloads` skips that folder and nothing
  else.

Patterns follow gitignore syntax:

- A pattern without a `/` matches an entry's name at any depth below the
  file's directory. A pattern with a leading or inner `/` is anchored to the
  file's directory.
- A trailing `/` matches directories only. `**` matches any number of
  directories, as in `**/sample.mkv` or `Extras/**`.
- Bracket expressions follow gitignore: `[!a]` and `[^a]` both negate, POSIX
  classes such as `[[:digit:]]` work, and a leading `]` or a leading or
  trailing `-` is literal.
- `!` re-includes a path an earlier pattern excluded. Nested `.ignore` files
  stack, and the last matching pattern wins, so a deeper file can re-include
  what a parent excluded. Nothing re-includes content inside an excluded
  directory, because the walk never enters it.
- Blank lines and lines starting with `#` are comments. Each line is trimmed,
  as in Jellyfin. Invalid patterns are dropped.

Silo matches anchored patterns against the path relative to the `.ignore`
file's directory, as git does. Jellyfin matches them against the absolute
path, so the two can disagree only for patterns containing a `/`.

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
  Existing empty-scan confirmation and file-removal grace policies still
  apply. An ignore marker does not authorize cleanup of an empty library.
- Ignored entries do not count as walk failures. They never suppress
  missing-file reconciliation the way unreadable paths do.
- An unreadable `.ignore` or `.siloignore` is treated as absent; a broken
  ignore file never aborts a library walk.
- An explicit single-file scan (`ScanFile`) ignores ignore files: an
  explicitly requested file is scanned even if a pattern would exclude it.
  For audiobooks, this rebuilds the containing book as a whole.
- Subtree folder scans inherit markers and patterns from their configured
  library root through the subtree's ancestors. Rules outside the configured
  root do not apply. If an ancestor directory cannot be read, the subtree is
  protected from missing-file reconciliation.
- A subtree scan whose configured library root is itself skipped (by
  `.nomedia` or a pattern-less `.ignore`) does nothing. Such a scan would find
  no files and retire the subtree outside the empty-root guard, so only a full
  library scan decides what happens to a skipped root's catalog.
