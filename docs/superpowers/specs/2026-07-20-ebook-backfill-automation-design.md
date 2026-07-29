# Ebook Backfill Automation + Rate-Limit Cooldown Floor

Date: 2026-07-20
Status: approved

## Problem

The legacy ebook enrichment lane (priority -100, ~65k items on the first
production install) only drains when an operator manually runs the
`backfill_ebook_metadata` task. Worse, the first production canary run showed
that rate-limited items become claimable again almost immediately: the
ebook-metadata plugin attaches a `RetryInfo` of about one second to its
`ResourceExhausted` errors — request-pacing advice for its internal token
bucket — and `enrichmentRetryDelay` adopts that hint verbatim as the queue
re-eligibility horizon. Successive runs therefore re-claim and re-defer the
same saturated tail instead of letting the provider's quota recover.

Evidence (production, 2026-07-20): 100 claimed, 30 enriched, 1 no-match,
69 deferred with `last_error_class = 'rate_limited'` and
`next_attempt_at ≈ last_attempt_at + 1s`.

## Design

Two independent pieces that compose into self-pacing automation.

### 1. Rate-limit cooldown floor

`internal/ebooks/enrichment_queue.go`, `enrichmentRetryDelay`:

- Rate-limited branch: resolve the candidate delay as today (provider
  `retryAfter` if positive, else the transient backoff for the attempt
  count), then clamp it to no less than the cooldown floor, and cap at
  `maxEnrichmentRetry` (24h) as today.
- Floor default: **15 minutes**.
- Env override: `SILO_EBOOK_RATE_LIMIT_COOLDOWN` (Go duration string,
  e.g. `30m`). Invalid or non-positive values fall back to the default,
  matching the tolerant parsing style of the other `SILO_EBOOK_*` knobs.
- Provider hints longer than the floor are honored unchanged (up to the
  24h cap). Only pacing-grade short hints are raised.
- Transient and permanent classes are unchanged.

### 2. Default interval trigger for the backfill task

`internal/taskmanager/tasks/sync_ebook_metadata.go`:

- `BackfillEbookMetadataTask` gains a default trigger:
  `TriggerConfig{Type: TriggerTypeInterval, IntervalMs: 15 * 60 * 1000}`.
- The task manager applies defaults whenever no triggers are persisted for
  the task key (`internal/taskmanager/manager.go`), and this task has never
  persisted any, so existing installs pick the trigger up on upgrade with no
  migration or manual step.
- Operators can retune or disable the trigger through the existing admin
  task-triggers UI/API; the manual run endpoint keeps working.
- The canary claim cap (`SILO_EBOOK_BACKFILL_MAX_CLAIMS`) and batch delay
  (`SILO_EBOOK_BACKFILL_BATCH_DELAY`) keep their semantics; unset means
  uncapped runs bounded by the 4-minute execution budget.

### Emergent behavior

Each 15-minute run claims what is ready under the execution budget and the
claim cap. Rate-limited items sleep at least the floor, so the next run meets
a fresh ready-set instead of the same saturated tail. A fully saturated batch
trips the existing zero-progress circuit breaker and ends the run early. An
empty ready-set makes the run exit in milliseconds. The backlog drains at
whatever pace the provider allows, unattended.

## Testing

- `enrichmentRetryDelay`: short hint raised to the floor; hint above the
  floor honored; no hint yields at least the floor; 24h cap still applies.
- Cooldown env parsing: valid duration honored, invalid/non-positive falls
  back to the default.
- `BackfillEbookMetadataTask.DefaultTriggers()` returns the 15-minute
  interval trigger; the sync task's triggers are unchanged.

## Out of scope

- Plugin-side changes to what `RetryInfo` the ebook-metadata plugin sends
  (its 1s hint remains correct for in-process pacing).
- Any change to the scheduled priority-0 sync task.
- Migrations and API surface: none needed.
