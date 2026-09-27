# Device poll rate-limited registration

`TestRequiredDevicePollR1Acceptance` runs the nine original registration-one
`device_poll.*.r1` cases as eighteen independent v1/v2 results on the real
rate-limited router. The embedded originals preserve every request, repeat count,
description, requirement, initial-state flag and v1 assertion. The eight
registration-zero cases paired earlier are excluded; no earlier result is copied.

The pairing keeps the accepted `pollDeviceLogin` contract: v2 nests the token pair
under `tokens`, always emits `profile_id`, `profile_token` and `temporary`, renders
`session_expires_at` as a UTC millisecond instant, answers `Cache-Control: no-store`,
and maps unknown, missing and rate-limited refusals to the `not_found` (404),
`validation_failed` (422) and `rate_limited` (429) problems. V1 keeps its original
status, headers, body oracles and requirements unchanged.

`device_poll.rate_limited.r1` is one of the two frozen bursts admitted by
`ValidateScenarioPairing`; the default sixteen-exchange budget still binds every
other case. All 31 requests are sent per transport: the first 30 must reach the
original unknown-code refusal with no effect, and request 31 must be refused by the
real per-second bucket (burst 30, 120 per minute) with the original headers and body.
The runner measures the burst against the real 500 ms refill and fails if the burst
outlived it; it never lowers the threshold, freezes the clock or primes hidden calls.

Each of the 80 requests has before/after snapshots of 26 complete tables taken with
one statement. Pending, denied, expired, unknown and missing polls must leave every
row unchanged. A collecting poll (`approved.r1`, `remote_approved.r1`, and the first
poll of `consumed.r1`) must add exactly one login session for the approving member
and move exactly one fixture request from approved to consumed, bound to that session
with `consumed_at` equal to `updated_at`; every other row and table stays unchanged.
The second `consumed.r1` poll must report consumed without credentials and without
any further change. Issued access and refresh tokens are verified against the
account, the new session, role, type and configured lifetimes. Remote approval must
carry the member's unlocked primary profile, a profile token bound to the account,
session, profile and current policy revision, and a stored session expiry capped at
24 hours below the configured refresh lifetime; the wire instant must equal that
stored expiry truncated to whole seconds (v1) or milliseconds (v2).

Use a fresh, exclusively owned scratch database on the loopback recovery PostgreSQL
instance. Record the database name before creating it. The name must begin
`silo_catalog_device_poll_`; shared scenario ports are rejected. Set
`SILO_SCENARIO_DATABASE_URL` privately, then run from the repository root:

```sh
SILO_CATALOG_DEVICE_POLL_OWNED=1 SILO_SCENARIO_REQUIRED=1 \
  go test -v -count=1 -run '^TestRequiredDevicePollR1Acceptance$' \
  ./internal/scenariocatalog/executor

go test -count=1 -run '^TestDevicePollR1Selection$' \
  ./internal/scenariocatalog/executor
```

`SILO_SCENARIO_REPORT` optionally writes the per-transport JSON report. The runner
checks ownership constraints, the constructor's foreign-data guard and unknown API
keys before the shared constructor; it reseeds before and after every transport and
resets actual rate-limit counters through the existing reload API.

The 81 selector negatives reject missing/duplicate cases, missing pairings, changed
original oracles, changed request intent or repeat counts, incorrect status, wrong
registration and added follow-up steps. This evidence does not cover concurrent
polls, retry after a lost collecting response, PIN-locked profile approval, the
approval and denial endpoints, real enrollment or outages. Source, ledger and
generated contracts are unchanged. Drop only the recorded owned database after
preserving evidence.
