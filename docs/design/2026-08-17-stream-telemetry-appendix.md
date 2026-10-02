# Stream Telemetry — Appendix: approaches tried and discarded

Companion to [the stream telemetry design](2026-08-17-stream-telemetry.md). That
document says what the system *is*; this one says what it deliberately is **not**, and
why. Every entry below was proposed, argued for, and rejected on evidence — several of
them twice.

Read this before proposing a simplification. The working document is short because the
reasoning was moved here, not because the reasoning is absent. Section references
(`§2.5`, `§4.4`, …) point at the working document.

---

## A. Approaches abandoned during implementation

Found by building, measuring or testing — not by review.

| Abandoned approach | Why it failed |
|---|---|
| **Nesting `io.LimitReader` in every accounting layer** | Go's kernel sendfile path unwraps exactly one limiter, and `http.ServeContent`'s `io.CopyN` already contributes it — so the *first* accounting layer silently forfeits sendfile. Measured with `strace`: 0 syscalls through the mounted proxy chain, 6 after slicing the limiter instead of nesting it (§4.4). This had been "settled by reasoning" twice, wrongly, before anyone ran `strace`. |
| **P0a's first sendfile fix** (`79493512`) | It restored `ReadFrom` forwarding but kept the nested limiter, so it did not actually restore sendfile. A byte-exact body and a correct `Range` status prove HTTP correctness, not sendfile. |
| **ORing `StartedAtDegraded` across every publisher** | A relay-only contribution carries a publisher-local first-seen stamp, so correlating a transcode node degraded an otherwise authoritative viewer-edge session. Start-time authority is viewer-edge-owned (§2.5). Caught by adversarial plan review before it shipped. |
| **"401/403/404 create no logical activity" as a blanket rule** | Conflicts with why manifest routes are enrolled at all: the compat master manifest finishes authorization, *then* starts a transcode, and can still 404. The boundary is authorization success, not response status (§4.2). |
| **A family flag defaulting to every family** | Would widen instrumentation across two more live byte paths inside the API process on upgrade alone. The default must be what already shipped; "set the variable before deploying" is a runbook, not a safe default (§6). |
| **Folding "field absent on one side" into a parity mismatch** | Legacy rows carry no value for several compared fields, so `agrees` would be permanently false and the flag useless. Absence is its own axis, counted in `fields_absent` (§6/P0d). Settled by writing the test and finding the assertion wrong, not the code. |
| **Asserting byte totals from `Registry.Snapshot()`** | `BytesAccepted` there is `lastSweptBytes`; only `Sweep()` folds live observations. Five tests, one full remediation round (§2.2). |
| **Repointing admin reads onto telemetry in P0d** | The payload is a join onto ~50 display fields telemetry is not canonical for, and parity has never been observed. The design orders comparison before repoint for a reason (§6/P0d). |
| **A ticker-refreshed global view** | `BuildGlobalView` costs ~347 ms at the 50,000-session cap. A ticker pays that on every server forever whether or not an admin is looking. Replaced by a read-driven TTL cache with single-flight refresh (§6/P0d). |
| **A blanket compression bypass on media routes** | Subtitle font bundles are JSON and sit below global compression; bypassing would drop `Content-Encoding`/`Vary` and change the wire contract. Bypass only non-compressible bulk paths (§4.4). |
| **`r.Use` middleware for ABS telemetry** | The ABS group is shared with socket.io by `Mount`, so a group-level wrapper sits over the websocket upgrade. Wrapped per route instead, with the mounted-router socket test running telemetry both off and on (§4.2). |
| **Reusing the compat `nodes.go` Redis scan for parity** | That endpoint passes stored session JSON through opaquely so an older node's extra fields survive the round trip; a decoding reader would drop them. A second, decoding reader exists for the parity path only (§6/P0d). |

---

## B. Positions abandoned during design review

The design went through four adversarial review rounds by Codex `gpt-5.6-sol` at high
reasoning effort plus four maintainer scope corrections. Roughly one review finding in
five was itself wrong, so every one was verified against the code before acceptance.
Condensed, newest first.

### Revision 8 — maintainer direction, after P0a shipped

