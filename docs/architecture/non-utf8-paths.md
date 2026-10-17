# File names that are not UTF-8

A Linux file name is a byte string, and libraries filled by older tools can
hold names in a legacy 8-bit encoding, such as a Latin-1 `å` stored as the
single byte `0xe5`. PostgreSQL text columns reject such strings (SQLSTATE
22021), and `encoding/json` replaces each invalid byte with U+FFFD, which
loses the name. Silo carries these names through `internal/fspath`.

## Encoded paths

Every path Silo stores or passes around is in fspath encoding:

- Valid UTF-8 is unchanged. Paths that were already UTF-8, which includes all
  rows written before this encoding existed, are the same encoded or not.
- Each byte of an invalid UTF-8 sequence becomes one code point in
  U+10FE80–U+10FEFF (Supplementary Private Use Area-B). A code point from that
  range that is really in a name is escaped byte by byte too, so
  `Decode(Encode(raw)) == raw` for every name.
- The encoding works on single bytes and never produces `/`. Joins,
  `filepath.Dir`/`Base`, SQL prefix and range matches (`pathscope`), and
  `lower(file_path)` behave the same on encoded paths, and the encoded form
  survives JSON, protobuf, and Redis unchanged.

## Where encoding and decoding happen

- Names enter Silo encoded once, where they come off the file system:
  directory listings (`fspath.ReadDir`), symlink resolution
  (`fspath.EvalSymlinks`, `fspath.Readlink`), and library-monitor inotify
  events. Patterns read from `.siloignore` and `.ignore` files are encoded
  the same way, so a pattern holding a name's raw bytes matches it. Never
  encode a path that came from the database, an API request, or another
  Silo process: it is already encoded, and encoding it again escapes it
  twice.
- Every system call or child process that receives a media path decodes it.
  Use `fspath.Open`, `Stat`, `Lstat`, `ReadFile`, `ReadDir`, and
  `CommandContext` (which decodes every argument, including paths embedded in
  FFmpeg filter strings). A path handed over another way, such as inside an
  ffconcat list on stdin or to `zip.OpenReader`, needs an explicit `Decode`.
  `Decode` leaves strings without reserved code points unchanged, so these
  wrappers are safe on configured roots and generated paths too.
- `Stat`, `Lstat`, and `ReadDir` wrap the `FileInfo` of an encoded name, which
  `os.SameFile` does not recognize. Compare such values with `fspath.SameFile`.
- Transcode nodes receive the encoded path over JSON, look it up in the
  catalog as stored, and decode it only to open the file.

## Display text

`fspath.Display` turns an encoded name into text for people: each escaped
byte is read as Windows-1252, so `B\xe5rd` shows as `Bård`. Title parsing in
`internal/naming` reads names through `Display`, both when it classifies a
path (episode markers, editions) and in the titles it returns, so a name
parses the same whether it was stored as Latin-1 or UTF-8, and parsed titles,
group keys, and download file names never contain the reserved code points.
Paths in naming results keep the encoding.
