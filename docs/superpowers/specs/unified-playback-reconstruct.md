# Unified playback-session reconstruction

**Status:** Implemented (`feat/unified-playback-reconstruct`)

This document is self-contained: it describes the whole design, the mechanism, the data
model, the failure analysis, and the rationale that led here. Commands and paths are
repository-relative; assume the repository root is the cwd.

---

## 1. The problem, in plain language

Someone is watching a movie. The server is doing real work to feed them — transcoding,
remuxing, or just streaming bytes — and it keeps a little memory of *who they are and what
they're watching*. That memory lives only in RAM.

Now the server restarts (`docker compose down/up`, a deploy, a crash) and comes back in under
a minute. The viewer never stopped watching — they have ~2–3 minutes of video buffered ahead
of them. But the server came back **with amnesia**: its memory of "who is session X, what were
they transcoding, how far along" is gone. So when the client asks for the next chunk, today the
server says *"404, never heard of you"* and playback dies the moment the buffer empties.

That bug existed for **every** server-side stream type, and each type had its **own** code path
to fix:

- **Direct play** — raw byte-range serving.
- **Remux** — repackage container on the fly (no re-encode).
- **Native HLS transcode** — full ffmpeg re-encode into HLS segments.
- **Jellyfin-compatibility (jellycompat) HLS** — Jellyfin clients streaming through Silo.

Four parallel flows, four chances to get it wrong, and only one of them (native transcode) had
ever been made restart-resilient. This work makes **all four** survive a restart through **one
shared flow**.

**The guarantee:** after this change, *every* video playback session — direct play, remux, and
HLS transcode, on **both** the native Silo API and the Jellyfin-compatibility (jellycompat)
path — resumes across a server restart instead of 404-ing. The methods differ only in *how
much* must be rebuilt (see §2): direct and remux are stateless re-serves with no runtime to
lose, so they just need their session lookup to survive; only transcode rebuilds an ffmpeg
process. This holds subject to the operational preconditions in §10 (persistent transcode dir,
client retry, outage within the buffer runway / card TTL) and the integrated-transcode
node-affinity constraint in §9. (Non-video sessions — audiobooks, ebooks — are out of scope.)

---

## 2. The one idea

A client always re-requests the next chunk after an outage, and **its request states where it
is** (an HTTP `Range`, a `?seek=`, or a segment number like `seg_00200`). So the server doesn't
need to remember a live session — it can **rebuild it just-in-time** from two things:

1. A tiny, durable **recipe card** — the handful of facts needed to reconstruct the session
   (who, what file, which codecs/tracks/quality). This is the *only* new thing we persist.
2. The **position the client itself supplies** on the very request that triggers the rebuild.

Everything else either already lives in Postgres (user identity, file identity, access scope)
or is implied by the request. Position can never be "lost," because the client re-supplies it
every few seconds.

One helper does this for every handler:

```
                        ┌──────────────────────────────────────────┐
   any serve handler →  │ TranscodeManager.LoadOrReconstructSession │
   (stream / segment /  │   1. GetSession(id)                       │
    manifest / compat)  │   2. miss? → rebuild Session from card    │  ← reconstruct
                        │   3. re-bind ownership to live caller      │  ← security (2-factor)
                        └──────────────────────────────────────────┘
                                          │
                         ┌────────────────┴─────────────────┐
                         ▼                                   ▼
                  Tier 1: Session-only            Tier 2: Session + runtime
                  (direct, remux)                 (transcode, jellycompat)
                  client re-supplies position;    also rebuild ffmpeg at the
                  ServeDirectPlay / ServeRemux    requested segment via
                  just re-run — nothing else.     ReconstructTranscode.
```

---

## 3. Visual flow: a restart mid-playback

### 3.1 The normal day (no restart)

The client keeps a **~120-second bucket** of video filled ahead of where it's watching,
topping it up by asking the server for the next chunk every few seconds.

```
   Watching here              Bucket filled to here
        │                            │
        ▼                            ▼
   ●────────────────────────────────┤
   0:00      (already played)      +120s
        └──────── client keeps topping this up ────────┘
                  "give me segment N, now N+1, now N+2…"
```

### 3.2 The restart (the whole point)

