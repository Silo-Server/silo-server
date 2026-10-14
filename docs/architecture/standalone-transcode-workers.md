# Standalone transcode workers

The `worker` process runs the transcode listener with local scratch storage and
HTTP/HTTPS access to its enrolled core. It receives encoded source bytes and
performs decoding, filtering and encoding on its own hardware. It does not open
the media library or construct PostgreSQL, Redis or object-storage clients.
The existing transcode mode remains available for deployments using shared
storage.

## Identity and enrollment

The core stores a separate Ed25519 signing key, encrypted by its existing
at-rest cipher. A host operator issues a five-minute, single-use enrollment
ticket for an enabled transcode node. The ticket binds both origins, the node
identity, the core public key and the explicit HTTP policy. Redemption installs
the worker's independently generated public key. Its protected local binding
contains its own private key and the core public key, without customer JWT,
database, Redis or object-storage credentials.

HTTP is an explicit option for trusted local links. HTTPS uses certificate and
hostname verification. Requests cannot redirect to another origin, use an
environment proxy or fall back from HTTPS to HTTP. The registered origins are
canonical scheme/host/port origins, without arbitrary URL paths or credentials.
Host case, an explicit default port and a trailing slash do not change the
enrolled origin or its normal request routing.
An enrolled node remains classified as a standalone worker if its stored URL
is later edited into an invalid origin; dispatch rejects it before transmitting
a legacy request containing a source path or client token.

Core and proxy processes classify origins from complete local snapshots,
refreshed once per second. A snapshot's two-second certificate starts while its
database transaction holds the shared configuration lock, before reading rows.
Publication preserves that exact deadline and checks both monotonic and wall
time. Warm legacy segment requests therefore need no classification SQL.
An expired snapshot requires a bounded, coalesced database refresh; a failed
refresh cannot permit legacy fallback.

Database triggers take the exclusive configuration lock and drain older
certificates for three seconds before a real enrollment, enrolled-node URL or
type change, or membership removal can commit. The drain runs once per
transaction; health and unchanged writes do not drain. New admissions may wait
or fail closed during that transition. After commit, a new classification cannot
use an earlier negative snapshot. Requests admitted before the change may
complete, including long response bodies. Current proof and work-authority
checks continue reading node identity by primary key.

An append-only database history retains previously enrolled raw URLs across URL
changes, type changes, membership removal and node deletion. Every snapshot
canonicalizes those origins too. A retired origin remains denied even after a
process restart; it cannot become eligible for legacy requests carrying source
paths or client JWTs. Ordinary history removal and registry truncation are
refused. The same database fence covers direct SQL changes and does not depend
on Redis invalidation delivery.

Core commands, worker control calls and source reads use separate signature
domains. Signatures bind the destination, method, escaped path, query, body
digest, timestamp and nonce, plus byte-range, source identity and output scope
headers. Each verifier bounds its replay cache; the core shares worker-request
replay protection across its API replicas. Verification also requires the request
Host to match the signed public audience; a reverse proxy that rewrites Host
invalidates the worker proof.

## Source admission and execution

The core intercepts requests to an enrolled standalone node and replaces source
paths with opaque descriptors before dispatching `/worker/execute`. Only current
catalog files in enabled media folders and approved theme files can become
sources. A descriptor binds a node, work identity, generation, immutable source
revision, size and strong ETag. Arbitrary URLs and `.strm` sources are rejected.

The worker adapter presents each admitted source to FFmpeg through a private
loopback HTTP URL. Its pooled HTTP client supplies the worker signature to the
core. Worker signing keys and core filesystem paths do not enter FFmpeg
arguments. The local URL contains a bearer handle and is visible to processes
permitted to inspect FFmpeg arguments.
FFmpeg and FFprobe inputs must be an admitted handle or a regular file under
the worker's scratch root. Remote playlist/concat demuxers and unregistered
network inputs are refused. Internally generated sampling concat members pass
the same admission check.

Core source delivery supports HEAD, conditional reads and single byte ranges.
Every read checks its grant, strong file identity and continuing authority.
The adapter checks status, ETag, size and returned range before forwarding
bytes. File replacement, truncated responses, stale grants and cancellation
fail the operation. Streaming uses fixed 64 KiB buffers; it does not stage the
whole source file or scale memory with source size.
An authorized reader can pause without losing its stream. A bounded authority
watcher renews the write deadline while access remains valid and interrupts a
blocked write when access is revoked. Adapter cancellation also interrupts
blocked local writes, releases reader slots and joins its callback before the
handler returns.

