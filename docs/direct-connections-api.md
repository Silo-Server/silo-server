# Direct Connections API

`GET /api/v2/admin/direct-connections/status` requires an acting administrator.
Operation ID: `getAdminDirectConnectionsStatus`.
The hidden setting `direct_connections.enabled` gates readiness probes and the
API host's direct engine. The engine also requires a valid UDP port and permission
from the replica gate. Status reads report that lifecycle; refreshes rerun probes.
`GET /api/v2/admin/direct-connections/capabilities` (operation ID
`getAdminDirectConnectionsCapabilities`) requires the same acting administrator.
Its common head contains `revision`, `state`, and `allowed`; the typed `status`
and `refresh` fields report support for readiness reads and explicit refreshes.
The state is `available` when the service is wired and `not_configured` otherwise.
It does not run probes or depend on the hidden setting or current probe health.
Responses use `Cache-Control: private, no-cache`, an ETag, and conditional reads.

The optional boolean query `refresh` defaults to false. A normal read returns
the last completed snapshot, waiting for an initial check if needed. A refresh
starts one check or joins the check already in flight. Each check has a fifteen-
second timeout, and the settings read that starts a check has its own fifteen-second
timeout, so a request that must read settings and start a check can take about
thirty seconds. Canceling the request stops its wait, not the shared check.
A missing service, or a settings read that fails or runs out of time, answers
`503 dependency_unavailable` with `Retry-After`.

A successful response contains `enabled` and a `hosts` array with the API host
only. Each host has `host` (the existing `NetworkAccessHostRef`, ID `api`),
`checked_at` (an Instant), `features`, `knobs`, and, after binding, `listeners`.
Each API process keeps its own result. With several API replicas behind a load
balancer, the replica that answers reports its own process under the same `api`
host, so consecutive reads can come from different replicas. The response does
not yet identify the replica.
Generated [OpenAPI](../contracts/api/v2/openapi.json) defines the exact schemas
and additive feature, state, and reason vocabularies.

Each feature contains `feature`, `kind` (`local` or `network`), `state`, and
optional `reason`, `requested`, `granted`, and `detail`. Byte counts in feature
rows are decimal strings; Linux socket-buffer grants report the usable size,
half the kernel's doubled readback (socket(7)).
Disabled network rows are `off` with reason `disabled`, with no network probes.
When enabled but router discovery is disabled, `port_mapping` is `off` with
reason `admin_off`. Probe errors and timeouts produce `degraded/probe_failed`
for the affected observations. A probe that gets no STUN reply leaves
`nat_mapping`, `ipv4`, and `ipv6` at `off/probe_failed`. When a NAT probe
succeeds but a family has no available address, its `ipv4` or `ipv6` row is
`off/not_available`.
Hairpin and mapped-address reachability remain
`unknown/not_measured`; reachability includes a mapped address when observed.
On a non-Linux platform the probe's local rows, including `direct`, are
`off/unsupported_platform`. The `engine` row is added by the engine lifecycle and
does not follow that rule.

The `engine` feature has kind `local`. It is `on` while the engine runs and `off`
otherwise. Its reasons include `disabled`, `udp_port_invalid`,
`udp_port_bind_failed`, and `engine_start_failed`. `udp_port_bind_failed` means the
engine could not bind the requested UDP port. `engine_start_failed` means the engine stopped or could not start
for any other reason: its construction failed, or the HTTP/3 server stopped while the
engine ran. After the HTTP/3 server stops, the engine restarts once, after a settings
read and, with Redis, a successful lease renewal or acquisition that both completed
after the stop; the next such success permits the restart, and a failed read or
renewal keeps it pending. Without Redis the read alone permits it. After a second
stop the engine stays off until the settings or the replica gate change. Both
`engine` and `direct` can report these replica reasons:

| Reason | Meaning |
|---|---|
| `multiple_api_replicas` | The census sees more than one API process. |
| `replica_census_unavailable` | The census fails, Redis is configured but this process has no usable client or presence identity, or `server.mode` is `api` and Redis is not configured. |
| `replica_lease_pending` | No lease is held locally, acquisition is pending or unavailable, or a fresh lease is inside its 90-second hold. |
| `replica_lease_held` | Another process holds the lease. |

The `direct` row, when otherwise `on`, is `off` whenever new direct connections
are unavailable: the replica gate says so, or the engine is stopped with
`udp_port_invalid`, `udp_port_bind_failed`, or `engine_start_failed`, and the row
carries that reason. An `on` `engine` row can carry
`multiple_api_replicas` or `replica_census_unavailable`: a holder keeps serving
established sessions while its last successful renewal is less than 60 seconds
old. Engine state and replica permission are read live; `checked_at` identifies
the probe snapshot. See the [gate table](architecture/direct-connections.md#replica-census-and-lease).

Listener rows carry `listener`, `requested`, `granted`, `state`, `reason`, optional
`errno`, and `send_buffer`. The nested send-buffer requested and granted values
are integers; the grant uses the same usable-byte convention as feature rows.
Knob rows carry `env`, `default`, `effective`, optional `allowed`, `min`, and `max`,
and optional `requested` and `reason` when startup parsing adjusts a request.
The bounds are decimal strings for numeric knobs, which omit `allowed`.
`internal/tuning/tuning.go` defines these numeric rows:

| Knob | Default | `min` | `max` | Meaning |
|---|---|---|---|---|
| `SILO_NET_TCP_SEND_BUFFER` | `33554432` | `1` | `1073741823` | TCP send-buffer request in bytes; `off` disables the request. |
| `SILO_NET_DIRECT_UDP_PORT` | `8094` | `1` | `65535` | Fixed engine UDP port; invalid input becomes effective `0` and prevents binding. |
| `SILO_NET_DIRECT_RECEIVE_WINDOW` | `128` | `4` | `128` | Client maximum stream receive window in MiB; it does not affect downloads or playback speed. |

Receive-window values outside the bounds clamp with reason `clamped`. Malformed
receive windows use the default with reason `invalid_default`; invalid UDP ports use
`0` with reason `invalid`. `SILO_NET_DIRECT_GSO` and
`SILO_NET_DIRECT_ACK_DECIMATION` allow `on` and `off`, defaulting to `on`.
`SILO_NET_DIRECT_BBR_LOSS` allows `both`, `no_lower`, `loss_thresh`, and `off`,
defaulting to `both`. An unrecognized value of one of these three turns that knob
`off`, with reason `invalid`, as for the TCP knobs. These adjustments do not prevent
process startup. `SILO_NET_DIRECT_RECEIVE_WINDOW` and
`SILO_NET_DIRECT_ACK_DECIMATION` are client policy: the rows report the configured
values, and no client receives them yet. They do not change the
server's own engine or transport policy.

The architecture and privilege invariants are in
[Direct Connections](architecture/direct-connections.md). The web administrator
consumer uses this read; Apple, Android, and Jellyfin have no matching readiness
operation or playback change.
