# Ebook Enrichment Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make ebook discovery fast and deterministic while metadata enrichment runs independently as a durable, bounded, observable workflow that can improve records over time.

**Architecture:** The scanner remains responsible for filesystem reconciliation, embedded metadata, local artwork, format grouping, and identity hints such as ISBN. It never performs broad remote enrichment. A database-backed ebook enrichment state machine claims work with leases, applies outcome-specific retry or refresh horizons, and is drained by the existing task manager. The ebook metadata plugin uses ISBN-first source tiers, ranked matches, and per-source health cooldowns instead of fanning every title query out to every configured source.

**Tech Stack:** Go, PostgreSQL/goose, pgx, Silo task manager, Silo plugin gRPC API

## Global Constraints

- Ebook scans must not wait for remote metadata providers.
- Existing embedded ebook metadata and local artwork remain higher priority than remote metadata.
- Ebooks use ISBN identifiers; audiobook-only identifiers and narrator fields must not be introduced.
- Enrichment must survive process restarts and must not let concurrent workers claim the same item.
- Provider outages, rate limits, and deterministic client errors must not block scans or create tight retry loops.
- Previously enriched records must become eligible for controlled refresh so corrected metadata can be adopted later.
- Changes must be based on `Silo-Server/silo-server` upstream `main` and must not modify unrelated local branches.

---

### Task 1: Decouple Ebook Scans From Remote Retry

**Files:**
- Modify: `internal/libraryingest/executor.go`
- Modify: `internal/libraryingest/executor_test.go`

**Interfaces:**
- Consumes: `librarykind.Of(folder.Type)` and the existing matcher interface.
- Produces: `usesDedicatedEnrichment(folderType string) bool`, used to skip synchronous unmatched-item retry for ebooks while preserving existing video behavior.

- [x] **Step 1: Write the failing executor test**

Add a matcher that records retry calls and a table test which ingests an ebook folder and a movie folder. Assert that `RetryUnmatchedItemsByFolderAndPathPrefix` is not called for `ebooks`, but remains called for `movies`.

- [x] **Step 2: Run the focused test and verify red**

Run: `go test ./internal/libraryingest -run TestIngestFolderSkipsSynchronousRetryForDedicatedEnrichment -count=1`

Expected: FAIL because ebook ingestion still calls the synchronous retry method.

- [x] **Step 3: Implement the dedicated-enrichment gate**

Add:

```go
func usesDedicatedEnrichment(folderType string) bool {
	return librarykind.Of(folderType).Ebook
}
```

In the scoped matching loop, retain `ProcessAllByFolderAndPathPrefix` and variant finalization, but call `RetryUnmatchedItemsByFolderAndPathPrefix` only when `usesDedicatedEnrichment(folder.Type)` is false.

- [x] **Step 4: Verify the focused and package tests**

Run: `go test ./internal/libraryingest -count=1`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/libraryingest/executor.go internal/libraryingest/executor_test.go
git commit -m "fix(ebooks): decouple enrichment from library scans"
```

### Task 2: Replace Failure Counting With a Durable Enrichment State Machine

**Files:**
- Create: `migrations/sql/20260719090000_ebook_enrichment_jobs.sql`
- Create: `internal/ebooks/enrichment_queue.go`
- Create: `internal/ebooks/enrichment_queue_test.go`
- Modify: `internal/ebooks/enrichment.go`
- Modify: `internal/ebooks/enrichment_test.go`

**Interfaces:**
- Produces: `EnrichmentQueue.Enqueue(ctx, contentID, priority)`, `ClaimBatch(ctx, limit, leaseDuration)`, `Complete(ctx, contentID, outcome, refreshAfter)`, and `Fail(ctx, contentID, errorClass, message, retryAfter)`.
- Consumes: `enrichmentItemRow` and the existing `Enricher.enrichItem` persistence path.

- [x] **Step 1: Write failing queue query and transition tests**

Test that claims use `FOR UPDATE SKIP LOCKED`, set `lease_until`, and only select `pending` jobs whose `next_attempt_at` is due or whose lease expired. Test outcome policies:

```go
success -> next_attempt_at = now + 90 days
no_match -> next_attempt_at = now + 30 days
transient failure -> exponential backoff capped at 24 hours
rate_limited -> provider retry horizon, capped at 24 hours
permanent failure -> next_attempt_at = now + 30 days
```

- [x] **Step 2: Run queue tests and verify red**

Run: `go test ./internal/ebooks -run 'TestEnrichmentQueue|TestEnrichmentRetryPolicy' -count=1`

Expected: FAIL because the queue and retry policy do not exist.

- [x] **Step 3: Add the migration**

Extend `ebook_enrichment_state` with:

```sql
status text NOT NULL DEFAULT 'pending',
priority integer NOT NULL DEFAULT 0,
attempts integer NOT NULL DEFAULT 0,
next_attempt_at timestamptz NOT NULL DEFAULT now(),
lease_until timestamptz,
last_attempt_at timestamptz,
completed_at timestamptz,
outcome text,
last_error_class text,
last_error text
```

Add a partial claim index on `(priority DESC, next_attempt_at, updated_at)` for rows whose status is `pending` or `running`.

- [x] **Step 4: Implement atomic queue claims and transitions**

Use one transaction and a CTE:

```sql
WITH candidates AS (
    SELECT content_id
    FROM ebook_enrichment_state
    WHERE next_attempt_at <= now()
      AND (status = 'pending' OR (status = 'running' AND lease_until < now()))
    ORDER BY priority DESC, next_attempt_at, updated_at
    FOR UPDATE SKIP LOCKED
    LIMIT $1
)
UPDATE ebook_enrichment_state state
SET status = 'running',
    lease_until = now() + $2::interval,
    last_attempt_at = now(),
    attempts = attempts + 1,
    updated_at = now()
