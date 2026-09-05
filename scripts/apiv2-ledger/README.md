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
- `assign_sections.py [--check] [LEDGER]` writes the `section` field (the Phase 4 delivery
  unit) onto every entry from listener, namespace and path; `--check` is what
  `make verify-migration-ledger` runs. Move a route between sections by editing the
  script's tables, then rerun it.
  — merge a regenerated inventory and consumer map into the ledger by key,
  preserving curated fields, manual and follower call sites, and each
  mechanical site's `path_literal_line`; seeds defaults only for rows with no
  entry. The two SHAs (`git rev-parse origin/main` in each sibling after
  `git fetch`) are written to the ledger's `source_trees` header and must be
  the commits the trees were exported from. Re-running on an unchanged
  inventory and consumer map rewrites the ledger byte for byte.

Two call-site annotations refine what the pinned-tree test
(`TestSiblingCallSitesResolveAgainstPinnedTrees` in `internal/contractledger`)
checks at a sibling site. `path_literal_line` is the line where a path
constant is declared when the call line only names it (`static let endpoint`,
`const val DEVICES_PATH`), so the route's last static segment is looked for
there instead of at `line`. `match: follower` marks a site that never spells
the path because the client fetches a server-supplied URL from the playback
plan, and the site is the validator or resolver that admits it; a follower is
exempt from the segment check but its file and line must still exist at the
pin. The test skips only when a sibling checkout is absent next to this
repository; a checkout that is present but has not fetched the pinned commit,
or a site that no longer holds up at the pin, fails it.