| Was | Became |
|---|---|
| Identity disagreement **quarantines the row** | **Record and surface both values** with a prominent admin warning. Quarantining hides exactly the case the system exists to catch: a publisher conflict is itself a possible abuse signal. Monitoring records and surfaces only; decision logic is deferred to P1+ (§2.5). |

### Revision 7 — maintainer direction

| Was | Became |
|---|---|
| `LibraryHarvest` is the primary rip signal | **`DeliveryRate` ships first** — no storage, catches the single-pass rip. Harvest demoted to the patient case and re-evaluated before P2 (§5.3). |
| Identity = ids + route + method + IP | Adds the **request-time capture set**: client name/version/build/channel/UA, device id, outcome, token age, request count. Unrecoverable if not captured at request time (§2.2). |
| Downstream restream and aggregate node load are tracked gaps | **Explicit non-goals.** Not gaps, not deferred (§5.2). |

### Revision 6 — after review round 4

| Was | Became |
|---|---|
| "Only a permanent ban is durable" | **Contradicted the sanction table.** Durable state is *sanctions*; durability and permanence are separate axes (§1, §3.2). |
| `EvaluationInput` carries `Usage` + `Jobs` | **Fields with no producer.** P1 ships `View` + `Limits` only (§5.1). |
| "Five rules ship" | Two enforce; the rest deferred without a phase (§5.1). |
| "Download flood: enforced" | **Not covered** — re-downloading one title never grows the distinct set (§5.2). |
| The view carries a "monotonic version" | It is an **opaque epoch**; a fingerprint has no order (§2.5). |
| `complete` = freshness | Freshness cannot say *which* publishers are required. Membership added (§2.5). |
| Stop-on-lease-loss = fenced | **Not fencing.** A paused leader can resume; needs a monotonic fence token validated at every mutation (§2.5). |
| Sanction cache invalidated by pub/sub | **Pub/sub is lossy and at-most-once.** Now a hint over a generation counter reconciled against Postgres (§3.4). |
| Harvest merges served intervals | **No producer exists** — no offsets on `Observation`/`Transfer`, multipart ranges give no usable `Content-Range` (§5.3). |
| ABS "records an empty address" | **False.** It falls back to `RemoteAddr`; the real defect is proxy-peer attribution (§4.3). |

### Revision 5 — after review round 3

| Was | Became |
|---|---|
| `LibraryHarvest` is ledger-free and cheap | **Withdrawn.** A byte *sum* is not byte *coverage* — serving the first 10% nine times reads as 90%. Needs a bounded, pruned coverage store; idempotence holds only *after* completion is established (§5.3). |
| Harvest denominator = 90% of `FileSize` | **Representation-specific**, and `file_size` is nullable. Transcode and remux declared **undetectable** by this rule rather than silently missed (§5.3). |
| `stream_bans` carries suspend | **Cannot** — no `action`, no lift metadata, no active state. Replaced by `stream_sanctions` (§3.4). |
| Suspend "blocks all streams" | Four unspecified behaviours, now defined: gate, in-flight fan-out, remote job stop, and what a lift clears (§3.4). |
| `StartedAt` authoritative from session creation | **No carrier existed** — no creation time in token or card, so reconstruction stamped `time.Now()`. Added in P0a (§4.3). |
| Viewer identity owned by the outermost edge | **The compat proxy could not supply it** — `buildProxyRedirectURL` omitted uid/pid/mfid entirely. Fixed in P0a (§4.3). |
| Redis failure ⇒ every replica evaluates | **Split-brain**, and worse than useless: replicas could issue *durable* suspensions from divergent local views. Fenced lease; a degraded view stops global evaluation (§2.5). |
| Violation counter per evaluation pass | A steady condition escalated to suspension in three ticks. Now per *incident transition* (§3.2). |
| Eight writer wrappers | **Nine** — chi compression is mounted globally on both routers and has no `ReadFrom` (§4.4). |
| Legacy retirement inside P0 | **Its own project** — nine named consumers including health payloads and two `/api/v1` endpoints (§6). |
| P0 ships as one phase | **Split P0a–P0d** (§6). |

