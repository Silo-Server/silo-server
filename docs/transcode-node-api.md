# Transcode node throttle contract

The API server resolves `enable_transcode_throttle` and
`transcode_throttle_seconds` before starting remote HLS playback. The
`POST /transcode/start` request carries `throttle_seconds`: zero disables
throttling; a positive value sets the forward buffer in seconds. Configured
positive values below 60 seconds are clamped to 60, and invalid configured
values use the 300-second default. Negative request values return HTTP 400.

The start response echoes `throttle_seconds` after arming the throttler.
When throttling is enabled, the API rejects a missing or mismatched echo and
stops the rejected remote transport. Disabled requests remain compatible with
nodes that omit this field. Throttling is on unless an administrator turns it
off, so update transcode nodes before the API servers that require this
attestation.

Recipe cards and signed reconstruction claims preserve the resolved threshold.
Remote reconstruction and FFmpeg restarts re-arm the same policy. Local native
and Jellyfin compatibility playback share the settings resolver. Playback
expiration closes owned transports; process shutdown drains local FFmpeg
sessions, cancels and waits for active progressive remux requests, and refuses
new HLS or progressive-remux admission after the drain begins.

Copy-video playlists retain actual FFmpeg fragment durations. A complete
playlist inferred from source keyframes is not safe: restarting the HLS muxer
can change its cut schedule, giving an existing segment URL different content.
Clients seeking outside the produced window must negotiate a new playback
transport at the requested source position.

The throttle fields belong to the internal API-to-node contract. Apple and
Android clients require no request or response changes.

The start response also reports `hw_accel`, the backend retained for video
decoding and tone mapping, and optional `encoder_hw_accel`, the actual video
encoder backend. When a GPU can tone-map but cannot encode HEVC, `hw_accel`
keeps that GPU backend while `encoder_hw_accel` is `none` for libx265 encoding.
The same holds when a VAAPI device tone-maps a capped encode but offers neither
VBR nor CBR rate control; libx264 or libx265 then encodes.
Activity reporting uses `encoder_hw_accel`, falling back to `hw_accel` for
older nodes that omit it. Stored recipe cards preserve both values; execution
validates the encoder again when reconstructing a session.

## Native worker retirement fencing

`GET /api/v2/admin/nodes/capabilities` advertises `worker_drain`. This capability
describes the API server; a configured worker must separately answer the private
drain protocol. Unsupported or unreachable workers cannot produce a retirement
receipt. This feature adds no v1 business route or client playback requirement.

The same endpoint advertises `disabled_creation`. Native v2 node creation
accepts optional `enabled`, defaulting to true. A controller commissions with
`enabled=false`, confirms the exact worker's identity and readiness, then uses
a separate guarded configuration update to enable placement. Natural-URL
create retries must match the stored enabled state; a delayed disabled create
cannot supersede activation. A late request after deletion may create another
disabled row, so controllers retain reconciliation authority after ambiguous
creation outcomes. Frozen v1 creation keeps its enabled=true behavior.

`PUT /api/v2/admin/nodes/{id}/drain` requires the node's original configuration
`If-Match`. One PostgreSQL transaction disables placement and records a unique
admission fence. The API then asks the worker to confirm it. A failed reply can
follow a committed fence: reconcile the node configuration and the fresh
`GET /api/v2/admin/nodes/{id}/drain` result before another explicit write.

Both private listeners expose bearer-protected `GET` and `POST /admin/drain`.
The worker resolves its stable registration and reads the durable fence when
admitting execution or media requests. Ordinary `enabled=false` continues to
remove placement and routine health sampling without fencing worker admission.
Retirement fences reject unknown session and transport identities, background
extracts, capability probes, and reconstruction. Work admitted before the fence
continues; retirement does not stop an encoder or transfer.

The fresh status contains `node_id`, `fence_id`, `worker_instance_id`, `native_server_id`, `fenced`,
`drained`, `active_jobs`, `active_requests`, `active_reservations`, and
`observed_at`. The API adds `config_etag`, and verifies that the configured URL,
revision, and fence have not changed across the worker request. All responses
carry `Cache-Control: no-store`. Execution includes GPU admission and teardown
holds, detached probes, source preflights, capability listings, copy-seek keyframe
work, and subtitle-cache fills through child exit and publication/discard cleanup.
These cover the interval before job registration and after the ordinary job count
drops. Request reservations cover in-flight
execution and egress; bounded session permits cover gaps between media requests.
Permits remain until signed expiry, a positive session-deny marker, or confirmed
transport teardown. Header-authenticated media and internal transport permits
use the maximum token lifetime, currently 24 hours. Absence from Redis and quiet
bandwidth are never proof that a permit ended.

`native_server_id` is the read-only `server.identity_id` in the worker's shared
database, matching native server discovery. The worker never initializes a
missing realm. Missing registration or realm prevents private confirmation;
a changed realm prevents another receipt from the same process. The API checks
the worker realm against its own database snapshot, requires every proof field,
and rejects observations older than the request with a 30-second clock-skew
allowance. Worker-only public `/api/v1/health` also carries `native_server_id`
and `worker_instance_id` after initial authority resolution. Public health uses
the last verified binding and is not retirement authority. Controllers compare
it with the authenticated, fresh private receipt from the exact owned process;
public identity alone authorizes nothing. The private control bearer is the
configured runtime JWT signing secret, not the at-rest settings encryption key.

A temporary PostgreSQL failure refuses unknown identities, permit renewal, and
retirement observations. An admitted, unsealed media identity may continue only
until its original permit expiry; an outage cannot extend that permit or reopen
a sealed worker. Ordinary token playback therefore keeps its bounded continuity
without treating unavailable database authority as a drain acknowledgement.

Only zero execution, request, and permit reservations sets `drained=true`.
That observation seals admission under the same worker mutex, so a late dispatch
or a resumed client token cannot invalidate the zero. A worker restart retains
the database fence and refuses reconstruction from the preceding process.

The receipt applies to the addressed worker instance, not to unregistered or
orphan processes. A controller must correlate that instance with the exact
container/process it will stop. An unexpected instance change or URL replacement
requires process-lifecycle evidence before retirement; a zero from a replacement
worker cannot establish that an older process has exited.

Controllers transfer retirement custody by deleting the exact registration with
`DELETE /api/v2/admin/nodes/{id}` and the fresh receipt's configuration validator
before stopping its owned process. A confirmed 204, exact registration absence,
and a durable record of that acknowledged deletion allow process cleanup. An
uncertain deletion outcome requires retaining the process; absence alone cannot
establish that the controller deleted a still-fenced registration. Cancellation
before guarded deletion changes the validator and refuses deletion. After
deletion, the old process refuses admissions and private proof even if a new
registration reuses its URL and configuration is reloaded. Its gate cannot adopt
another registration or server realm.

`DELETE /api/v2/admin/nodes/{id}/drain` is an explicit guarded cancellation and
returns 204. It invalidates the previous configuration validator and leaves
placement disabled. Read a fresh validator before a separate guarded reenable.
Enabling a node or changing its URL, type, or name while its fence exists returns
409; this restriction also covers compatibility and direct configuration writers.
The worker synchronizes an
explicit cancellation on its next admission. Deleting the node cascades fence
cleanup; rolling back the migration refuses while any retirement fence remains.
Successful rollback preserves the last effective node validator in the older
configuration column, so cancellation followed by rollback cannot resurrect a
pre-fence `If-Match`. The configuration trigger resumes before the transaction
commits; a rollback fault restores the original revisions and trigger state.