FROM candidates
WHERE state.content_id = candidates.content_id
RETURNING state.content_id;
```

Keep state rows after success so refresh eligibility and prior outcomes are durable.

- [x] **Step 5: Route `Enricher.Run` through the queue**

Materialize missing candidates into `ebook_enrichment_state` with `INSERT ... SELECT ... ON CONFLICT DO NOTHING`, claim a bounded batch, load the existing item fields, and transition each claim to success, no-match, skipped, or failure. Context cancellation must release the lease without incrementing item failure state.

- [x] **Step 6: Verify ebook package tests**

Run: `go test ./internal/ebooks -count=1`

Expected: PASS.

- [x] **Step 7: Commit**

```bash
git add migrations/sql/20260719090000_ebook_enrichment_jobs.sql internal/ebooks/enrichment_queue.go internal/ebooks/enrichment_queue_test.go internal/ebooks/enrichment.go internal/ebooks/enrichment_test.go
git commit -m "feat(ebooks): add durable metadata enrichment queue"
```

### Task 3: Drain Bounded Work Continuously And Report Honest Progress

**Files:**
- Modify: `internal/taskmanager/tasks/sync_ebook_metadata.go`
- Modify: `internal/taskmanager/tasks/sync_ebook_metadata_test.go`
- Modify: `internal/ebooks/enrichment.go`
- Modify: `cmd/silo/main.go`

**Interfaces:**
- Produces: `EnrichmentRunResult{Claimed, Enriched, NoMatch, Failed, Deferred, Remaining int}`, an incremental task, and a separate manual legacy-backfill task.
- Consumes: the durable queue from Task 2.

- [x] **Step 1: Write failing task tests**

Test that the scheduled task drains only priority-zero-or-higher work until the queue is empty or a four-minute execution budget expires. Add a separate `backfill_ebook_metadata` task with no default triggers which may claim the priority `-100` legacy backlog. Assert progress messages include claimed, enriched, failed, and remaining counts, and cancellation stops between batches.

- [x] **Step 2: Run focused task tests and verify red**

Run: `go test ./internal/taskmanager/tasks -run TestSyncEbookMetadata -count=1`

Expected: FAIL because the task runs one opaque batch and only returns `items_enriched`.

- [x] **Step 3: Add structured enrichment results**

Change `Enricher.Run` to return:

```go
type EnrichmentRunResult struct {
	Claimed   int `json:"claimed"`
	Enriched  int `json:"enriched"`
	NoMatch   int `json:"no_match"`
	Failed    int `json:"failed"`
	Deferred  int `json:"deferred"`
	Remaining int `json:"remaining"`
}
```

- [x] **Step 4: Implement time-budgeted draining**

Loop over bounded queue claims while work remains and the execution deadline has not elapsed. The scheduled task must never claim legacy-backfill rows; only the manually triggered backfill task may include them. Report progress after every batch; never estimate 100 percent until no immediately eligible work remains.

- [x] **Step 5: Verify task and ebook tests**

Run: `go test ./internal/taskmanager/tasks ./internal/ebooks -count=1`

Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add internal/taskmanager/tasks/sync_ebook_metadata.go internal/taskmanager/tasks/sync_ebook_metadata_test.go internal/ebooks/enrichment.go
git commit -m "feat(ebooks): drain enrichment backlog with progress"
```

### Task 4: Rebuild Ebook Provider Strategy In The Metadata Plugin