```
 SERVER:  UP ───────────────✕ DOWN (≤60s) ✕──────────────── UP again
                            │                               │
 CLIENT:  buffered ─────────┼──── playing from buffer ──────┼──── next request
                            │                               │
                       (in-memory session + ffmpeg lost)    │
                                                            ▼
   GET next chunk  ─────────────────────────────────────────────────────────┐
   (+ login auth, + session id, + position: Range / ?seek= / seg_NNNNN)      │
                                                                             ▼
   ┌─────────────────────────────────────────────────────────────────────────────┐
   │ LoadOrReconstructSession                                                      │
   │   session gone → load recipe card → rebind ownership (refuse 0/mismatch)      │
   │                                                                               │
   │   card.PlayMethod ?                                                           │
   │     direct    → rebuild Session;            ServeDirectPlay (HTTP Range)      │
   │     remux     → rebuild Session;            ServeRemux at ?seek= (re-spawn)   │
   │     transcode → rebuild Session +           ReconstructTranscode seeked to    │
   │                 TranscodeSession/ffmpeg     the requested seg_NNNNN           │
   └─────────────────────────────────────────────────────────────────────────────┘
                                                                             │
   chunk served; client's buffer refills; user never noticed. ◄─────────────┘
```

### 3.3 The transcode rebuild, in detail

When the missing runtime is an ffmpeg process (transcode / jellycompat), the rebuild has a fast
path and a slow path:

```
  Client: "GET seg_00200 for session X"   (+ its normal login auth)
                    │
                    ▼   ← instead of 404, RECONSTRUCT:
      ┌─────────────────────────────────────────────┐
      │ 1. Load the recipe card for session X:       │  ← durable descriptor
      │    which file, which user, which             │
      │    quality / codec / audio / subtitle.       │
      │ 2. Re-bind ownership: the login on THIS      │  ← security: 2 factors
      │    request must own session X (reject if 0   │     (auth token AND ownership)
      │    or mismatched).                           │
      │ 3. Re-register the Session in memory.        │
      │ 4. Need seg_00200:                           │
      │    • already on disk? → hand it over          │  ← fast path (persistent dir)
      │    • not on disk?     → start ffmpeg seeked   │  ← rebuild path
      │      to 200 × segDur, produce from there.    │
      └─────────────────────────────────────────────┘
                    │
                    ▼
        Hands back seg_00200. Client's bucket refills. User never noticed.
```

### 3.4 Why the runway math works

- Buffer 120–180s, downtime ≤60s ⇒ when the server returns, the client still has **60–120s**
  buffered before it needs the next *unbuffered* segment.
- Worst case (segment dir was wiped): cold ffmpeg start (~0.5–2s) + encode one 2–6s segment
  faster-than-realtime ⇒ a few seconds to first byte, comfortably inside the 60s floor.
- Therefore **re-transcoding on demand is a normal path, not a fallback to avoid.** The buffer
  *is* the reconstruction budget.

---

## 4. Architecture: one manager, one front door

Everything routes through a single `playback.TranscodeManager` that **both** the native
(`internal/api/handlers`) and jellycompat (`internal/jellycompat`) handlers embed and delegate
to. The card-lifetime rules, the reconstruct concurrency cap, and the node-affinity constraint
therefore live in exactly one place instead of being copied per handler.

```
   internal/api/handlers.PlaybackHandler ─┐
                                          ├─► *playback.TranscodeManager
   internal/jellycompat.PlaybackHandler ──┘     ├─ transcodes map + mutex
                                                ├─ RecipeStore wiring (save/refresh/delete)
                                                ├─ reconstruct single-flight + concurrency cap
                                                ├─ LoadOrReconstructSession (the front door)
                                                ├─ ReconstructSession / ReconstructTranscode
                                                └─ CloseTranscodeSession / orphan cleanup
```

Before this work, jellycompat carried its **own** `PlaybackHandler` with a private `transcodes`
map and a duplicated transcode lifecycle that never grew the reconstruct half — that duplication
*was* the root cause of jellycompat dying on restart. Retiring it gives jellycompat reconstruct,
the cap, the affinity rule, and the card lifecycle for free.

`SessionManager.RegisterReconstructed` inserts a rebuilt `Session` under its **existing**
session id (no fresh UUID, no limit double-count, race-yielding to a concurrent winner) — the
piece that lets reconstruct re-establish a session without it looking like a brand-new play.

---

## 5. The recipe card: one descriptor, four methods