Native Matroska keyframe indexing also reads admitted HTTP ranges. Its cache
and saved copy-playback plans bind the stable source identity and revision;
both check current authority before reuse. Background verification keeps the
same handle lifetime and never reopens an HTTP URL as a filesystem path.
Four index readers share source-read admission. Each uses a 2 MiB cache,
64 KiB read-ahead and ranges of at most 1 MiB by default. A check has a
two-minute deadline and fetches at most twice the source size, capped at
64 GiB and one million requests. Network, cancellation and budget failures
remain retryable and do not cache a permanent missing index. A first start can
keep FFmpeg's normal playlist when the index is unsupported or exceeds these
limits. A cold reconstruction of an existing full VOD plan fails retryably on
transient range, index or reader-limit errors, so it cannot publish a changed
playlist or record a fallback decision. Unsupported indexes retain the legacy
fallback. Short fixture parity does not establish indexed-copy performance for
every source.

The default source grant lasts 120 seconds. Workers renew active handles every
45 seconds, using at most eight concurrent requests with two-second deadlines.
Renewal must return the same complete descriptor. A failed renewal fences that
generation and stops its producers without cancelling a newer replacement.
Source and reader quotas, cancellation and expiry bound resources at both ends.

Selected embedded text subtitles may be extracted to bounded local ASS files
to preserve the existing burn-in and seek behavior. Embedded fonts are extracted
with generated names and size/count limits. These operations read the admitted
source; they do not require library mounts or stage the media file. Existing
client subtitle/font delivery stays on the core's HTTP routes.

## Generation and authority

The core keeps live and pending generations separately in shared Redis.
Dispatch acquires short durable admission leases; a candidate becomes live only
after the expected worker response. A failed candidate preserves the previous
live authority. Frozen core-only input bindings prevent later recipe edits from
retargeting an existing generation to different bytes.
Bindings remain available for the lifetime of their live or pending generation,
including ready artifacts retained beyond one day. Replacing, cancelling or
stopping that generation removes its bindings atomically with the authority
record.

The worker refreshes signed authority snapshots once per second. Their short
leases fail closed if the core is unreachable. The core re-resolves current
account, profile, library, content and operation permissions before extending
authority. A durable recipe or a still-valid client token does not override a
disabled account, deleted profile, disabled folder or stopped execution.
Prepared jobs additionally pin the current durable attempt and lease owner;
reusing the same owner string cannot authorize a previous retry.

Stopping work installs a durable core fence. Signed generation cutoffs prevent a
delayed stop from removing a successor. Worker source handles and producers are
closed by the exact affected generation. Reconstruction obtains the latest
core-authorized recipe and fresh source grants; it cannot reconstruct from
token-carried filesystem paths alone.
Enrolled standalone HLS workers require a stored core recipe for both client
authentication modes. The core classifies the selected node before dispatch and
persists the accepted recipe before publishing a client or proxy route. A failed
recipe write rolls back the accepted work. Existing legacy token-based workers
retain their token-carried reconstruction behavior.
Core recipe publications also have independent opaque versions. Teardown captures
the version before stopping work and removes only that publication. Publishing
the same recipe again still creates a new version, so delayed cleanup cannot
erase a successor's reconstruction recipe.

## Output delivery

Trusted proxies retain their existing client JWT validation. They obtain a
core-signed output grant containing a public verification key for one worker,
work identity and generation. The proxy derives the matching private key from
its existing secret with a separate domain label. The worker receives only the
public key and bounded output claims, not the customer secret or the original
client JWT.
The standalone verifier also rejects raw customer tokens without forwarding
them to the core. Its engine accepts only the opaque, core-signed output grant.

Core media relays use the same viewer grants, including tokenless UUID and
header-authenticated playback URLs. The core recovers the original credential
from the current recipe and reuses a stable internal token across segments;
long-lived plays renew without replacing their originating credential. Prepared
download relays bind the current caller to one unambiguous artifact row and reuse
tokens only for the same credential, claims and work generation. These private
tokens do not appear in public URLs. Only the preparer's marked, exact artifact
HEAD probe retains core maintenance authority.

Proxies sign each output request, including range and segment acknowledgement
headers. The worker verifies it locally against the grant and its current
authority snapshot. Grants are cached for at most five minutes, bounded by the
client token lifetime; normal segment delivery needs no per-segment core RPC.
Secret rotation changes the signed authorization epoch and invalidates worker
and proxy grant caches. Output is refused before the first signed configuration
snapshot establishes that epoch.

Prepared downloads keep their bytes and receipts on the worker. The core verifies
the opaque source execution fingerprint and translates its receipt to the
existing client fingerprint. Proxies validate that bridge before exposing bytes.
A successful preparation releases source handles while retaining authorized
artifact output; reading a ready artifact does not require the original source
to remain present.