### Revision 4 — after review round 2

| Was | Became |
|---|---|
| "One Snapshot, no secondary data paths" | **Asserted, not built.** `Snapshot` was per-publisher while rules needed a global view. The merge contract, ownership per field and freshness were defined (§2.5). |
| Deterministic ordering removes the need for election | **Wrong.** A total order only agrees on identical inputs. Election restored; ordering demoted to intra-epoch tie-break (§2.5). |
| `Rule(Snapshot)` | **Self-contradictory** — `OverCap` needs limits, ratio needs file sizes, volume needs history. Replaced by an assembled `EvaluationInput` (§5.1). |
| Transcode-node segments/artifacts = `viewer_egress` | **Wrong, and a repeat of a round-1 bug.** Every node route sits behind `requireBearer`; counting them re-created relay double-counting (§4.2). |
| Transcode ratio via target bitrate × runtime | **No such denominator exists.** `TargetBitrateKbps` is max *video* only and often 0; no runtime is stored; segments are re-servable (§5.3). |
| Per-session ratio catches ripping | **Wrong.** A rip reads the source once, at ~1× coverage. Demoted to alert-only secondary (§5.3). |
| Download-class pours fit the session model | **They don't** — proxy downloads mint a new session id per redirect. Split into `Transfer` (§4.2b). |
| `UsageSink` seam in P0 = "one migration, one file" | **Wrong** — multi-replica double counting, epochs, idempotency. In-memory sink dropped; the ledger is unscheduled until its contract is designed (§5.4). |
| Escalation ends in an automatic ban | **Softened.** Policy table with `suspend` as the aggressive default; `ban` stays an admin action (§3.2). |
| `clientip` missing on the proxy only | **Also missing on standalone ABS** (§4.3). |
| "No annotation fails registration" | Not implementable in chi — it cannot tell whether an arbitrary `r.Get` serves media. Replaced by a route-manifest diff test (§4.1). |

### Revision 3 — maintainer scope correction

| Was | Became |
|---|---|
| Volume and byte budget "deliberately not in v1" | **Reversed.** Concurrency caps count streams and therefore cannot bound a rip; volume is first-class (§5.2, §5.3). |
| Usage persistence unaddressed | Checked all 76 existing tables: none stores bytes (§5.4). |

### Revision 2 — after the first adversarial review

The review returned "do not implement as written", and its root finding became the
spine of the design: **an in-flight HTTP transfer is not a playback session.**

| Was | Became |
|---|---|
| Per-request meter is the unit; "no merge exists, therefore merge bugs cannot" | **Wrong.** Short transfers vanish between sweeps; relays double-count; native and remote ids genuinely differ. Aggregation is unavoidable — the fix is homogeneous role-tagged observations folded into one accumulator (§2.1, §2.2). |
| Cuts need no durability, re-derived each tick | **Wrong.** A cut that works destroys its own evidence, so it lapses and the still-valid token reconstructs. That is an exploitable duty cycle, not self-healing (§3.1). |
| Meter flag replaces `SetWriteDeadline`; lands in ~5–6s | **Wrong.** A flag is cooperative and cannot interrupt a blocked write; the real fallback is a 180s stall window with `WriteTimeout: 0` behind it (§3.3). |
| `nil` transport ⇒ "one code path" | **Overstated** — true of the rule API only. Replaced by an explicit `SnapshotStore` plus per-process publisher identity (§2.3). |
| Bytes are the only liveness clock | **Too reductive.** Misses buffer-ahead, producer activity and never-served requests (§2.4). |
| `clientip` is free at the edge | **Wrong.** Not mounted on the proxy or ABS routers (§4.3). |
| The meter preserves sendfile via `Unwrap`/`ReadFrom` | **Wrong.** `io.Copy` uses a direct type assertion and never consults `Unwrap` (§4.4). |
| Enrolment list complete | **Incomplete** — missing all manifest routes, native subtitles and fonts, and the compat bandwidth probe (§4.2). |
| P0 is zero-risk | **Wrong.** No policy risk is not no playback risk (§6). |
| Node-blob Redis encoding | **Partly kept**, refined to per-instance hash + deltas + heartbeat (§8). A blob TTL would tie publisher health to node liveness. |
| Group-merged limit resolution | **Confirmed correct**, hardened with caching and a degraded-mode metric (§5.3). |

