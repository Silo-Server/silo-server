# Server-side Kindle→EPUB conversion (MOBI / AZW / AZW3) — design

**Goal:** Let the silo-server render Kindle-family ebooks (mobi, azw, azw3) in the Android in-app reader by converting them to EPUB **server-side, in-process, on demand**, served transparently through the existing read endpoint. No external converter binary, no cgo.

**Status:** Approach approved by the user: convert via **libmobi**, run **built-in** to the server as **WASM executed by `wazero`** (a pure-Go runtime). On-demand + cached, admin-gated, DRM-free only. This is the MOBI half of the "all 9 server formats in-app" goal; the CBR (client-side) half is a separate Android spec.

> **Linchpin PROVEN (build spike, 2026-06-17, linux/amd64).** libmobi + `mobitool` cross-compile to `wasm32-wasi` (wasi-sdk 25, libmobi `9062742`, zlib 1.3.1 built to wasm); `-e` (create EPUB) is present; the module converts real MOBI6 / KF8-hybrid / HUFF-CDIC / unicode samples to **well-formed EPUB** (`mimetype`=`application/epub+zip` first, `META-INF/container.xml`, `OEBPS/*`). Verified end-to-end under **wazero** (the prod runtime) with WASI preopen + argv + `_start` + exit handling. Reproducible build stage committed at `tools/mobitool-wasm/Dockerfile` (with a smoke-conversion gate). Build-config gotcha resolved: build libmobi against a **wasm-compiled zlib** — `--with-zlib=no` makes libmobi vendor miniz that collides with mobitool's own miniz at `wasm-ld`. **DRM finding:** `mobitool` prints `Document is encrypted` to **stdout** but still **exits 0** and writes a (garbage) EPUB — so detect failure by capturing stdout + validating output, never by exit code alone.

## Why this shape

- Pure-Go MOBI/KF8 *parsers* don't exist at usable quality (the Go MOBI packages only *write*). Calibre is the gold standard but ~300MB+ of Python/Qt — rejected as too heavy. **libmobi** (mature C library behind the `mobitool` CLI) handles MOBI6 + KF8/AZW3 + AZW, decompression, and EPUB generation.
- Rather than shell out to an external `mobitool` binary (a deploy moving part) or bind via cgo (forces `CGO_ENABLED=1`, a C toolchain, painful cross-compile, and an LGPLv3 static-link obligation), we compile **`mobitool` to a WASI `.wasm`** once and **run it in-process with `wazero`**.
- This keeps the main Go build **`CGO_ENABLED=0`** and pure; one **architecture-independent** `.wasm` runs on amd64/arm64 servers alike; the `.wasm` is `go:embed`-ed into the binary (self-contained); libmobi's LGPL is cleanly isolated as a separate embedded artifact; and — a real bonus — **untrusted ebook bytes are parsed inside the WASM sandbox**, so a memory-safety bug in libmobi on a malicious file cannot reach the host (only the mapped temp dir).

## Components

### 1. The WASM module (build-time artifact)
- Compile libmobi + its `mobitool` tool to `wasm32-wasi` using the **wasi-sdk** (clang). Output: `mobitool.wasm`.
- Build libmobi **without libxml2** (`--with-libxml2=no` / `--disable-xmlwriter` as available) so the module has no external native deps; mobitool's EPUB writer uses a bundled miniz, which is portable C and compiles to WASI cleanly. (Feasibility of the no-libxml2 build for the `-e` path is the #1 thing to verify in the build spike — see Risks.)
- Build it in a **dedicated Docker build stage** (or a committed `Makefile`/script under `tools/mobitool-wasm/`). **Commit the resulting `mobitool.wasm`** into the repo so the normal Go build does not need the wasi-sdk — it only `go:embed`s the prebuilt artifact. Rebuild the `.wasm` only when bumping libmobi; record the libmobi version + build flags next to it.

