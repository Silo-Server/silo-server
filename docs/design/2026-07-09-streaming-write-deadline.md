# Streaming write-deadline fix: stop truncating streams at 120s without losing stalled-client protection

**Status:** planned (2026-07-09)
**Repos:** silo-server (primary), silo-apple (hardening follow-up)
**Root-cause session:** live debugging on Apple TV, 2026-07-09 evening

## Problem

`cmd/silo/main.go:2373` sets `WriteTimeout: 120 * time.Second` on the main API
`http.Server`. Go's `WriteTimeout` is an **absolute deadline from the start of each
request**, not an idle timeout — so every response still being written at T+120s is
killed mid-body with a clean connection close.

Observed impact (2026-07-09, dev server, Apple TV loopback client):

- Every direct-stream GET (`/api/v1/stream/{session_id}`) serving a 15.3 GB MKV was
  cut at exactly 120s. Server log signature: `path=/api/v1/stream/` with
  `duration_ms=120000` (three instances in a 3h window; more go unlogged when the
  client disconnects first).
- The Apple client's byte-cursor reconnect ladder silently absorbs most kills, so
  playback "mostly works" — but when a kill lands during backpressure parking or a
  demuxer resync, the client's 3-attempt `.prematureEOF` retry budget exhausts and it
  performs a full teardown + new-session recovery (~5 s visible stop). Four such
  failures in ~35 minutes of playback.
- Confirmed by dual-path probe (long ranged GET consumed at playback rate): died at
  ~123s direct to the origin and ~151s via the arkyncdn openresty edge (origin killed
  the edge's upstream GET at 120s; the edge's buffer drained for the extra ~30s).
  The CDN is exonerated — it only relayed the origin's truncation.

Precedent inside the same file: the Jellyfin-compat listener (`main.go:2514`) and the
Audiobookshelf-compat listener (`main.go:2535`) both already set `WriteTimeout: 0`
because compat clients stream long responses. The main listener serves the exact same
class of traffic for first-party clients and was never aligned.

## Goals / non-goals

Goals:

1. Streaming/long responses are never killed while they are making progress.
2. Genuinely stalled connections (client gone, TCP zombie, slow-loris) still get
   reaped in bounded time — we do NOT simply set `WriteTimeout: 0` on the main
   listener, because that removes mid-response stall protection entirely (a peer that
   stops ACKing would block a handler goroutine + fd indefinitely).
3. All non-streaming API routes keep today's 120s absolute protection unchanged.
4. No client changes required for the fix to take effect (Android, web, Infuse/compat
   benefit identically).

Non-goals: CDN edge tuning; silo-server#333 control-plane restart work; the
silo-apple `.prematureEOF` parking hardening (tracked as follow-up below, it is
defense-in-depth once this lands).

## Design: rolling per-write deadline for streaming handlers

Semantics change for streaming responses only, from "must complete within 120s" to
"must make progress at least every N seconds".

### New helper (one small file, e.g. `internal/api/httpstream/rolling_deadline.go`)

```go
// RollingDeadlineWriter wraps a streaming http.ResponseWriter and pushes the
// connection's write deadline forward on every successful write, so long
// responses survive indefinitely while stalled ones die within `window`.
type RollingDeadlineWriter struct {
    w      http.ResponseWriter
    rc     *http.ResponseController
    window time.Duration // stall budget, e.g. 180s
    step   time.Duration // min interval between deadline bumps, e.g. 15s
    last   time.Time
}

func New(w http.ResponseWriter, window time.Duration) *RollingDeadlineWriter
func (s *RollingDeadlineWriter) Write(p []byte) (int, error)  // bump-then-write
func (s *RollingDeadlineWriter) ReadFrom(r io.Reader) (int64, error) // see below
func (s *RollingDeadlineWriter) Header() http.Header
func (s *RollingDeadlineWriter) WriteHeader(code int)
func (s *RollingDeadlineWriter) Flush()
func (s *RollingDeadlineWriter) Unwrap() http.ResponseWriter // ResponseController compat
```

Key points:

- Uses `http.NewResponseController(w).SetWriteDeadline(time.Now().Add(window))` —
  Go ≥ 1.20 (we are on 1.26.4). A per-request controller deadline **overrides** the
  server-level `WriteTimeout` for that response, which is exactly the mechanism the
  stdlib provides for this case.
- Bumps are rate-limited (`step`, ~15s) so we do one `SetWriteDeadline` per ~15s of
  wall time, not one per 32 KB chunk.
- **`ReadFrom` must be implemented** and delegate to the underlying writer in bounded
  slices (e.g. `io.CopyN(underlying, r, 64<<20)` per iteration, bumping the deadline
  between iterations). Rationale: `http.ServeContent` → `io.Copy` uses the
  `io.ReaderFrom` fast path on the response writer (sendfile for `*os.File`); a naive
  wrapper without `ReadFrom` silently forfeits sendfile and burns CPU copying 15 GB
  through userspace. Bounded-slice delegation keeps sendfile *and* the rolling
  deadline.
