# Streaming write deadlines and writer-chain conformance

The main API `http.Server` sets `WriteTimeout: 120s`. Go's `WriteTimeout` is an
**absolute deadline from the start of each request**, not an idle timeout, so every
response still being written at T+120s is killed mid-body — including a perfectly
healthy multi-gigabyte media stream.

`internal/httpstream.RollingDeadlineWriter` replaces that contract for streaming
responses only. The per-response deadline is pushed forward as the body makes
progress, so the semantics change from "must complete within 120s" to **"must make
progress at least every `window` seconds"**. A response that keeps moving lives
indefinitely; a stalled one is still reaped inside the window.

The server-level 120s guard stays exactly as it is. Streaming handlers opt out
per-response; every JSON, image and other ordinary API route keeps it.

## The contract

- The deadline is set through `http.NewResponseController(w).SetWriteDeadline`. A
  per-request controller deadline overrides the server-level `WriteTimeout` for that
  response — the mechanism the stdlib provides for exactly this case.
- `window` defaults to 180s, overridable via `SILO_STREAM_WRITE_STALL_TIMEOUT`
  (integer seconds).
- If the transport does not support per-response write deadlines, the wrapper degrades
  to a plain pass-through and the server-level `WriteTimeout` stays in effect.
- A paused client that stops reading for longer than `window` has its connection
  reaped. That is intended: the client's reconnect ladder resumes at its byte cursor,
  and this is a strict improvement on the 120s that applied to *all* streams before.

### Bumps are throttled on `Write`, never on `ReadFrom`

`Write` bumps at most once per `bumpStep` (~15s) so a fast stream issues one
`SetWriteDeadline` per step rather than one per 32 KB chunk.

**That throttle must not be applied to `ReadFrom` slices.** A slice is already bounded
at `readFromChunk`, so throttling around one saves at most a syscall per 4 MiB and
costs correctness: a slice completing less than a step after the last bump would get no
refresh, and the next slice would start with as little as `window - step` remaining.
That raises the sustained rate a client must hold from the documented floor to roughly
203 kbit/s and reaps healthy slow clients. Every slice — including the first — gets a
full window.

### Slice size is a correctness constraint, not a tuning knob

The deadline is an *absolute* time, so a write attempted after it fails immediately.
Slice size divided by window is therefore a **hard floor on the sustained client rate**.

The original 64 MiB slice against a 180s window implied ~3 Mbit/s: any slower client had
its deadline expire part-way through a single slice and was reaped despite continuous
progress. The slice is `httpstream.ReadFromChunkDefault` — 4 MiB, a ~186 kbit/s floor.
Raising it re-introduces the reap.

The proxy's egress meter has the *same* shape of constraint for a different reason and
therefore a different value: it credits a rolling per-second ring only when a slice
completes, so `internal/proxy.meterChunk` is 256 KiB. At 4 MiB a 200–500 kbit/s viewer
takes 60–170s per slice and reads as zero egress for most samples of the 60s window,
which under-reports committed bandwidth and lets the planner over-admit.

## Writer-chain conformance

`RollingDeadlineWriter` is not the only `http.ResponseWriter` on a media route, and the
ones above it in the chain can silently defeat it. Two rules apply to **every** wrapper
mounted on a path that serves media:

- **Forward `ReadFrom`.** `io.Copy` discovers `io.ReaderFrom` by direct type assertion
  and never consults `Unwrap()`, so a single wrapper without `ReadFrom` disables
  sendfile for everything below it. Wrappers that count bytes must transfer in bounded
  slices and credit each one, or a large transfer lands in a single accounting bucket.
  Use `httpstream.ForwardReadFrom`, which is the one implementation of this tail —
  hand-rolling it is how sites drift apart.
- **Implement `Unwrap()`.** Without it, `http.ResponseController` dead-ends at that
  wrapper and `SetWriteDeadline` fails, degrading the rolling writer to a plain
  pass-through — the deadline is silently gone. Preserve `Hijacker` too wherever a
  wrapper could sit over an upgradable route (ABS socket.io, the playback control
  websocket).

### Forwarding `ReadFrom` is necessary but not sufficient

Go's kernel sendfile path unwraps **exactly one** `io.LimitedReader` before it looks for
the `*os.File`, and `http.ServeContent`'s `io.CopyN` already contributes that one. An
accounting layer that hands down a *freshly nested* limiter therefore forfeits sendfile
even though it forwards `ReadFrom` correctly.

`httpstream.CopyChunked` slices the caller's limiter over the same underlying reader
instead of nesting a new one. **If you change it, re-run the
`strace -f -e trace=sendfile` comparison over a mounted router.** A byte-exact body and
a correct `Range` status prove HTTP correctness, not sendfile.

### chi's compressor is bypassed, not repaired

chi's `middleware.Compress` cannot be fixed in place: `compressResponseWriter`
implements `Unwrap`/`Flush`/`Hijack`/`Push` but **not** `ReadFrom`, and its handler
wraps unconditionally — the encoder is chosen later, so even a non-compressible content
type still gets a sendfile-killing wrapper.

It is bypassed on exact bulk-media routes via `httpstream.CompressExcept`, matching only
the registered GET/HEAD methods with exact segment counts and exact casing. A *blanket*
bypass would be wrong: subtitle font bundles are JSON served under the same global
compressor, and bypassing them would drop `Content-Encoding`/`Vary` and change the wire
contract.

### Stream telemetry sits inside the same contract

`streamtelemetry.observedWriter` is inserted between an enrolled route handler's
`RollingDeadlineWriter` and the real response writer, on every enrolled route family. It
follows the same rules: bounded `ReadFrom` forwarding preserves sendfile, `Unwrap`
preserves deadline traversal, and `Flush`, `Hijack` and `Push` retain their
optional-interface behavior. The outer compressor still bypasses only exact bulk routes;
subtitle-font JSON remains compressible.

## How conformance is verified

Handler-level tests bypass exactly the middleware this concerns, so they cannot prove
any of the above. Conformance is verified by driving the **mounted routers over real
sockets** (`internal/api`, `internal/jellycompat`, `internal/audiobooks/abs`,
`internal/proxy`), covering GET/HEAD, single and multi-range, conditional responses,
`Accept-Encoding` present and absent, HTTP/2, the proxy→node hop, and the socket.io
upgrade.

The deadline behavior itself is pinned by `internal/httpstream/readfrom_deadline_test.go`,
which asserts that a slow-but-progressing stream survives a window far shorter than the
whole transfer, that an oversized slice is still reaped (so nobody restores a large slice
without noticing), and that neither the production bump throttle nor a long pause before
the first write shortens the window a slice runs against.