### 2. `internal/ebookconvert` (new package) — the in-process converter
```go
// Converter converts a Kindle-family ebook file to EPUB bytes, in-process via wazero.
type Converter interface {
    // Convert reads srcPath (mobi/azw/azw3) and returns the path to a converted .epub.
    // Returns ErrDRMProtected / ErrUnsupported / ErrConversionFailed as appropriate.
    Convert(ctx context.Context, srcPath string) (epubPath string, err error)
}
```
- `//go:embed mobitool.wasm` → compile the module **once** at startup with `wazero.NewRuntime` + `CompileModule` (the compiled module is cached and reused; only cheap instantiation happens per conversion).
- **wazero command-module specifics** (from Codex review): `wasi_snapshot_preview1.MustInstantiate` the host module once; `Runtime.CompileModule` once at startup; per conversion instantiate with `ModuleConfig.WithArgs(...)` (argv ≈ `mobitool -e -o <outdir> <input>`), `WithFSConfig`, `WithStdout`+`WithStderr` (capture **both** — `mobitool.c` prints many failures with `printf` to stdout, not stderr), and the default `_start`. Use `WithName("")` (or a unique name) for repeated instantiation. Non-zero exits surface as `*sys.ExitError`. Honor ctx timeout via `RuntimeConfig.WithCloseOnContextDone(true)`.
- **Filesystem sandboxing** (from Codex review): wazero's `WithDirMount`/`WithReadOnlyDirMount` still permit relative `../../` traversal *within host permissions*, so **never preopen the library directory or the cache root**. Per conversion: copy the source into a private scratch tree, mount input **read-only via an `fs.FS`** (`WithFSMount(os.DirFS(in), …)` — confined, no `..` escape) and a dedicated empty **writable** output dir (`WithDirMount`). **The WASM sandbox is a memory-safety boundary, not a hard filesystem jail** — a writable dir mount can still be traversed by a compromised guest within the server user's reach. Filesystem isolation therefore depends on a **deployment requirement: run silo-server as a constrained, non-root service user (the container already does), so the converter's blast radius is minimal.** This is documented here and in `internal/ebookconvert/converter.go`.
- Enforce a wall-clock **timeout** (ctx), a **max source size**, and a **concurrency semaphore** (N simultaneous conversions). WASM memory is bounded by wazero config. **Resource note:** the defaults are `DefaultMaxMemoryPages` = 1 GiB linear memory per conversion × `DefaultConcurrency` = 2, i.e. up to ~2 GiB of wasm memory under saturation (plus the 256 MiB source / 512 MiB output caps). Generous for a small server; tune `ebookconvert.Options` for tight deployments.
- Map mobitool's exit code / captured stdout+stderr to typed errors. **Deterministic** verdicts (cacheable): DRM-protected input → `ErrDRMProtected`; corrupt / unconvertible layout / oversize-output → `ErrConversionFailed`. **Transient** verdicts (never negatively cached, retried next read): the per-call timeout firing → `ErrConversionTimedOut`; source over the size cap → `ErrSourceTooLarge`; caller cancellation/deadline → the context error propagated verbatim.

### 3. Conversion cache (on-demand, keyed)
- Cache converted EPUBs on disk under a configured cache root, keyed by **strong source identity + converter identity** — `file_id + size + mtime_ns + content hash` (use the scanner checksum if one exists) **plus** the converter **module fingerprint** (`moduleVersion`: a sha256 of the embedded `mobitool.wasm`, which already pins the libmobi commit, build flags, and wasi-sdk used to produce it). (Plain `path|size|mtime` can false-hit when a file is replaced with preserved mtime/size — Codex.) A stale key misses and reconverts.
- **Singleflight** per key so concurrent reads of the same just-opened book convert once. The shared conversion runs on a **detached context** (decoupled from any one caller), so a single caller cancelling its request never aborts the work for the others.
- **Negative caching**: remember only **deterministic-bad** sources — DRM (`ErrDRMProtected`) and conversion failures (`ErrConversionFailed`) — so every reader open does not reconvert a known-bad file. Transient outcomes (timeout, cancellation, oversize source) are **not** cached so they retry.
- A cache **hit refreshes the entry's mtime**, so the mtime-ordered budget eviction acts as a real LRU; eviction never deletes the entry it is handing back or another conversion's in-flight temp.
- Bounded by an LRU / total-size budget with opportunistic eviction (mirrors the existing reader-file/image cache conventions in the codebase).