- First bump happens in `New` (covers the header write + first body bytes).
- `window` default 180s, overridable via env `SILO_STREAM_WRITE_STALL_TIMEOUT`
  (seconds). 180s > the Apple client's longest observed benign backpressure park
  (~20s) and > outage-probe cadence with margin, while still reaping zombies in
  minutes. Note: a paused client that stops reading its socket for >window will have
  the connection reaped — that is fine and intended; the client's reconnect ladder
  resumes at the byte cursor on unpause (this is today's behavior for ALL streams at
  120s, so it is a strict improvement).

### Application points (all on the main listener)

| Path | File | Mechanism | Change |
|---|---|---|---|
| Direct play (the tonight failure) | `internal/playback/directplay.go:52` `ServeDirectPlay` | `http.ServeContent` of the full media file | wrap `w` before `ServeContent` |
| Remux stream | `internal/playback/remux.go:226` `ServeRemux` (write loop `:255`) | manual chunked `Write`+`Flush` | wrap `w` (loop needs no change) |
| Offline downloads | `internal/api/handlers/downloads.go:401` → `svc.ServeFile` | full media file download | wrap `w` at the handler before delegating |
| Transcode-node proxy | `internal/api/handlers/playback.go:2998` `io.Copy(w, resp.Body)` | long-lived proxy of remote transcode output | wrap `w` |
| Ebook/converted serving | `internal/api/handlers/ebook_convert_serve.go:159`, `ebook_reader.go:625` | `ServeContent`, files up to ~100s of MB; slow readers can exceed 120s | wrap `w` (cheap, same helper) |

Explicitly **unchanged**:

- Transcode HLS segments (`playback.go:2897 http.ServeFile`) — bounded few-second
  files; 120s absolute is fine. (Harmless to wrap later if segment sizes grow.)
- WebSockets (`/control/ws`) — hijacked connections manage their own deadlines;
  tonight's WS survived 4+ minutes untouched, confirming no interaction.
- Jellyfin/ABS compat listeners — separate `http.Server`s, already `WriteTimeout: 0`;
  optionally migrate them to the rolling writer later so they regain stall
  protection (follow-up, not required).
- All JSON/image/API routes — keep the server-level 120s absolute deadline.

`cmd/silo/main.go:2374` itself stays at `WriteTimeout: 120s`. That is the point: the
global guard remains, streaming handlers opt out per-response with a better contract.

## Server tests

1. **Unit — rolling behavior:** wrapper on a fake ResponseController records deadline
   bumps; assert bump on first write, rate-limited bumps thereafter, `ReadFrom`
   chunking delegates and bumps per slice.
2. **Integration — survives the server timeout:** `httptest.Server` with
   `WriteTimeout: 1s`; handler streams 3s of throttled data through the wrapper
   (window 2s). Assert full body received (fails without wrapper — this is the
   regression test for tonight's bug).
3. **Integration — stalled client still reaped:** wrapper with window 500ms writing
   to a client that stops reading; assert handler write errors within ~window, not
   never.
4. **Regression:** non-streaming route on the same server still killed at
   `WriteTimeout` (guard unchanged).

## Validation on dev (after tonight's viewing)

1. `make dev-deploy`.
2. Re-run the range-GET probe (script preserved from tonight's session): sustained
   open-ended GET at playback rate for ≥ 15 min on both direct and CDN paths; expect
   zero early closes and no `duration_ms=120000` entries in the request log.
3. Long ATV playback session (existing client build): expect zero
   `prematureSourceEnd` teardowns over a full film.
4. Spot-check normal API latency/behavior and one Jellyfin-compat client.

## Follow-up workstream: silo-apple client hardening (separate branch)

Defense-in-depth so any truncating origin/middlebox (not just our own bug) degrades
to an invisible reconnect instead of a 5s rebuild:

1. Park persistent mid-stream `.prematureEOF` into the existing outage ride-through
   once the cursor-resume retries reproduce the same truncation offset
   (`PlaybackOriginOutagePolicy.shouldPark`, `PlaybackSourceOriginStream.swift:159-173`;
   writer then gets the 240s park allowance instead of the 10s deadline). Keep it
   kill-switchable alongside `SILO_DISABLE_OUTAGE_RIDE_THROUGH`.
2. Add `[CMP-ORIGIN]` logging: one line per origin connection end (cause, HTTP
   status, cursor, bytes delivered, retry streak) — tonight the entire retry-and-give-up
   sequence ran with zero log output.
3. Verify reconnects use fresh connection state (AetherEngine builds a new URLSession
   per reconnect to avoid poisoned keep-alive pools; our 3 rapid same-offset failures
   while a fresh session succeeded seconds later is circumstantial evidence we reuse).
4. Optional: raise `.prematureEOF` retry cap toward AE's progress-aware semantics.

Android note: the server fix benefits Android identically with no client change;
worth a later check of how its player handles a 120s kill today (it may have been
absorbing them silently, or contributing to unexplained rebuffers).

## Rollout

Dev first (above), soak for a few days of normal viewing, then include in the next
server release. The change is additive and per-handler; risk is localized to the
wrapped streaming paths, and the global timeout still protects everything else.
