# Remaining frozen setup family

`TestRequiredSetupFamilyAcceptance` runs the ten remaining original `setup.*`
cases as twenty independent v1/v2 results. The embedded originals preserve every
request, description, requirement, initial-state flag and v1 assertion. The three
previously paired non-r1 refusals are excluded.

The status/shape/meaning originals are **completed-setup refusals** against the
seeded household. They are not successful initial-setup cases. Their original
notes explicitly require separate empty-database success coverage. This packet
does not change that initial state or claim success-path acceptance.

Use a fresh, exclusively owned disposable PostgreSQL instance with pgvector,
bound to an ephemeral `127.0.0.1` port. Record resource ownership before creating
it. The database name must begin `silo_worker_setup_`; shared scenario ports are
rejected. Never point this executor at an existing deployment or a peer resource.
Set `SILO_SCENARIO_DATABASE_URL` privately, then run from the repository root:

```sh
SILO_WORKER_SETUP_OWNED=1 SILO_SCENARIO_REQUIRED=1 \
  go test -v -count=1 -run '^TestRequiredSetupFamilyAcceptance$' \
  ./internal/scenariocatalog/executor

go test -count=1 -run '^TestSetupFamilySelection$' \
  ./internal/scenariocatalog/executor
```

`SILO_SCENARIO_REPORT` optionally writes the per-transport JSON report. The runner
checks ownership constraints and unknown API keys before the shared constructor;
the constructor's accepted foreign-data guard still runs before migrations and
reseeds. It reseeds before each transport and at teardown, and resets actual
rate-limit counters through the existing reload API. All r1 cases use the real
limited router for both transports.

Each of 32 HTTP requests has before/after snapshots of eight complete tables:
users, profiles, API keys, server settings, login sessions, device login requests,
invitations and invite codes. Every row must remain unchanged. The rate-limit
case retains its original seventh-request oracle and independently checks that
the first six requests are completed-setup refusals. V1 retains all original
rate-limit headers; v2 uses the accepted Problem contract with Retry-After and
without legacy X-RateLimit headers. No fake limiter or threshold change is used.

The seventy selector negatives reject missing/duplicate cases, missing pairings,
changed original oracles, changed request intent, incorrect status and wrong
registration. This evidence does not cover concurrent reloads, empty-database
setup, real enrollment, outages, signup, login or native consumers. Source,
ledger and generated contracts are unchanged. Stop and remove only the recorded
owned resource after preserving evidence.