**Files (plugin repository `silo-plugin-ebook-metadata`):**
- Create: `provider/health.go`
- Create: `provider/health_test.go`
- Create: `provider/ranking.go`
- Create: `provider/ranking_test.go`
- Modify: `provider/provider.go`
- Modify: `provider/provider_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces: source tiers `identifier`, `catalog`, and `extended`; `SourceHealth.Allow`, `RecordSuccess`, and `RecordFailure`; deterministic `RankMatches`.
- Consumes: existing `Source.Search`, `Source.Fetch`, normalized ISBN helpers, and plugin settings.

- [x] **Step 1: Write failing source-tier tests**

Assert default operation enables reliable catalog sources only, exact ISBN fetches use identifier-capable sources in order, and title/author searches do not invoke optional scraper sources unless explicitly enabled.

- [x] **Step 2: Write failing health-policy tests**

Assert HTTP 403/405 disables a source for one hour, HTTP 429 honors `Retry-After`, timeouts use exponential cooldown, and one healthy response closes the circuit.

- [x] **Step 3: Write failing ranking tests**

Rank exact normalized ISBN first, then normalized title plus author, then title plus year/language. Reject low-confidence title-only collisions and deduplicate matches by normalized ISBN or source/provider ID.

- [x] **Step 4: Implement tiered querying**

For an ISBN query, fetch identifier sources sequentially and stop after a complete high-confidence match. For title/author queries, query the reliable catalog tier with bounded concurrency, rank all results, and query the extended tier only when enabled and the catalog tier produced no acceptable match.

- [x] **Step 5: Implement per-source health cooldowns**

Wrap every source call with health admission and outcome recording. A source in cooldown is skipped without returning a provider-wide error; healthy sources continue serving the request.

- [x] **Step 6: Update plugin documentation**

Document default sources, optional extended sources, API-key sources, match ranking, cooldown behavior, and the `enabled_sources` override.

- [x] **Step 7: Verify plugin tests**

Run: `go test ./... -count=1`

Expected: PASS.

- [x] **Step 8: Commit**

```bash
git add provider/health.go provider/health_test.go provider/ranking.go provider/ranking_test.go provider/provider.go provider/provider_test.go README.md
git commit -m "feat: add tiered resilient ebook metadata sources"
```

### Task 5: Add Backfill Safety Controls

**Files:**
- Modify: `internal/taskmanager/tasks/sync_ebook_metadata.go`
- Modify: `internal/taskmanager/tasks/sync_ebook_metadata_test.go`

**Interfaces:**
- Consumes: the manual legacy task from Task 3.
- Produces: a claim cap, inter-batch pacing, and a no-progress circuit breaker.

- [ ] **Step 1: Preserve typed source deferrals**

Count `ResourceExhausted` provider results as deferred work with the provider's
retry delay, not as generic failures.

- [ ] **Step 2: Stop stalled drains**

Stop cleanly after one full batch with no terminal outcome. A plugin packaging
error, source outage, or rate-limit saturation must never walk the backlog.

- [ ] **Step 3: Add canary controls**

Support a maximum number of claims per manual run and an inter-batch delay.
The first production trial uses 20 claims, one worker, and a one-second delay.

- [ ] **Step 4: Verify safety behavior**

Test all-failed, all-deferred, mixed-progress, claim-cap, pacing, cancellation,
and honest result reporting.

### Task 6: End-To-End Verification And Operational Rollout

**Files:**
- Modify: `docs/superpowers/plans/2026-07-19-ebook-enrichment-architecture.md`

**Interfaces:**
- Consumes: Tasks 1-4.
- Produces: verified deployment and backfill procedure.

- [ ] **Step 1: Run server verification**

Run: `go test ./internal/libraryingest ./internal/ebooks ./internal/taskmanager/tasks ./internal/scanner -count=1`

Expected: PASS.

- [ ] **Step 2: Run race-sensitive server verification**

Run: `go test -race ./internal/ebooks ./internal/taskmanager/tasks -count=1`

Expected: PASS.

- [ ] **Step 3: Run plugin verification**

Run from the plugin worktree: `go test -race ./... -count=1`

Expected: PASS.

- [ ] **Step 4: Package and preflight**

Build the plugin binary and installed manifest together. Verify their checksums,
versions, and runtime manifests match before replacing a running installation.

- [ ] **Step 5: Deploy a bounded production canary**

Deploy without overwriting compose or `.env`. Use 20 maximum claims, one worker,
and a one-second inter-batch delay. Do not enable an automatic legacy trigger.

- [ ] **Step 6: Apply acceptance gates**

Promote only when runtime-manifest errors are zero, no full batch fails or
defers, at least 80 percent of claimed rows reach success or no-match, database
claims remain below one second, and Silo stays healthy without scan regressions.

- [ ] **Step 7: Expand in measured stages**

Increase the claim cap only after recording throughput, provider error rate,
queue depth, database load, and projected completion time. Stop automatically
when a stage misses an acceptance gate.
