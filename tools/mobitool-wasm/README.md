# mobitool.wasm — Kindle→EPUB converter module

`mobitool.wasm` is [libmobi](https://github.com/bfabiszewski/libmobi)'s `mobitool`
compiled to `wasm32-wasi`. The server runs it **in-process** via
[`wazero`](https://github.com/tetratelabs/wazero) (pure Go) to convert
MOBI/AZW/AZW3 ebooks to EPUB on demand. The consuming package is
`internal/ebookconvert`, which `go:embed`s this artifact.

## Why this shape
- **No cgo, no external binary.** The `.wasm` is `go:embed`-ed; the Go build stays
  `CGO_ENABLED=0` and cross-compiles trivially.
- **Architecture-independent.** wasm32 bytecode runs identically on amd64/arm64
  servers through wazero. Build once (CI, amd64), run anywhere.
- **Sandboxed.** Untrusted ebook bytes are parsed inside the WASM sandbox with
  only a per-conversion scratch dir mounted — a libmobi memory bug on a malicious
  file cannot reach the host.
  - Sandbox scope: this is a **memory-safety boundary, not a hard filesystem
    jail**. The input is mounted read-only via a confined `fs.FS` (no `..`
    escape), but a writable wazero dir mount can still be traversed within the
    server user's permissions, so filesystem isolation relies on running the
    server as a constrained, non-root user (the container already does). Never
    preopen the library directory or the cache root — only per-conversion
    scratch dirs.

## How the server uses it
- The module is instantiated as a WASI **command module** per conversion
  (argv ≈ `mobitool -e -o <outdir> <input>`), compiled once at startup and
  reused. Both stdout **and** stderr are captured — mobitool reports many
  failures on stdout with exit 0.
- Conversion is **on-demand through the read endpoint** (never at scan/ingest),
  gated by an admin setting and a capability endpoint, with the result cached on
  disk. Cache keys combine strong source identity (file id + size + mtime +
  scanner checksum when available) with a fingerprint of this `.wasm`, so
  bumping the artifact transparently invalidates old conversions. Concurrent
  reads of the same book singleflight into one conversion.
- **Error taxonomy** (`internal/ebookconvert/errors.go`): *deterministic*
  verdicts — DRM-protected, conversion failed (corrupt/unconvertible) — are
  negatively cached so a known-bad file is not reconverted on every open;
  *transient* verdicts — timeout, oversize source, caller cancellation — are
  never cached and retry on the next read.
- **Constraints:** DRM-free sources only (DRM removal is out of scope, always);
  fixed-layout "Print Replica" books are not convertible (mobitool ignores
  `-e`); source and output sizes, wall-clock time, wasm memory, and conversion
  concurrency are all capped via `ebookconvert.Options`.

## Pinned versions

| Component         | Version / commit                           |
|-------------------|--------------------------------------------|
| wasi-sdk          | 25.0                                       |
| libmobi           | `906274205c11944b628da1c553b255acb1af7c55` |
| zlib              | 1.3.1 (compiled to wasm)                   |
| wasmtime (smoke)  | 27.0.0 (build-time smoke test only)        |

License: libmobi is **LGPL-3.0-or-later**; shipping the unmodified compiled
artifact + this build recipe satisfies the relink obligation. zlib is zlib-license.

## Build config notes (validated by spike, 2026-06-17 on linux/amd64)
- `--with-libxml2=no` → libmobi's **internal xmlwriter** provides OPF/EPUB output,
  so the `-e` (create EPUB) path works with no libxml2 dependency.
- libmobi is built **against a wasm-compiled zlib** (not `--with-zlib=no`). Forcing
  `--with-zlib=no` makes libmobi vendor its own miniz, which then collides with
  `mobitool`'s separate miniz (used for EPUB zip creation) — `wasm-ld` rejects the
  duplicate symbols. Real zlib for the library + the tool's miniz for zip = no clash.

## Rebuild + install the artifact
```sh
docker build --platform linux/amd64 -t mobitool-wasm tools/mobitool-wasm
id=$(docker create mobitool-wasm)
docker cp "$id:/mobitool.wasm"        internal/ebookconvert/mobitool.wasm
docker cp "$id:/mobitool.wasm.sha256" internal/ebookconvert/mobitool.wasm.sha256
docker rm "$id"
```
The build runs a **smoke conversion** of a bundled libmobi sample and fails if no
EPUB is produced. `mobitool -v` embeds `__DATE__`/`__TIME__`, so rebuilds are not
byte-for-byte reproducible — rely on the smoke test + recorded sha256, not a
bit-identical rebuild check.

## Runtime behavior characterized by the spike
- Converts MOBI6, KF8/AZW3 hybrids, HUFF/CDIC-compressed, and unicode samples to
  well-formed EPUB (`mimetype` first = `application/epub+zip`, `META-INF/container.xml`,
  `OEBPS/*`).
- **DRM:** `mobitool` prints `Document is encrypted` to **stdout** but still **exits 0**
  and writes an EPUB (garbage content when it lacks the key). So the converter must
  capture stdout and validate the output is a usable EPUB — exit code alone is not a
  reliable DRM/failure signal.
  - ⚠️ **Brittleness note:** DRM/print-replica detection matches the **English
    stdout strings** mobitool emits (`drmMarkers` / `printReplicaMarker` in
    `internal/ebookconvert/converter.go`). When bumping the pinned libmobi commit,
    re-check those messages still match — a wording change silently downgrades a
    DRM book to a generic "no EPUB produced" failure (it still falls back to the raw
    original safely, just without the explicit DRM signal).
