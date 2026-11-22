# Scanner ignore files

Media folders honor three file-based ignore mechanisms during scans. Series,
movie, audiobook, podcast, ebook, and manga walkers apply these rules to the
paths they discover. Existing library layouts still apply: podcast episodes
must be directly inside a show folder, and audiobook discovery follows the
[audiobook folder layout](audiobook-folder-layout.md). A configured library
root inside another root is walked as its own root. A subtree or audiobook
file scan applies these rules from its scan folder down, after widening the
scope as that layout describes. Widening stops below the deepest configured
root that holds the scope, but the widened scan still inherits the rules of
every ignore file between the outermost configured root that holds it and its
scan folder, as a library scan of that outer root would. Audio the ignore
files exclude does not count when widening, and a scope the library walk only
reaches through a symlink back to a folder above it is not widened. An
admin's request for an excluded file admits that file and nothing else the
rules exclude (see below).

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
  skipped. A `.ignore` that lists `downloads` skips that folder and nothing
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

## Hidden and housekeeping directories

Without any ignore file, every walker skips a directory below a configured
library root whose name starts with `.`, as Jellyfin does, and the NAS and OS
housekeeping folders `@eaDir`, `@Recycle`, `#recycle`, and `$RECYCLE.BIN`
(case-insensitive). The rule is `scanner.SkippedDirName`, which the real-time
monitor shares. A skipped directory is never read, so one the scan cannot open
is not a walk failure. Hidden files are not skipped by this rule.

A configured library root is walked whatever its name, unless it lies inside
another configured root of the same library (see Scan behavior). A subtree scan whose
scope is a skipped directory, or lies below one, reads nothing.

## Scan behavior

- Ignored content simply never appears in walk results. Missing-file
  reconciliation then retires anything previously cataloged there, which is
  the intended outcome of adding an ignore file to an already-scanned folder.
  Existing empty-scan confirmation and file-removal grace policies still
  apply. An ignore marker does not authorize cleanup of an empty library.
- Ignored entries do not count as walk failures. They never suppress
  missing-file reconciliation the way unreadable paths do.
- An `.ignore` or `.siloignore` that a directory listing shows but that cannot
  be read (permissions, or an I/O error on a network mount) makes its
  directory a walk failure: nothing under it is imported, and what is already
  cataloged there is protected from missing-file reconciliation, as for an
  unreadable directory. Treating it as absent would import what it excludes,
  then retire all of it once the file reads again. A file deleted between the
  listing and the read is absent. A subtree scan whose configured library root
  has an unreadable ignore file fails. Real-time monitoring keeps watching such
  a directory; the scans its changes trigger record the failure.
- A single-file scan (`ScanFile`) of a video, ebook, or manga file reads no
  ignore files. An admin's scan request for a file's exact path scans it even
  if a pattern would exclude it.
- An audiobook file scan rebuilds the books under the file's folder, widened
  as the [audiobook folder layout](audiobook-folder-layout.md) describes
  (every book in the library, for a file directly in the library root), and
  applies the ignore files to them from the configured library root down, as
  a subtree scan does. An admin's request for the exact path of a file the
  ignore files exclude admits that file past them, together with the folders
  on its path, so it joins its book; every other file and folder the rules
  exclude stays out. Below an ignore file that cannot be read the rules are
  unknown, so that rebuild reads no ignore files and scans every book under
  its folder (in the whole library, for a file directly in the library root)
  until the next library scan retires the excluded ones again. Both
  exceptions follow the scan run's trigger, not the path, so a scan Silo
  starts on its own always applies the rules, even when they change while the
  scan waits in the queue. A subtree scan never bypasses them.
- A file change from real-time monitoring or autoscan (an *arr webhook, a
  plugin source, or the Jellyfin `/Library/Media/Updated` endpoint) that
  would become a single-file scan is checked against the ignore files from
  its configured library root down when it is resolved, and dropped when they
  exclude it, so automation does not import that file only for the next
  library scan to retire it again. If an ignore file on the way cannot be
  read, the change is scanned anyway rather than dropped. An audiobook rebuild
  then meets the same unreadable file and records it as a walk failure, as a
  library scan does; a later scan imports the book once the file reads. A
  file change that becomes a subtree scan needs no check; the subtree scan
  applies the rules.
- Subtree folder scans inherit markers and patterns from their configured
  library root through the subtree's ancestors. Rules outside the configured
  root do not apply. If an ancestor directory cannot be read, the subtree is
  protected from missing-file reconciliation.
- When one of a library's configured roots lies inside another, the outer
  root is the boundary for media files. A full scan walks the inner root as an
  ordinary folder of the outer one, so the outer root's ignore files, and the
  hidden-folder rule for the inner root's own name, apply below it. Subtree
  scans and the full scans of every library kind apply the same rules there.
  Local theme music discovery for movies and series uses the same boundary, so
  it finds no theme the outer root's rules exclude or that sits in a skipped
  folder below it.
- A subtree scan whose configured library root is itself skipped (by
  `.nomedia` or a pattern-less `.ignore`) does nothing. Such a scan would find
  no files and retire the subtree outside the empty-root guard, so only a full
  library scan decides what happens to a skipped root's catalog. The same goes
  for a nested root that the outer root's rules exclude.