---

## C. Prior art: the `feat/sauron-async-enforcer` branch

This design supersedes an earlier attempt — 26 commits, ~12.5k lines, based on a commit
70 branches behind `main`. That branch was **mined, not rebased**, and still exists as
`origin/feat/sauron-async-enforcer` (`d01b5de3`). Its four planning documents were
copied verbatim into this branch for context and have been removed again now that their
conclusions are recorded here. They remain readable at:

| Document | Path on `origin/feat/sauron-async-enforcer` |
|---|---|
| Original plan + as-built deltas | `docs/superpowers/plans/2026-07-04-stream-monitoring-and-kill-switch.md` |
| The widened architecture decision | `docs/superpowers/plans/2026-07-07-abuse-cold-enforcer-architecture.md` |
| Path × monitoring × kill coverage matrix | `docs/architecture/playback-paths-monitoring-kill-matrix.md` |
| Adversarial abuse scoring, corrections 1–9, decisions A1–A8 | `docs/architecture/stream-abuse-matrix.md` |

**Kept from it — the ideas, not the code:** server-observed existence rather than
client-reported liveness; every reason collapsing to a small set of enforcement actions;
the hot path paying at most one in-memory lookup; the `Route` dimension; and the finding
that *every* byte-serving surface must be enrolled or the picture lies.

**Kept after review, having first been deleted along with the code:** reason-scoped
monotonic verdict expiry; per-process publisher identity distinct from node URL; a
bounded transfer registry; first-seen logical `StartedAt`; a non-extending cut latch;
and mounted-router/real-socket tests.

**Deliberately not repeated:** the two-tier lease; the durable `stream_revocations`
table; durable tombstones; startup resurrection machinery; the Postgres session log; and
`mergeStreams`-style reconciliation of two incompatible models. Those are consequences
of choices this design does not make.

### Inherited identifiers

Older decision and gap ids still appear in commit messages and code comments. Recorded
here so they resolve after the prior-art copies were deleted.