### 4. Wiring into the read path
- `serveEbookInline` (`internal/api/handlers/ebook_reader.go:602`) currently `os.Open`s `file.FilePath` and `http.ServeContent`s it with `ebookMimeType(...)`.
- Change: when the source format is **kindle-family AND conversion is enabled**, resolve the converted EPUB via the cache (convert-on-miss), then serve **that** file with `Content-Type: application/epub+zip`. All other formats are served exactly as today (raw, byte-range, unchanged).
- Byte-range/`http.ServeContent` semantics are preserved — we serve the cached EPUB `*os.File` the same way, so resume/range still works.
- **Response headers** (from Codex review): `Content-Type: application/epub+zip`, `Content-Disposition` with an `.epub` filename, a strong `ETag` derived from the conversion cache key, and — because the *same URL* can return raw or converted bytes depending on the setting/failure — `Cache-Control: private, max-age=0, must-revalidate` (or `no-store`) to prevent stale client/proxy caching of the wrong representation.
- **HEAD is cache-only.** A `HEAD /read` must never trigger a (possibly minute-long, ~1 GiB) conversion. It answers from the cache: a hit serves the real converted headers + `Content-Length`; a negatively-cached source serves the `failed` contract; a miss advertises the converted headers (`Content-Type`, the cache-key `ETag`, `X-Silo-Ebook-Conversion: converted`) **optimistically with no body and no conversion** — the subsequent `GET` produces the body and the authoritative DRM/failure verdict, which then populates the cache that later HEADs read. *(Client note: a HEAD miss intentionally omits `Content-Length`; coordinate with the Android/Apple clients if any relies on it.)*

### 5. Admin gate + client capability
- An **admin-toggleable setting** (`ebook.kindle_conversion_enabled`, default off) gates the whole feature, via the existing server settings mechanism (`internal/api/handlers/settings.go` / `admin.go` `HandleUpdateSetting`). The flag is read with a short TTL cache so the hot read path (and the capability endpoint clients poll) does not hit the DB per request, while toggles still apply within seconds without a restart. When off: kindle-family files serve raw as today and the capability is not advertised.
- **Two gates to advertise the capability** (from Codex review): advertise conversion availability to clients **only when** (a) the admin flag is on **and** (b) the embedded WASM module compiled + instantiated successfully at startup. The Android client flips mobi/azw/azw3 from `ExternalOnly`→`InApp` **only when the flag is advertised**; otherwise it keeps today's external-open fallback. (Android-side change is small and tracked separately; this spec is server-side.)
- **Capability endpoint (implemented):** `GET /api/v1/ebooks/capability` → `{enabled, source_formats:["mobi","azw","azw3"], served_format:"epub", header:"X-Silo-Ebook-Conversion", header_failed_value:"failed"}`. `enabled` is true only when both gates hold (`EbookConversion.active`). The read response carries `X-Silo-Ebook-Conversion: converted|failed` so the client knows whether the body is EPUB or the raw original (→ external-open). ETag on the converted response is derived from the **exact conversion cache key** (`SourceKey.CacheKey()`) so a validator can never disagree with the cache; the failed/raw fallback is `Cache-Control: no-store` since the same URL flips representations with the flag.

## Data flow (read of a `.azw3`, conversion enabled)
1. `GET /api/v1/ebooks/{cid}/files/{fid}/read` → authorize → `file` is `azw3`, conversion enabled.
2. Cache lookup by source identity+version. Hit → serve cached `.epub`. Miss → singleflight: `Converter.Convert` (wazero runs `mobitool.wasm` on a temp copy) → store under key.
3. `http.ServeContent` the `.epub` with `application/epub+zip`, byte-range supported.
4. Client sees EPUB → existing reflow `EpubReader` renders it. Reader progress stays keyed to the **source** `fileId` (the EPUB is a derived view of the same file), so progress/annotations are unaffected.