The `RecipeCard` gained a `PlayMethod` discriminator (`direct` / `remux` / `transcode`). That
single field is what lets one descriptor and one reconstruct path serve every method — they
differ only in the per-method runtime rebuilt on demand:

| Method | What the card carries | What reconstruct rebuilds |
|--------|-----------------------|---------------------------|
| `direct` | identity (user/profile/file) | just the `Session`; client re-supplies `Range` |
| `remux` | identity + audio selection | `Session`; ffmpeg re-spawns at `?seek=` |
| `transcode` | identity + full `TranscodeOpts` | `Session` + `TranscodeSession`/ffmpeg seeked to seg |
| jellycompat | a `transcode` card keyed by the upstream session id | same as transcode, via the shared manager |

**Back-compat:** an empty `PlayMethod` decodes as `transcode`, so cards written before this
change still reconstruct correctly.

For the transcode/remux runtime to emit **manifest-compatible** segments (boundary- and
decode-timestamp-compatible, IDR-aligned at segment edges — *not* byte-identical), the card
stores the **full** encode parameter set, not a subset: everything in `TranscodeOpts`
(`internal/playback/transcode.go`) that affects output bytes — input path, source/target
codecs, resolution, bitrate, segment duration, start segment number, HW accel/device, subtitle
track + burn-in, audio track, faststart, total duration. Non-serializable runtime fields (log
sink, context, cmd, channels) are **not** stored; they are re-wired on reconstruct.

These are *choices* made at session start (rendition/ABR negotiation, subtitle burn-in) that are
not safely re-derivable from the client and that the client must not be able to forge — so they
are persisted exactly, and treated as a rebuildable cache, not sacred state.

---

## 6. What carries the state — and why it's swappable

Two pieces of durable state back the whole feature, each behind an **interface** so the storage
engine is a constructor-only swap that touches neither the reconstruct logic nor the handlers:

| State | Interface | Default impl | Backing table |
|-------|-----------|--------------|---------------|
| Recipe card (all methods) | `playback.RecipeStore` | `PostgresRecipeStore` | `transcode_recipes` |
| Compat play session | `jellycompat.CompatPlaybackStore` | `DurableCompatPlaybackStore` (write-through cache) | `jellycompat_playback_sessions` |

All reconstruct logic and every handler call **only the interfaces**. The Postgres surface is
confined to the two `New*Store(pool)` constructors plus the two table DDLs. Both
implementations are **nil-safe**: a nil pool yields a disabled no-op (recipe store) or a
cache-only degrade (compat store), so callers never special-case an unavailable database.

