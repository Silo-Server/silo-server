# Local sidecar artwork (NFO libraries)

Sidecar images next to a movie or series (`poster.jpg`, `folder.jpg`, `cover.jpg`,
`fanart.jpg`, `backdrop.jpg`, `background.jpg`, `logo.png`, `clearlogo.png`, and the
`<basename>-poster` / `<basename>-fanart` / `<basename>-logo` variants; extensions
`.jpg/.jpeg/.png/.webp`, 8 MiB cap) are discovered by the builtin NFO provider and
copied into the S3 image cache. Clients — including jellycompat — always receive the
normal presigned `poster_url`/`backdrop_url`/`logo_url`; library files are never
served directly, and API nodes never need filesystem access to libraries.

Key facts:

- Sources are recorded as `file://<absolute-logical-path>` in the `*_source_path`
  columns and cached by the metadata image-cache processor under
  `local/{contentType}/{contentID}/{hash8}/{imageType}/...`, where `hash8` is the
  file's content hash. Editing the file and refreshing rotates the key; the stale
  hashed prefix is deleted after a successful re-cache.
- The processor confines each read to the owning library's `media_folders` root
  paths with a lexical check on the logical path (symlinked roots stay valid).
- Missing or unreadable files (ENOENT/EPERM) and paths outside the library roots
  are stable failures with a long retry deferral; recovery is refresh-driven — the
  provider-artwork backfill sweep deliberately skips `file://` sources.
- Generic names (`poster.jpg`, ...) only attach when the directory holds a single
  content group; a shared `folder.jpg` in a flat multi-movie folder applies to none
  of them. `<basename>-poster.jpg` style names always attach to their file.

**Deployment constraint:** the host running the metadata image-cache processor must
mount the media libraries at the same paths as the scanner/metadata worker,
otherwise local artwork jobs fail until the mount is present.
