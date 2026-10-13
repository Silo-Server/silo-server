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

Core commands, worker control calls and source reads use separate signature
domains. Signatures bind the destination, method, escaped path, query, body
digest, timestamp and nonce, plus byte-range, source identity and output scope
headers. Each verifier bounds its replay cache; the core shares worker-request
replay protection across its API replicas.

## Source admission and execution

The core intercepts requests to an enrolled standalone node and replaces source
paths with opaque descriptors before dispatching `/worker/execute`. Only current
catalog files in enabled media folders and approved theme files can become
sources. A descriptor binds a node, work identity, generation, immutable source
revision, size and strong ETag. Arbitrary URLs and `.strm` sources are rejected.

The worker adapter presents each admitted source to FFmpeg through a private
loopback HTTP URL. Its pooled HTTP client supplies the worker signature to the
core; credentials and core filesystem paths never enter FFmpeg arguments.
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

Ready artifacts can serve several viewers. The core retains each viewer's
original token in a bounded output-authority registry and checks current access
when refreshing the worker's short snapshot lease. Workers receive only opaque
allowed authority IDs. Revoking one viewer denies their cached grant and stops
their active response without stopping the shared artifact or another viewer.
The response checks work generation, authorization epoch, viewer authority and
the original token expiry while writing. The five-minute grant lifetime governs
admission; it does not truncate an otherwise authorized long download.

## Configuration, capacity and observations

Signed configuration contains only segment retention, subtitle sampling capacity,
drain state, a revision and the output authorization epoch. The worker's hardware,
scratch paths and job ceiling stay operator-owned. The ceiling counts actual
encoding and probe lifetimes, including shared probes that outlive a cancelled
caller. Healthy and replacement generations of one work identity share one job
slot. Source reads do not consume compute slots.

Draining refuses new work and permits the traffic of existing sessions. Health
advertises `worker_protocol: http_only_v1`; capability warmup, snapshots and metrics
use the existing transcode runtime hooks. Worker session and telemetry reports
travel over signed HTTP. The core accepts only current work/generation scopes,
supplies trusted ownership labels and sanitizes bounded log batches. The worker
uses local telemetry storage rather than Redis.

The public route inventory and the worker protocol extension describe the
standalone execution and core control surfaces alongside the retained worker
operations. [Source transport measurements](transcode-source-transport-evidence.md)
describe the accepted direct-FFmpeg timing evidence and its limits. Operator
setup documentation belongs in the separate user manual.