> **Why Postgres, not Redis?** The card is low-volume, single-reader, write-once / delete-once
> state — not hot-path. Putting it in Postgres means **no new dependency** and **always on**
> (no graceful-degrade gap), and it is *more* durable than a cache (a Redis flush silently drops
> every card; a table does not). The interface seam means a `RedisRecipeStore` remains a
> drop-in. See [Appendix A.3](#a3-storage-redis-vs-postgres) for the full trade-off.

### 6.1 `transcode_recipes`

One row per active transcode session: `session_id` PK, denormalized identity columns
(`user_id`, `profile_id`, `media_file_id`, `transcode_node_url`), the full card as `opts JSONB`,
`created_at`, and `expires_at` (indexed).

- **Write/read:** `Save` is `INSERT … ON CONFLICT (session_id) DO UPDATE` with
  `expires_at = now() + interval`; `Get` is `SELECT opts … WHERE session_id=$1 AND expires_at >
  now()`; `Delete` removes the row.
- **TTL is an `expires_at` column, not a session-length cap.** It is re-armed on activity
  (throttled to ≤1/min, off the per-segment hot path). Current value 30 min. A 2-hour movie
  restarted 1 hour in was refreshed seconds before the outage, so it trivially survives a ≤60s
  gap; the 30 min only bounds how long an *idle/paused/abandoned* session stays resumable.
- **Expiry = filter-on-read + sweep-on-timer.** Reads filter `expires_at > now()`, so a lapsed
  row is invisible the instant it expires — correctness never depends on precise deletion
  timing. `DeleteExpired` physically reclaims rows on boot and on the reconciler janitor tick.

### 6.2 `jellycompat_playback_sessions`

One row per active compat play session: `id` PK, denormalized `compat_token` and `user_id`
(indexed for lookup/audit), the full `PlaybackSession` as `data JSONB`, `created_at`, and
indexed `expires_at`. It holds the **load-bearing** `PlaySessionId → UpstreamSessionID` mapping
plus the negotiated media sources, route item id, and seek. Without it, after a restart the
compat segment/manifest handlers 404 at their first lookup before any transcode reconstruct can
even run. `DurableCompatPlaybackStore` is a write-through cache: reads hit memory first, fall to
Postgres (the restart case), and `FindByRoute` scans live rows by compat token.

This store is what makes **all** compat methods resilient, not just HLS transcode. Compat direct
play and remux (`HandleVideoStream`) are Tier-1 stateless re-serves exactly like their native
counterparts — `ServeDirectPlay` re-runs with the client's `Range`, `ServeRemux` re-spawns
ffmpeg at the client-supplied `?seek=` — so they carry no recipe card. The only thing that could
404 them after a restart was the compat `PlaybackSession` lookup (`resolvePlaybackRoute`) coming
up empty; reloading it from this table closes that gap for direct/remux and HLS uniformly. (In a
node-planner deployment, compat direct/remux instead redirect to a proxy node with a
self-contained `streamtoken` JWT and never touch the integrated server's memory at all — so they
are resilient by construction.)

---

## 7. Card lifetime & cleanup lifecycle

The central invariant: **card lifetime is decoupled from session liveness.** Liveness uses a
short grace (governs concurrency-limit accounting); the card uses a long TTL (governs
resilience). This is what turns three different "lost the runtime" events into a resume instead
of a failure — and it falls out for free:

- **Server restart** — the headline case.
- **ffmpeg crash recovery** — process dies, session torn down, card kept → next request rebuilds.
- **Pause-past-the-grace resume** — a paused session is reaped to free its slot (correct), but
  the card survives, so resuming later reconstructs under the same id instead of restarting the
  movie.

The card is deleted **only on a genuine user/admin stop** (`DELETE /playback/{id}`, WS
`Stop`/`Terminate`, admin force-stop — threaded via a `userInitiated` flag). On a liveness reap,
ffmpeg crash, abort, or transcode-restart-under-the-same-id the card is **kept**; an abandoned
card is reclaimed by its own `expires_at`, so it never leaks a slot or a dir.

Segment-dir deletion is driven by **card expiry**, not by boot. The predicate is "*card
absent/expired AND not in the live transcodes map*", applied by three triggers: the user-stop
finalization, the periodic reconciler janitor, and a descriptor-aware boot sweep
(`CleanupOrphanedTranscodes` spares dirs whose card is still active). **Fail-safe:** if the card
store is unreachable at cleanup time, **skip the wipe** — a bounded disk leak is recoverable;
deleting a live session's segments is not.

---

## 8. Security: two-factor, preserved exactly

Reconstruct does **not** introduce an "anyone with the UUID gets in" risk. Every
segment/manifest/stream request needs **both**:

1. an authenticated caller (routes are under `RequireAuth`), **and**
2. a `session.UserID` that matches that caller.

The `session_id` is a 122-bit crypto-random UUID — useless to a third party, who would also
need the owner's auth token. Reconstruct re-binds `session.UserID` to the **live** authenticated
caller and lets the existing ownership check run unchanged. It must **refuse on `userID == 0` or
mismatch** — it never trusts the card's stored `user_id` alone. The card stores no secrets;
`media_file_id`/`user_id` are re-resolved against Postgres at use time, so revoked access takes
effect immediately, even mid-reconstruct.

**Jellycompat auth mapping:** the compat path authenticates by Jellyfin token, not native
`RequireAuth`. The compat session's user is mapped to the native user id (the card is written
with the native `StreamAppUserID`) before reconstruct, so the same ownership re-bind and refusal
rules apply unchanged.

---

## 9. Concurrency cap & the node-affinity constraint

- **Reconstruct concurrency cap.** A restart can trigger a burst of simultaneous reconstructs
  (every active client retries at once). `acquireReconstructSlot` bounds concurrent reconstructs
  with a semaphore and lets a cancelled request give up its place rather than queue dead work.
  Jellyfin segment requests are a burst source too, so the cap is shared by both handlers.

