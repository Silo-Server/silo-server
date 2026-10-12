# Transcode node capacity and throttle contract

## First-connect customer configuration handoff

The core operator must set `SILO_NODE_ENROLLMENT_ENABLED=true` before using
automatic handoff. It is disabled by default: acting admins receive HTTP 503
without an outbound request when the operator has not allowed sending the core's
bootstrap secrets to worker URLs. This switch is owned by the process environment,
cannot be enabled through application settings, and does not affect ordinary
registration of workers that already have customer configuration.

Enrollment is opt-in for `MODE=transcode`. With `SILO_NODE_ENROLLMENT_TOKEN`
(32–512 characters) and a persistent `SILO_NODE_BINDING_FILE`, a worker may start
without `DATABASE_URL`, `REDIS_URL` or `SECRET_KEY`. The binding path defaults to
`/var/lib/silo/node-binding/binding.json` when only the token is supplied.
Legacy workers and maintenance commands still require their existing bootstrap
credentials. Other server modes cannot enroll.
Use a separate worker environment file without the customer variables: a shared
core `.env` supplying `SECRET_KEY` prevents credential-free worker startup.

Before the database-backed runtime starts, its separate listener exposes
`GET /api/v1/health` with `status: "unbound"` and the provider's encoding capacity,
and `POST /api/v1/node/enroll` authenticated by the provider's one-time bearer
token. HTTP is supported for trusted internal networks; HTTPS is optional and
normal certificate verification applies. The core never follows enrollment
redirects.

The native acting-admin, demo-restricted `POST /api/v2/admin/nodes/enroll`
accepts a transcode node's registration fields, `enrollment_token`, and optional
`worker_database_url` / `worker_redis_url` overrides for endpoints reachable by
the worker. The core supplies its bootstrap database URL, optional Redis URL and
shared encryption key. The browser receives only the ordinary node registration
receipt and configuration ETag. Neither the token nor customer credentials are
stored in the node registry or returned in responses. Configuration-only
`POST /api/v2/admin/nodes` keeps its existing behavior.

The worker validates encrypted customer settings and effective Redis access,
then saves an immutable binding containing the connection credentials and node
identity. The directory must be private (0700), the file is 0600, and publication
is atomic and never overwrites a prior binding. Failed validation, including
missing Redis required by transcode mode, leaves the token usable. A
post-publication directory-sync failure retains the winner and lets the matching
handoff retry durability confirmation; competing bindings are refused. Saved
restarts confirm directory durability before starting. Successful binding retires
admission and starts the existing worker
runtime. The provider encoding ceiling remains in operator bootstrap and cannot
be supplied in the binding payload or changed by customer settings.

The bound worker exposes bearer-authenticated `GET /api/v1/node/enrollment` with
`binding_id` (an HMAC of a fixed protocol label under the shared encryption key)
and `encoding_capacity`. The core requires that receipt under the customer's
worker bearer before registering the node, and clamps initial `max_jobs` to a
positive provider ceiling. Exact retries recover lost replies, startup delays and
failed registry writes through the same authenticated receipt and natural URL
registration. A 503 can follow a saved binding; retry the same enrollment request.
A worker bound to another customer returns a conflict without another handoff.

Restarts load the private saved binding before opening the normal worker
listener. Reassignment requires a provider reset while the worker is stopped,
including clearing the binding and prior customer artifacts and issuing a new
token. Removing a core registry row does not release or reset the worker binding.

## Operator-owned encoding capacity

Dedicated workers accept `SILO_TRANSCODE_MAX_JOBS` in their startup environment
or bootstrap `.env` file. Unset or `0` means unlimited, preserving existing
installations. A positive integer caps all concurrent FFmpeg media processes:
playback, seek/reconstruction, prepared downloads, audio/subtitle extraction,
thumbnails, trickplay, analysis, and hardware smoke encodes. Discovery commands
and ffprobe are excluded. Values outside `0`–`2147483647` fail startup. This setting
applies only to transcode mode and cannot be changed by database configuration
reloads. The operator must retain control of the worker's environment/container.

Admission is atomic before process launch. A slot remains occupied until the
process's owner reaps it, including cancellation and teardown; failed launches
release immediately. Completed playback encodes release capacity while the
session and produced segments can remain available. Serving existing output
does not consume a slot. Restart and fallback attempts must acquire anew and
cannot exceed the ceiling. Saturated work returns HTTP 503 with a retry signal;
capacity exhaustion does not establish unsupported hardware or codecs.

Health and authenticated status add `encoding_capacity`, for example:

```json
{"max_jobs":4,"active_jobs":2,"available_jobs":2}
```

Unlimited workers report `max_jobs: 0` and `available_jobs: null`. The core keeps
this report in the node's opaque `last_stats.encoding_capacity` sample and shows
the operator ceiling and remaining slots read-only in Admin > Nodes. Placement
honors both this ceiling and the customer limit, including provisional starts
not yet represented in the latest health sample.

When a healthy worker reports a positive ceiling, the core lowers its stored
`max_jobs` if that value is higher or unlimited. Customer edits are also clamped
to the last reported ceiling; lower limits are preserved. Reconciliation uses
the existing configuration revision/invalidation mechanism, and subsequent
unchanged reports do not change the revision. URL changes discard the previous
worker's report. Unlimited workers and older workers reporting no ceiling never
change an existing configuration. The worker's own admission remains the
authority even if the core is older, has stale health data or is modified.

These are worker and administration changes. Native Apple/Android clients need
no new fields or UI; Jellyfin playback uses the same admitted worker processes.

## Playback throttling

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
