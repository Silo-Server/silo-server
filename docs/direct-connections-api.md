# Direct Connections API

`GET /api/v2/admin/direct-connections/status` requires an acting administrator.
Operation ID: `getAdminDirectConnectionsStatus`.
The hidden self-check is a readiness diagnostic; it starts no direct engine.
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
Non-Linux local rows are `off/unsupported_platform`.

Listener rows carry `listener`, `requested`, `granted`, `state`, `reason`, optional
`errno`, and `send_buffer`. The nested send-buffer requested and granted values
are integers; the grant uses the same usable-byte convention as feature rows.
Knob rows carry `env`, `default`, optional `allowed`, `effective`,
and optional `requested` and `reason` when startup parsing adjusted a request.
Numeric byte-request knobs omit `allowed`.

The architecture and privilege invariants are in
[Direct Connections](architecture/direct-connections.md). The web administrator
consumer uses this read; Apple, Android, and Jellyfin have no matching readiness
operation or playback change.