- **Node-affinity constraint (load-bearing).** The playback `SessionManager` is per-process and
  not shared across API front-ends; recipe cards **are** shared. For a **remote transcode-node**
  session the card's non-empty `transcode_node_url` makes every front-end proxy to the same
  ffmpeg, so reconstruct is node-safe. For an **integrated transcode** (`transcode_node_url ==
  ""`) the card carries no owning-node identity: a front-end that misses the session
  reconstructs its **own local ffmpeg**. If requests for one integrated session are spread
  across multiple front-ends *without* sticky affinity, two front-ends can run divergent ffmpeg
  against different dirs — split-brain.

  Therefore, until the card gains an owning-node identity (deferred), integrated transcode is
  supported only when **every request for a session reaches the same front-end**: a single API
  front-end, or LB session affinity keyed on the playback session id. Deployments needing many
  front-ends should run transcodes on dedicated `--mode=transcode` nodes (the remote path, which
  is unaffected). This constraint is also documented at the reconstruct call site in code.

---

## 10. Preconditions (each is load-bearing)

The runway math holds only if three things are true; if any fails the feature delivers nothing,
so verify them first.

- **P1 — `TranscodeDir` survives `compose down/up`.** Default `/tmp/silo-transcode` is
  container-ephemeral; if it doesn't survive, the "serve surviving segments" fast path never
  fires (only re-transcode does — still works, but slower). **This is the single
  highest-leverage operational step, and it is config, not code.** Point
  `playback.transcode_dir` at a persistent **named volume** mounted at a **stable path
  identical across restarts** (not `tmpfs`, not the container's writable layer):

  ```yaml
  services:
    silo:
      volumes:
        - transcode-cache:/var/lib/silo/transcode
  volumes:
    transcode-cache:
  ```

  Then set `playback.transcode_dir` to that path. A persistent dir does not self-clear, so disk
  hygiene now depends on the janitor (which spares dirs whose card is still active). For remote
  transcode nodes it is the **node's** `TranscodeDir` that must be persistent (or a shared FS).

- **P2 — Re-transcode of one segment completes well under the buffer floor** on worst-case
  hardware (software-decode fallback; subtitle burn-in is heaviest). Measure.

- **P3 — Client retry/backoff exists.** Without it the server work is invisible. Clients must
  retry segment/manifest GETs on 5xx/connection error and reconnect the WebSocket. Coordinated
  in `silo-android` / `silo-apple` (and verified per major Jellyfin client for the compat path).

---

## 11. Files, verification, and what's deferred

### 11.1 Files

| Category | Files |
|----------|-------|
| New source | `internal/playback/transcode_manager.go`, `internal/playback/recipecard.go`, `internal/playback/recipecard_postgres.go`, `internal/jellycompat/playback_sessions_postgres.go` |
| Modified source | `internal/api/handlers/playback.go` (drained into the manager), `internal/api/handlers/stream.go`, `internal/api/router.go`, `internal/playback/session.go`, `internal/playback/transcode.go`, `internal/jellycompat/{streams,handlers_playback,playback_sessions,router,server,auth}.go` |
| Migrations | `migrations/sql/20260617233705_add_transcode_recipes.sql`, `migrations/sql/20260618053223_add_jellycompat_playback_sessions.sql` |
| Tests | `internal/playback/{transcode_manager,reconstruct,transcode_close,recipecard}_test.go`, `internal/jellycompat/playback_sessions_postgres_test.go` |

The native handler is *net subtractive* — ~90% of the manager is logic **moved** out of
`playback.go`, not new surface. The genuinely new logic is `LoadOrReconstructSession`, the
`PlayMethod` card fields + constructors, the durable compat store, and the reconstruct call
sites.

### 11.2 Verification

- `go build ./...`, `go vet`, `make verify-local-paths`, `make migrate-validate` — clean.
- `golangci-lint` (v2) clean on all new/changed files.
- Unit + race tests for `internal/playback`, `internal/jellycompat`, `internal/api/handlers`
  (run in a libvips/CGO-capable container — a bare-host `go test ./...` skips the CGO
  `internal/api/handlers` package). `LoadOrReconstructSession` is covered across
  live / forbidden / zero-caller / reconstruct(remux) / refused-mismatch / missing; the
  recipe-card round-trip, legacy decode, and disabled no-op; `RegisterReconstructed`
  insert/race-yield/concurrency; the close-vs-close-process dir semantics; and the
  reconstruct concurrency cap. The durable compat store has a `SILO_TEST_DATABASE_URL`-gated
  round-trip proving a session written by one instance reloads in a fresh one (the restart
  case), plus a nil-pool cache-only path.
- Manual: `docker compose down/up` (<60s) mid-playback for native direct, remux, transcode, and
  a Jellyfin HLS client; confirm reconstruct instead of 404; repeat with an ephemeral vs
  persistent `TranscodeDir`.

### 11.3 Deferred (intentional) & risks

- **Redis-backed** `RecipeStore` / `CompatPlaybackStore` — constructor-only follow-up.
- **`503 + Retry-After`** instead of today's response on reconstruct — needs client capability
  negotiation; a cross-repo decision, so deferred. Until then the feature relies purely on
  client retry/backoff and changes no existing response shape.
- **Owning-node id on the card** for integrated transcode — removes the node-affinity
  constraint (§9).
- **Cross-process janitor↔reconstruct locking** and **partial `.tmp` segment validation** — a
  Go mutex can't serialize a transcode-node `os.RemoveAll` against an integrated-server
  reconstruct; the serve-from-disk fast path should validate segment completeness, not just
  existence.
- **Real-media measurement** of `-copyts` splice timing (VFR / long-GOP / audio-priming sources
  can desync A/V at the splice) and subtitle-burn-in runway cost (a full filtered re-encode,
  far heavier than the "0.5–2s + one segment" estimate).
- **HW-accel re-acquisition** (QSV/VAAPI/NVENC contention) may fail on reconstruct; falling back
  to software changes encoder output (in-band SPS/PPS drift on the MPEG-TS segment path).
- **`503` retry-storm** from many clients reconnecting at once — add client backoff jitter; the
  server-side reconstruct cap (§9) is the counterpart.

---

## Appendix A — Design history & rationale

This is the trail of *why* the design is shaped the way it is. It is not needed to understand or
operate the feature; it records the decisions and the alternatives that were rejected.

### A.1 Reconstruct, not rehydrate

The first sketch tried to **snapshot live runtime objects** (the ffmpeg process, the in-memory
session) and rehydrate them after a restart. That is fragile: runtime objects hold file handles,
contexts, channels, and OS process state that cannot be serialized meaningfully. The reframe
that unlocked everything: a missing in-memory session is not an error to recover from a snapshot
— it is a **reconstruct trigger**. Rebuild from durable identity/authorization/file facts
(already in Postgres) plus a small persisted descriptor plus the position the client re-supplies.
Nothing live is ever frozen; the descriptor is a rebuildable cache.

### A.2 Phased delivery (how the four methods landed)

The work shipped in stages to keep each change reviewable:

1. **Native HLS transcode** first — the recipe-card + reconstruct path, Postgres-backed.
2. **Extract the shared `TranscodeManager`** out of the native handler (no behavior change),
   then **generalize `RecipeCard`** with the `PlayMethod` discriminator and add
   `LoadOrReconstructSession` as the single front door.
3. **Direct + remux** — they reuse the front door; direct needs no runtime, remux re-spawns at
   `?seek=`.
4. **Jellycompat** — retire its duplicate `PlaybackHandler`, embed the shared manager, and add
   the durable compat session store so its `PlaySessionId → UpstreamSessionID` mapping survives.

Direct play technically already survived restarts (stateless byte ranges), but folding it into
the same card/front-door flow removed the last special case.

### A.3 Storage: Redis vs Postgres

The card is **low-volume, single-reader, write-once / delete-once** state. The historical "Redis
is the smaller surface" argument only held *while Redis was already a hard dependency*.

| Dimension | Gated Redis | Postgres table (chosen) |
|-----------|-------------|-------------------------|
| New dependency | Requires Redis | None — Postgres always present |
| Gating | Off without Redis (degrades to old behavior) | Always on — no deployment left out |
| Expiry | Free via `EXPIRE` TTL | `expires_at` + filter-on-read + janitor sweep |
| Durability | Lost on a Redis flush (it's a cache) | Survives anything Postgres survives |
| Multi-node | Shared instance | Shared DB (single-reader; see below) |
| Ops/visibility | `redis-cli`, opaque-ish | A real table: `SELECT *`, joinable, auditable |
| Fits "remove Redis" direction | Against it | With it |

**Multi-node on Postgres is clean** precisely because the card has *exactly one reader* — the
integrated server doing reconstruct. Worker (transcode/proxy) nodes never read it: reconstruct
reads the card and re-issues `/transcode/start` to a node with the opts *in the request*. So
there is no cross-node shared-cache requirement, only a shared *record*, which a shared Postgres
already is. `transcode_node_url` is a column, so a front-end can re-pin or re-dispatch after a
restart; an HA pair of API servers on one Postgres both reconstruct for free. The one tradeoff:
a write on the playback-start path and a small table bounded by the sweeper — negligible at
Silo's scale.

The store is an **interface** with a nil-safe impl specifically so this is a reversible
decision: a `RedisRecipeStore` is a constructor swap that touches no reconstruct logic, handler
hooks, card-lifetime rules, or cleanup logic.

### A.4 Considered: encode the descriptor into the session token itself

We considered making the `session_id` a **signed JWT** whose claims carry the session inputs, so
the "recipe" travels inside the token and no server-side card is needed. Rejected as a
*replacement*, recommended as a *complement* for future multi-node work:

- **What it solves well:** stateless authorization and self-routing across LB / proxy /
  transcode nodes — any node verifies ownership from the signature without a lookup.
- **Why it can't replace the card:**
  - **Effective ≠ requested.** HWAccel may fall back to software, a 4K guard may downgrade
    resolution, the segment-numbering base is computed — all decided *after* mint. Baking
    *requested* values into a frozen token risks a reconstruct that desyncs from the manifest the
    client already holds.
  - **Mutability.** Quality switch / audio-track change / node re-pin mutate the session
    mid-flight; a JWT is immutable, so each change mints a *new* token — reintroducing the
    "new session" churn we're avoiding.
  - **Revocation isn't free.** Self-contained tokens are revocable only via expiry or a
    server-side denylist — i.e. back to needing a DB for the one thing the token was meant to
    avoid. Today's model revokes instantly by re-resolving against Postgres each request.
  - **Size/exposure.** A fat token rides every segment URL and leaks metadata if logged; a
    122-bit UUID does neither.

**This already partly exists.** Silo's `internal/streamtoken` JWT already carries the
*requested/static* stream parameters for delivery + routing, verified by signature alone on
proxy/transcode nodes (no DB). It and the recipe card are complementary, not competing: the
stream token proves "this caller may stream this session"; the card holds "what to rebuild." A
future multi-node design can extend the `streamtoken` claims for identity/routing and keep a
slimmed card for the mutable/effective encode state. (Caveat: `JWTSecret` is a single symmetric
secret shared with every node — fine for trusted nodes, but an asymmetric/per-node credential is
the eventual hardening.)

### A.5 Failure analysis — when each risk actually bites

| # | Issue | When it happens | Severity |
|---|-------|-----------------|----------|
| 1 | `TranscodeDir` on throwaway storage — fast path never fires | Every restart, if not on a persistent volume (still works via re-transcode) | Precondition |
| 2 | Re-transcode slower than remaining buffer runway | Rebuild path; weak hardware or subtitle burn-in | Major |
| 3 | Timestamp drift at the splice → brief A/V desync | Rebuild path, awkward source media (VFR, long GOP, audio priming) | Major |
| 4 | Client treats the one failed request as fatal | Every restart, if clients lack retry+reconnect | Precondition |
| 5 | Janitor deletes a folder mid-rebuild | Rare race; worse across nodes (no simple in-process lock) | Major |
| 6 | `404 → 503` behavior change for old clients | Rollout/coordination, not a runtime failure | Minor |

**One-sentence summary:** the client keeps playing from its buffer through the outage; when it
comes back asking for the next chunk, the server — which forgot everything — rebuilds just that
session from a tiny saved recipe plus the position the client itself supplies, and the user never
sees the gap, *as long as* segment storage is persistent, the client retries, and rebuilding a
chunk is faster than the buffer drains. The biggest *practical* risks are the dull ones (#1
persistent storage, #4 client retry), not the exotic ffmpeg ones.

---

## AI-use disclosure

Designed and implemented with AI assistance (design, codebase exploration, implementation, and
an adversarial review pass). Code references were verified against the tree at authoring time;
re-verify identifiers before relying on them, as the repo moves quickly.