## Error handling — explicit client failure contract
Transparent raw fallback is **not** safe on its own: once the server advertises conversion and the Android in-app EPUB path opens `/read`, returning raw MOBI/AZW bytes hands the EPUB reader a non-EPUB body (Codex). So failures need a contract the client can detect:
- On conversion failure (DRM, corrupt, timeout, oversize) the read endpoint returns a **structured signal** the Android client maps to its external-open fallback — preferred: a distinct status (e.g. `422 Unprocessable` / `415`) with an error code, **or** serve raw bytes **plus** an explicit `X-Silo-Ebook-Conversion: failed` (with the original MIME) header that the client checks. Pick one and make both sides honor it. Never silently hand raw bytes to the EPUB path.
- `ErrDRMProtected` vs `ErrConversionFailed` are distinguished in logs + (optionally) the error code, so DRM books get the "open externally" message and transient failures can be retried.
- Module compile/instantiate failure at startup (corrupt embed) → capability **not advertised** (gate b), kindle files serve raw as today, logged loudly.

## Testing
- `internal/ebookconvert` unit tests with **small DRM-free MOBI6, AZW, and KF8/AZW3 fixtures**: `Convert` produces a valid EPUB (zip with `mimetype` first + `META-INF/container.xml` + OPF + at least one content doc), deterministic across runs; corrupt input → `ErrConversionFailed`; oversize → rejected before invocation; timeout honored (ctx cancel).
- Cache tests: miss→convert→hit; key changes on source mtime/size and on module-version bump; singleflight collapses concurrent conversions.
- Handler tests: enabled+kindle → `application/epub+zip` body is a valid EPUB; disabled → raw bytes + original MIME; non-kindle → unchanged; DRM/failed → graceful fallback path.
- A build-stage check (CI) that the committed `mobitool.wasm` matches a rebuild from the pinned libmobi (or at least that it instantiates + converts a fixture), so the artifact can't silently rot.

## Sequencing — spike first (Codex-recommended)
**The first PR is only the build spike + a tiny Go harness that converts fixtures** — no handler/cache/wiring until the linchpin is proven. Codex confirmed libmobi documents both deps as optional (`--with-zlib=no` → bundled `miniz.c`; `--with-libxml2=no` → internal xmlwriter) and that `mobitool.c` gates `-e` behind `USE_XMLWRITER`, not `HAVE_LIBXML2` — so the no-libxml2 wasm build is *plausible*, but the spike must **assert** `mobitool -h` exposes `-e` and a fixture actually converts. Only then design the handler/cache/wiring PR.

## Risks / spike-first items
1. **libmobi+mobitool compiles to `wasm32-wasi` with the `-e` (EPUB) path working without libxml2.** The linchpin. If libxml2 turns out mandatory for `-e`, fall back to compiling libxml2 to wasm too (heavier) or assembling the EPUB in Go from libmobi's `MOBIRawml` parts.
2. **wazero WASI preopen file IO** for the file sizes involved (tens of MB) — confirm mapping + memory/time limits.
3. mobitool CLI surface as a wasm **command module** (argv + preopens) — confirm wazero runs it via `_start` and we capture exit status + stdout + stderr.

## Committed `mobitool.wasm` — guardrails (Codex)
Commit the artifact (so normal Go builds need no wasi-sdk) **with**: the pinned libmobi source commit, the Dockerfile/build script that produced it, its SHA256, the build flags, and the LGPL license notice. Add a **CI smoke test** that instantiates the embedded module and converts a fixture. Do not gate CI on byte-for-byte rebuild reproducibility — `mobitool -v` embeds `__DATE__`/`__TIME__`, so a rebuild won't match byte-for-byte unless normalized; rely on the fixture-conversion smoke + metadata hash instead.

## Process gate (Codex)
`docs/architecture/v1-scope.md` indicates v1 scope is not locked and new user-facing capabilities may need a proposal / project-tracking entry **before** a feature PR. Confirm this process step with the maintainer before opening the conversion PR. *(Decision deferred to the user.)*

## Out of scope
- CBR (separate Android client-side spec).
- DRM removal (never).
- Converting at scan/ingest time (on-demand only).
- Surfacing a separate "EPUB version" in the catalog (we serve transparently through `/read`).
- Reusing the WASM converter for other formats (later, if useful).

## Commit / deploy rules (project)
- silo-server changes go on a **feature branch, committed locally, PR-ready — NOT pushed** (per standing rule; server changes land via PR).
