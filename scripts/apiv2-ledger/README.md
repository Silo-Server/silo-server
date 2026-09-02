# apiv2-ledger

Tooling behind the consumer evidence in `contracts/api/v2/migration.json`. See
"Migration ledger" in `docs/architecture/api-contract.md` for how the pieces fit.
Commands assume the repository root is the cwd and take repository paths as
arguments; export the sibling repos with `git archive <sha>` at the commit you
will pin rather than pointing the scripts at a working checkout.

- `extract_consumers.py WEB_SRC APPLE_ROOT ANDROID_ROOT OUT` — find every
  first-party HTTP/WebSocket call site against the native API, including paths
  built by helper functions and string builders.
- `match_consumers.py ROUTE_INVENTORY CALL_SITES OUT_MAP OUT_UNMATCHED` — map
  call sites onto inventory rows by normalized path template and method; the
  unmatched list is for manual resolution.
- `sweep_uncredited.py ROUTE_INVENTORY LEDGER WEB_SRC APPLE_ROOT ANDROID_ROOT [APPLE_GIT ANDROID_GIT]`
  — grep the last two static segments of every inventory path and list every
  request-shaped hit the ledger does not credit yet; with the sibling git
  checkouts given, also report credited files missing at the pinned commits
  as `stale-against-pinned-tree`.
- `build_ledger.py ROUTE_INVENTORY CONSUMER_MAP LEDGER APPLE_SHA ANDROID_SHA`
  — merge a regenerated inventory and consumer map into the ledger by key,
  preserving curated fields and manual call sites; seeds defaults only for
  rows with no entry. The two SHAs (`git rev-parse origin/main` in each
  sibling after `git fetch`) are written to the ledger's `source_trees` header
  and must be the commits the trees were exported from.
