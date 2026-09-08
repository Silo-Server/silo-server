# Initial playback runtime

The v2 playback runtime is assembled by default in API, integrated, proxy, and
transcode modes. Account admission remains durable state checked per request;
there is no environment-variable activation switch or startup account list.

Enabling requires PostgreSQL, Redis, a signing key, the final configured user
store's captured-source interface and the application shutdown registry. Startup
checks PostgreSQL and Redis connectivity and validates the persisted server
installation identity within a ten-second startup budget. It fails startup when
those requirements are unmet. Runtime owner identity is per boot; installation
identity is the persisted identity also used by progress bootstrap.

## Admission and transport limits

Startup does not enroll accounts. Authenticated capability discovery and first
start establish a missing PostgreSQL source through the retained
[first-admission protocol](playback-first-admission.md). Registration state,
source generation and the admission UUID remain distributed authority fences.
Blocked, retiring or conflicting state refuses automatic admission.

The runtime applies to the shared playback handler. Old attempt replay and bridge
client coexistence retain their existing source gates; automatic first admission
does not authorize stale workers or unbound legacy writes.

Supported initial execution is direct delivery through API or proxy egress,
video HLS with API execution and API egress, and video HLS with selected
worker execution through API or proxy egress. HLS supports encoding and the
versioned copy-fMP4 remux recipe, including captured seek successors. Progressive
remux and audio-only execution require separate producer integration. Source installation precedes
non-launch preparation on the selected executor. The API publishes the immutable
recipe before sending exactly one remote start, then completes durable activation
before exposing the session. An uncertain start cannot fall back, retry execution,
or issue a legacy DELETE. Repeating the original request cannot launch again.

Clients opting into `header_authenticated_media_v1` receive credential-free API
media and auxiliary URLs; `authorized_media_origins_v1` additionally permits the
selected proxy origin. The plan carries the nonsecret `X-Profile-Id` selector;
clients attach their originally captured bearer/profile authority in memory.
Initial playback and seek successors preserve this negotiated mode and selected
origin. Session-addressed URLs resolve the current immutable executor recipe and
hold the same serving grants; missing bound state never falls back to a legacy
grant or reconstructs execution. Non-opt-in primary media URLs remain signed.
This negotiation does not advertise additional control operations.

The final egress holds `serve` authority through response completion. Worker
output requires a separate egress-opened `output_transfer` permit; it does not
grant the worker client egress authority. Standalone nodes use the same durable
route and immutable recipe and never reconstruct a missing bound runtime.

The API runtime configures the trusted catalog resolver for bound audiobook
timelines. Discovery and start share its complete catalog manifest rules; progress
and stop retain the selected part mapping, and a new part waits for the previous
part terminal receipt. See [bound client timelines](playback-client-timeline.md).

The planner excludes progressive remux when this runtime is active, even when a
client advertises it. Audio adaptation can select bound HLS remux instead. If no
supported executable delivery remains, planning returns a terminal refusal before
transport allocation. Client capabilities and the retained request stay unchanged.

## Timing and shutdown

The bounded testing policies match the isolated playback harness:

| Supervisor | Maximum duration | Safety margin | Renew before | Poll interval |
| --- | --- | --- | --- | --- |
| Owner | 30 seconds | 1 second | 10 seconds | 10 milliseconds |
| Executor grant | 1 second | 100 milliseconds | 200 milliseconds | 10 milliseconds |

These are testing defaults, not production sizing. Polling is more frequent than
both the safety margin and renewal window; the executor grant is shorter than
the owner budget. Clock and renewal failures revoke local authority. Do not
infer that a deployment's latency, clock behavior or scale meets these budgets
from successful dependency assembly.

Application cancellation fences new initial starts and grants, cancels their
work and joins owner/grant supervisors, output-transfer permit cleanup and
retained-owner cleanup. Standalone nodes cancel initial work before HTTP shutdown. Reconciliation
is registered with the same shutdown completion registry as transcode cleanup.
Each reconciliation tick visits at most one page of 100 intents per configured
account. It does not adopt active owners or invent final stop positions. Missing
source receipts leave uncertain intents durable for later reconciliation.

Shutdown waits under the application's existing graceful-shutdown deadline.
Cancellation is lease loss, not a fabricated stop receipt. If a dependency fails
to honor cancellation and cleanup exceeds the deadline, the application reports
that cleanup did not finish; it must not report a clean terminal receipt.

## Failed starts and recovery

Each configured runtime reconciles retained work across all accounts in bounded
pages. Multiple replicas use the existing compare-and-set transitions and exact
source receipts; the runner needs no account list and joins application shutdown.
A slow source cannot keep the scan from reaching later attempts. When idle cleanup
removes a local session, it closes that session's exact retained owner so the lease
can expire and recovery can finish; an absent player never keeps an owner alive
indefinitely.

A server-owned startup abort waits briefly for its source receipt and database
drain deadline, then returns the retained terminal decision when complete.
Otherwise reconciliation finishes it independently of the browser. Workers get
the same 30-second manifest readiness budget as local execution.

The web client retries the exact saved START automatically. If a previous response
never reached the player, recovery closes that captured session before another
Play. Once the durable session mapping exists, recovery resumes its exact STOP
directly, including after a lost response or reload. The original START remains
saved until cleanup completes. Identity changes quarantine the old recovery task.
