# apibench — hot-path performance harness

Drives the named API hot paths against a running Silo server and writes a JSON
report. It measures; it does not judge. Budgets and material-regression
thresholds are maintainer execution input (plan input 6) and are applied by
whoever compares two reports.

Commands assume the repository root is the cwd.

## What it records

Per path: request count, error count, status counts, elapsed window,
throughput (req/s), latency p50/p95/p99/max/mean (ms), and response bytes
(total/mean/max). With `-metrics` it also records the server's Go allocation
deltas over the window (`go_memstats_mallocs_total`,
`go_memstats_alloc_bytes_total`, and per-request averages). With `-db` it
records database activity deltas from `pg_stat_database` (`xact_commit`,
`tup_returned`, `tup_fetched`) and, when `pg_stat_statements` is installed,
total statement calls per request. A source that is not configured is absent
from the report rather than reported as zero.

## Named hot paths

`catalog_list`, `catalog_search`, `home_sections`, `playback_start`,
`playback_replan`, `progress_write`, `jellycompat_browse`. The example plan
under `testdata/plan.example.json` names all seven; copy it and fill in the
`${...}` environment references. Playback start captures `session_id` from its
first response so replan can target it. Mutating paths use `"once": true` so
they run one request per worker instead of the full window.

## Running

Not part of CI. It needs a reachable server and, for query counts, a database
DSN in the same style as the test suite's `SILO_TEST_DATABASE_URL` gating:

```
SILO_BENCH_BEARER=... SILO_BENCH_PROFILE_ID=... \
go run ./cmd/apibench -plan internal/apibench/testdata/plan.example.json \
    -out /tmp/v1-baseline.json -metrics http://127.0.0.1:8080/metrics \
    -db "$SILO_TEST_DATABASE_URL" -concurrency 8 -duration 30s -label v1-baseline
```

Run the same plan against the v2 candidate with `-label v2-candidate` in the
same environment, cache state, and dataset, then compare the two reports.
Streaming byte delivery is measured separately; it bypasses the JSON API.

## Report shape

`schema_version`, `label`, `started_at`, `finished_at`, `cache_state`,
`concurrency`, `duration`, `target` (base URLs plus which samplers were live),
and `paths[]` with the fields above. Credentials never appear in the report.