| Id | Meaning | Status in this design |
|---|---|---|
| **A1** | Over-cap enforcement uses a long revocation matching the token's reconstructable lifetime | Retained as the sticky-verdict argument (§3.1). |
| **A2** | Durable tombstones for revocation state | **Not repeated** — replaced by `stream_sanctions` (§3.4). |
| **A3** | Credential identity for a user cutoff comes from presented credential time | Superseded by the explicit creation-time claim (§4.3). |
| **A4** | ABS bare files and ebook/comic/PDF reads are observed and killable but **cap-exempt** | Retained (§4.2, §4.2b). |
| **A5** | Liveness is server-observed only; paused sessions holding an open realtime connection stay exempt (issue #243) | Retained (§2.4). |
| **A6** | Publish every stream to Redis keyed by per-process instance id; one evaluator per tick | Retained and hardened — the lease must be *fenced* (§2.3, §2.5). |
| **A7** | Registry saturation fails closed for download-class pours, plus a per-user concurrent-transfer cap | **Deferred to P1.** P0 serves through and reports truncation (§2.2, §9). |
| **A8** | Split the work along dependency order rather than shipping one batch | Retained as P0a–P0d (§6). |
| **GAP-10** | ABS access-log wrapper lacks `Unwrap`, so in-flight cuts no-op | **Fixed** (§4.4). |
| **GAP-11** | Ebook routes not wired into the kill switch | Enrolled as cap-exempt transfers (§4.2). |
| **GAP-12** | The rolling deadline re-arms a cut socket | Cut latch retained in the P1 design (§3.3). |
| **GAP-13** | `mergeStreams` discards edge `BytesServed` | **Not applicable** — no two-model merge exists here (§2.5). |
| **GAP-14** | Registry saturation blinds monitoring | Open, owned by P1 (§9). |
| **GAP-15** | Edge transcode visibility created before proxying | Superseded by the attachment boundary (§4.2). |
| **D18** | Jellycompat download quota hole | Out of scope — an enforcement gap in another subsystem (§5.5). |
| **E25–E28** | Compat and `auth/refresh` API flood limiting | Out of scope (§5.5). |
| **E29** | Per-node concurrent-transcode cap | Out of scope — node admission, not telemetry (§5.2, §5.5). |

---

## D. Verification and review record for P0

**Gates run on the implementation host** across the whole branch: `gofmt`,
`go build ./...`, `go vet ./...` clean; `go test ./...` with one pre-existing failure
(`TestResolveCopySeekAnchorMatchesRealLongGOPHEVC`, which needs ffmpeg ≥ 5.x);
`golangci-lint run --new-from-merge-base=origin/main` reporting 0 issues;
`make verify-local-paths` passing; `go test -race -count=3` green on
`streamtelemetry`, `proxy`, `transcodenode`, `httpstream`, `jellycompat`,
`audiobooks/...`, `api` and `nodesessions`. The web gates were not run — `pnpm` is
absent on that host — but no `web/` file was touched.

**Cross-model review coverage was uneven, and two commits have none.** The Codex side of
the relay hit its usage limit partway through the final session.

| Commit | Plan | Plan review | Implementation | Result review |
|---|---|---|---|---|
| proxy + transcode-node enrolment | Claude | **Codex** — 8 findings | **Codex** | Claude; 3 defects, fixed by Codex |
| jellycompat + ABS enrolment | Claude | **Codex** — 9 findings, 7 accepted | Claude | Claude |
| P0d admin parity | Claude | **none** | Claude | Claude |

**The P0d parity commit is the one with no second opinion at all**, and it is also the
commit that decides what "agrees" means — the semantics the legacy-retirement project
will lean on. If anything gets re-reviewed, it is that.

**What the review rounds were worth.** Codex's plan reviews caught the sendfile defect,
the start-time degradation defect, the family-gate default, and an ABS client-info
helper that had been claimed not to exist — all before any of it shipped. Roughly one
finding in five was still wrong in one direction or the other, so each was checked
against the code before being acted on.

**What worked, recorded because it is repeatable:**

- Adversarial plan review *before* writing code. Two rounds, seventeen findings, and
  the two that mattered most would both have shipped.
- Measuring instead of arguing. The sendfile question had been settled by reasoning
  twice, wrongly; `strace` settled it in one command.
- Driving mounted routers over real sockets. Handler-level tests bypass the middleware
  under test.
- Making the merge and the comparison pure functions. No Redis, no Postgres, every rule
  tested in CI.
- Writing the test before trusting the semantics. The `agrees`-versus-absence question
  was settled by a failing test — and the code turned out to be right.
- Running the gates directly rather than trusting the implementer's report. Codex
  reported the socket tests as impossible in its sandbox; they run fine on the host.

---

## E. Deferred P1+ design, retained for reference

Pulled out of the main document when P0 shipped. This is the enforcement and rules
design as reviewed, preserved so the thinking is not lost — **not** a commitment to
build it as written. Every threshold below was chosen before any production traffic
had been observed through the telemetry path, which is precisely why it was deferred.

## 3. Enforcement — P1, designed not built

### 3.1 Why a cut must be sticky

A violation that successfully stops its victim destroys the evidence it was derived
from. Cut lands → the meter disappears → the snapshot is clean → the cut lapses → the
still-valid token reconstructs (tokens stay reconstructable for ~24h, and loading an
existing session does not rerun fresh admission) → serves seconds of bytes → is cut
again. That is an exploitable duty cycle, not self-healing.

### 3.2 Four actions, chosen by a policy table

The infrastructure worth building is observing accurately and applying a deny. Which
rule triggers which response is tuning, and must never require a code change.

| Action | Effect | Reversible | Durable |
|---|---|---|---|
| `alert` | admin notification only | n/a | no |
| `cut` | stop this session; sticky so it cannot immediately resume | expires | no |
| `suspend` | block this user from all streams until lifted | yes | yes |
| `ban` | permanent denial | admin only | yes |

Default posture leans aggressive: rules escalate to **`suspend`**, not `ban`. A suspend
stops abuse immediately and is fully reversible; `ban` stays a deliberate admin action.

**A violation is an incident, not an evaluation pass.** One continuous over-cap
condition observed on three consecutive ticks is *one* violation. Count a new violation
only on a condition *transition*, or after the previous incident has cleared —
otherwise a steady state escalates to suspension in fifteen seconds.

| Violation | Sticky for | Durable? |
|---|---|---|
| 1st | 10 min cut | no |
| 2nd within the window | 30 min cut | no |
| 3rd | the rule's configured action (default `suspend`) | yes |
| Admin terminate | 30 min cut | no |
| Admin suspend / ban | until lifted / `expires_at` | yes |

**Sticky verdicts are the load-bearing part; escalation is not.** If the counter proves
fiddly, a single sticky verdict per subject still closes the oscillator. Expiry merges
monotonically and is reason-scoped: a later 10-minute over-cap verdict must never
shorten a live 30-minute admin termination. Postgres is written **only** when a
sanction is created, lifted or expires.

### 3.3 Cutting: cooperative flag *and* interrupt

A flag is only checked when execution reaches the next write, so it cannot interrupt a
`Write` already blocked on an unreading client, a `ReadFrom` blocked inside the
underlying writer, a remux goroutine blocked reading ffmpeg stdout, a segment request
waiting on production, or a relay blocked reading upstream. The real fallback is the
180s stall window, and the standalone streaming servers run `WriteTimeout: 0`, so there
is no server-level guard behind it. A cut handle therefore owns three things:

1. **An atomic flag** — stops a fast-draining pour at the next application write. This
   is the rip case.
2. **A request-scoped `CancelFunc` plus route-specific closers** — cancels upstream
   response bodies and closes ffmpeg-fed pipes, reaching blocked reads a deadline
   cannot.
3. **An immediate response write deadline**, with a **cut latch** so the rolling
   deadline's periodic bump cannot re-arm a cut socket.

Several handlers currently normalize or ignore write/copy errors, so ending the body
does not universally close the connection, especially for chunked and HTTP/2 responses.
Each enrolled route must be audited for error propagation, not assumed.

Honest bound: a cut lands within one enforcer tick for a draining stream and within the
interrupt path's latency for a blocked one. It is **not** a uniform 5s guarantee.

### 3.4 The sanction ledger

One Goose migration. The table models the *sanction*, not the ban, because `suspend` is
durable and reversible:

```sql
CREATE TABLE stream_sanctions (
    id           BIGSERIAL PRIMARY KEY,
    subject_kind TEXT NOT NULL,          -- 'user' | 'profile' | 'ip'
    subject_id   TEXT NOT NULL,
    action       TEXT NOT NULL,          -- 'suspend' | 'ban'
    status       TEXT NOT NULL,          -- 'active' | 'expired' | 'lifted'
    rule         TEXT NOT NULL,          -- which rule fired, or 'admin'
    evidence     JSONB,                  -- snapshot rows + violation history
    created_by   INTEGER,                -- NULL = automatic
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,            -- NULL = indefinite
    lifted_at    TIMESTAMPTZ,
    lifted_by    INTEGER
);
CREATE UNIQUE INDEX ... ON stream_sanctions (subject_kind, subject_id) WHERE status = 'active';
CREATE INDEX ... ON stream_sanctions (expires_at) WHERE status = 'active';
```

One active sanction per subject, enforced by the partial unique index. Lifting sets
`status`/`lifted_*` rather than deleting, preserving audit history. `evidence` is what
makes an automatic sanction reviewable.

**What a suspend must actually do**, because "blocks the user from all streams" is four
distinct behaviours:

1. Deny every future viewer-facing media request for that subject.
2. Fan out cuts to every in-flight session *and* transfer for that subject.
3. Stop correlated remote jobs — otherwise a node keeps encoding for its full idle
   window for a suspended user.
4. Define what an admin lift clears: the sanction always; sticky session cuts and the
   violation counter **also**, so a lifted user is not immediately re-suspended by
   stale state.

`users.enabled` is not a substitute: it blocks the whole account rather than streaming,
and does not stop already-issued proxy stream tokens or ABS public-session
capabilities.

**Cache coherence blocks P1.** The set loads into memory at boot, but Redis pub/sub is
lossy and at-most-once, so a replica that misses a message would deny or allow
indefinitely — unacceptable in both directions for a durable deny cache. The cache
needs a generation counter reconciled against Postgres on a bounded interval, with
pub/sub used only to make the common case fast.

---

## 5. Rules — P1 and later

### 5.1 One assembled input

`Rule = func(EvaluationInput) []Sanction`, where the input is built once per evaluation
pass:

```text
// P1 ships exactly this:
EvaluationInput {
    View     GlobalMonitoringView   // §2.5 — merged, epoch-stamped
    Limits   EffectiveLimits        // cached LimitResolver
}

// P2 adds, for LibraryHarvest only:
    Harvest  HarvestFacts           // §5.3
```

Rules stay pure consumers; every policy, catalog and ledger lookup happens once,
centrally, cached. This is what makes "one place to look" true rather than asserted. A
field with no producer is an invitation to build one badly, so fields appear when their
stores exist, not before.

### 5.2 The abuse surface, and which signal catches it

| Abuse | Signal | Phase | Confidence |
|---|---|---|---|
| Account sharing | session count per `uid` | P1 | **enforced** — measured fact |
| **Fast pull / naive rip** | **delivery rate ÷ media bitrate** | **P1** | **enforced — cheapest signal here** |
| Looping / wasteful re-pull | session bytes ÷ file size | P1 | alert-only, secondary |
| Library harvest / patient rip | distinct titles fully retrieved per window | P2 | alert-only first |
| Re-stream, naive token sharing | distinct viewer IPs per session | P3 | alert-only |
| Sustained volume | bytes per user per window | later | deferred — needs a durable ledger |
| Download flood | — | — | **not covered** — re-downloading one title never grows the distinct set |
| Token mint / hoard / replay | distinct sessions per token | deferred | not covered |
| Low-byte, high-work storms | requests per session per window | deferred | not covered |
| Transcode exhaustion, one user | starts + seconds per window | deferred | not covered |

Deferred rows are work without meaningful bytes, so a byte-oriented engine is the wrong
instrument for them; the §2.2 capture set records what a later probe would need without
building the probe now.

**Two explicit non-goals** — not gaps, not deferred, not tracked:

- **Downstream re-streaming.** A service that pulls once at a normal rate and fans out
  on its own infrastructure is invisible by construction, and is accepted as such.
  (`RestreamFanout` still catches one token used from several addresses, which is a
  different and much dumber attack.)
- **Aggregate transcode load across all users.** Per-node exhaustion control is a node
  admission concern.

### 5.3 Rip detection

**`DeliveryRate` — sustained delivery rate ÷ media bitrate. Ships first.**

A real player pulls at roughly **1× realtime**: it buffers ahead, then throttles to
match playback. A ripper pulls at whatever the link allows — routinely 10–20×. Both
inputs already exist (`MediaFile.Bitrate`, `MediaFile.Duration`, and the byte deltas
the collector already computes), so the rule needs **no storage at all**. It is the
cheapest thing in this design and it catches the single-pass rip.

- **Playback sessions only.** Full-speed delivery is expected for downloads, so
  `Transfer` is exempt.
- **Sustained, not instantaneous.** HLS buffer-ahead legitimately spikes for the first
  minute and after a seek.
- Requires a non-zero bitrate; fall back to `FileSize ÷ Duration`, else skip the
  session.
- **The threshold needs real traffic.** Read the distribution before picking a
  multiplier.

**`OverCap`** — count cap-relevant viewer-egress sessions per `uid` against the
**group-merged effective** limit (`access.EffectivePolicyForUser` → `strictestPositive`,
as the admission closure already does) — never raw `users.max_streams`, which is 0 for
standard users. Victim selection is deterministic, ordered by
`(logicalStartedAtUnixNano, canonicalSessionID)`, as a tie-break *within* an epoch and
not a substitute for election. Limits resolve through a shared cached `LimitResolver`;
on provider failure enforce the cached value and emit a degraded-mode metric. Fail open
only with no trustworthy cached value.

**`LibraryHarvest` — distinct titles fully retrieved per window. Alert-only first.**

**A byte sum is not byte coverage:** serving the first 10% of a file nine times sums to
90% of `FileSize` while revealing 10%. So this needs a real `HarvestProgressStore`
merging **non-overlapping served byte intervals** keyed by
`(user, canonical_item, representation, window)`, with one idempotent completion fact
written when union coverage crosses the threshold.

**It is blocked on a producer that does not exist.** Neither `Observation` nor
`Transfer` carries representation offsets; the direct path derives a single `rangeStart`
only after `ServeContent`, and a multipart range response yields no usable global
`Content-Range` at all. Release facts must carry exact served intervals, and multipart
or unknown-offset responses are excluded from coverage rather than guessed at. Window
semantics and the canonical title/part key are also undefined.

Denominators are representation-specific: `MediaFile.FileSize` for direct play (and it
is nullable — no size, no rule), artifact output size for prepared downloads, converted
`stat.Size` for ebooks. **Remux and transcode are explicitly undetectable by this
rule**, not quietly missed.

Known evasions and false positives, stated rather than discovered later: pulling 89% of
every title is invisible; re-downloading one title forever does not grow the distinct
set; transcoding the whole library is invisible; episodes are distinct items so a
legitimate binge completes dozens weekly; **household profiles share one `user_id`** so
several family members aggregate into one subject; public RSS downloads attribute to
the **feed owner**, so legitimate subscribers can make an owner look like a harvester;
and treating a 30-second extra, one comic chapter and a four-hour film as one "title"
is implausible without media-class-specific policy.

**`SessionOverConsumption`** — session bytes ÷ source file size. Catches only the
*wasteful* abuser who pulls the same file repeatedly inside one session. Kept because
it is nearly free, explicitly demoted to secondary alert-only so it is not mistaken for
rip protection, and applies to playback sessions only.

**`RestreamFanout`** — distinct viewer IPs per canonical session, sustained over a
window, from the bounded per-session IP set. Alert-only by default; stronger action
behind an operator setting. Privacy posture stated explicitly: this retains
short-window per-viewer IP history.

**`AdminTerminate` is not a rule.** It is operator-initiated and immediate — a command
that writes a sticky verdict directly, not something that arrives through the
evaluation loop.

**Transcode over-consumption does not ship.** There is no usable denominator:
`TargetBitrateKbps` is max *video* bitrate and is frequently zero, jellycompat local
transcodes never set it, no job start time or accumulated runtime is stored, wall-clock
is not media time, and segments are re-servable across replans.

### 5.4 The usage ledger, deferred honestly

A per-user byte budget ("500 GB / 30 days") is desirable and not in the shipping set,
because it is harder than one migration and one file: multi-replica double counting
(every replica flushing its own view multiplies the total), publisher epoch and stable
identity across restarts, cumulative vs incremental values and idempotency keys, UTC
bucket boundaries and late deltas, restart reconciliation and retention.

**No interface is frozen in P0.** When the ledger lands it uses an explicitly
idempotent contract — `(publisher_id, publisher_epoch, sequence, subject, metric,
cumulative_value, observed_at)` — with only the publisher that observed the viewer
egress writing usage, a persisted checkpoint, and increments computed transactionally.
A process-local in-memory sink cannot satisfy a multi-replica rule and would invite
callers to depend on semantics the durable version cannot honour.

No existing table could carry it: of the current 76, none stores bytes.

### 5.5 Still out of scope

The jellycompat download quota hole, the per-node concurrent-transcode cap, and
compat/`auth.refresh` rate limiting remain deferred. They are enforcement gaps in
*other* subsystems, not monitoring gaps, and none blocks this design.

---

---

## AI-use disclosure

Written with AI assistance (Claude Opus 5), consolidating the revision history of eight
design revisions, three phase documents and four prior-art documents. The review
findings summarised in section B were produced by Codex `gpt-5.6-sol` at high reasoning
effort and were verified against the cited code before acceptance.