Every delegated output, including ordinary playback, has an independent viewer
identity. Native and compatibility playback capture the originating login session
or API-key ID when admitted and preserve it through recipes, reconstruction and
replans. The standalone boundary refuses old tokens without that binding; legacy
transports retain their existing token behavior. Source credential identities are
removed from worker claims and recipe replies.

The core retains each viewer's original token in a bounded output-authority
registry. Each short snapshot rechecks the owning login's revocation, expiry and
remote-playback profile limits, or the owning API key's existence and current
scopes, alongside current content permission. Workers receive only opaque allowed
authority IDs. A revoked login loses cached grants and open responses without
stopping another login on the same account or a shared artifact. Output writes
check work generation, authorization epoch, viewer authority and customer-token
expiry. The five-minute grant lifetime governs admission; it does not truncate
an otherwise authorized long download.

A refresh captures viewer records before one shared assignment snapshot, then
batches exact credential ownership checks and reuses content decisions within
that request. Distinct logins retain independent revocation and expiry checks.
A failed dependency returns 503 without signing a partial allowlist or deleting
records based on an incomplete check. The two-second snapshot lease is unchanged.

Snapshot refreshes reclaim records only after an authoritative assignment lookup
confirms stopped or superseded work, or credential verification confirms a revoked,
expired or invalid source credential. Content and store errors withdraw snapshot
permission while retaining the record for recovery. Cleanup compares the complete
observed record and its registration revision, so an older core cannot delete a
concurrent renewal. A full registry performs one bounded sweep before refusing a
new viewer; active records remain subject to the record and byte limits.

## Configuration, capacity and observations

Signed configuration contains only segment retention, subtitle sampling capacity,
drain state, a revision and the output authorization epoch. The worker's hardware,
scratch paths and job ceiling stay operator-owned. The ceiling accounts for
producer and probe lifetimes, including shared probes that outlive a cancelled
caller. Source reads do not consume compute slots.

An admitted playback carries a server-owned `job_id` in its signed assignment and
output authority. Healthy and replacement transports share that logical playback
slot while retaining distinct work IDs. Native replans hold both the local session
lock and the shared database session lock through preparation and commit, bounding
that overlap to one replacement candidate. Health can briefly report two active transports
at a one-job limit; after commit the prior transport stops. A failed candidate
leaves the healthy producer running. The slot is released after the last transport and its retained probes finish.
Unrelated sessions, downloads and probes retain separate accounting identities.

The accounting ID grants no source, output or stop authority: those still require
the exact work ID and generation. Only verified core metadata selects a playback
group; request bodies and headers cannot select it. Separate namespaces distinguish
playback groups from fallback work identities. Existing assignments without the
field retain their original fallback identity. Memory and Redis stores preserve
each live work's accounting identity atomically through admission, completion and
source recovery, including an originally empty field. Signed output metadata lets
cold reconstruction retain the same group without another lookup or worker cache.

Capability builders waiting on another builder hold no compute permit. A full
worker may reuse its last complete capability report only for the same FFmpeg
identity, acceleration backend and device. With spare capacity it rebuilds the
report normally. An accepted reprobe invalidates that fallback until a new report
completes successfully. Cold probes and independent encoder jobs remain subject to the
worker's compute limit.

Draining refuses new work and permits the traffic of existing sessions. Health
advertises `worker_protocol: http_only_v1`; capability warmup, snapshots and metrics
use the existing transcode runtime hooks. Worker session and telemetry reports
travel over signed HTTP. The core accepts only current work/generation scopes,
supplies trusted ownership labels and sanitizes bounded log batches. The worker
uses local telemetry storage rather than Redis. When old and replacement work
report the same playback session, the core selects the transport recorded by the
committed playback attempt. An unavailable or ambiguous attempt omits that
session without rejecting unrelated sessions in the report.

The public route inventory and the worker protocol extension describe the
standalone execution and core control surfaces alongside the retained worker
operations. Operator setup documentation belongs in the separate user manual.

## Measured transport cost

Direct FFmpeg runs on a LAN added a median of about 6 ms (HTTP) and 12 ms
(verified HTTPS) to producer readiness at startup and 10 to 16 ms to a fresh
seek, and kept 97 to 99 percent of local-file throughput. A full-runtime pilot
against legacy shared-storage playback added a median of about 300 ms to
startup and 370 to 410 ms to seek, with gateways that closed each connection
after its response. These figures do not cover WAN links, cold caches, p95
latency or sustained capacity. The measurement reports are linked from pull
request #2408.
