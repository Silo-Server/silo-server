# Resilient Library Deletion Design

## Goal

Make `delete_library` survive concurrent database writers and large libraries. Today it deletes an entire library inside a single multi-minute transaction; on a large library this deadlocks against ordinary background writes and rolls back, making zero progress. Rewrite the deletion to run in small, autocommitted batches with per-batch deadlock retry, so it completes reliably, holds only short locks, and resumes cleanly if interrupted.

## Background

A `delete_library` admin job repeatedly failed with:

```
deleting orphaned items: ERROR: deadlock detected (SQLSTATE 40P01)
```

Diagnosis on a library of ~249K items / ~339K media files:

- The whole deletion runs in one transaction in `FolderRepository.DeleteWithStats` (`internal/catalog/folder_repo.go`): collect orphan IDs, drop the folder (CASCADE), then `DELETE FROM media_items WHERE content_id = ANY($1)`.
- `media_items` cascades to 15 child tables, so a bulk delete holds a very large lock set for minutes.
- The Postgres deadlock log shows the cycle: the delete cascade into `media_item_provider_ids` versus a concurrent `INSERT INTO media_item_provider_ids` from a routine background task. Each ~7-minute attempt was guaranteed to overlap an ordinary write and lose.
- Because it is one transaction, every failure rolls back completely: no progress, not resumable.

The fix targets the deletion mechanism only. Coordinating or pausing background writers is explicitly out of scope (see Out of Scope) — a batched, retrying delete is robust against *any* concurrent writer, not just today's tasks.

## Approach

Replace the single transaction with a sequence of phases. Each phase deletes in bounded batches, and each batch is one autocommit statement wrapped in a deadlock-retry helper. Short lock windows make deadlocks rare; when one occurs, only that small batch retries.

Scope of the rewrite:

- Rewrite `FolderRepository.DeleteWithStats` in `internal/catalog/folder_repo.go`.
- Add an unexported `retryOnDeadlock` helper in the `catalog` package.
- `FolderRepository.Delete` (thin wrapper), the `DeleteFolderStats` struct, and the `delete_library` executor in `internal/adminjob/library_delete.go` keep their current signatures and behavior. S3 image-cleanup queueing and generated-home-section cleanup are untouched.

## Deletion phases

`DeleteWithStats(ctx, id, progress)` performs, in order:

### Phase 0 — preflight (reads only)

- Load the folder name; return `ErrFolderNotFound` if the row is gone.
- `COUNT` media files (`WHERE media_folder_id = $1`), item links (`WHERE media_folder_id = $1`), and orphans, to populate the result payload and the progress denominator.

### Phase 1 — orphaned `media_items` (batched loop)

Repeat until no rows are returned:

```sql
SELECT mil.content_id
FROM media_item_libraries mil
WHERE mil.media_folder_id = $1
  AND NOT EXISTS (
    SELECT 1 FROM media_item_libraries other
    WHERE other.content_id = mil.content_id
      AND other.media_folder_id <> $1
  )
LIMIT $2          -- orphanDeleteBatch
```

For each returned batch: collect S3 image dirs for those IDs (existing `collectImageDirs`, accumulated into a deduped set), then

```sql
DELETE FROM media_items WHERE content_id = ANY($1)
```

The FK cascade removes each item's `media_item_libraries` (including this folder's), `media_item_provider_ids`, episodes, seasons, embeddings, and the rest of the 15 child tables. Deleting orphan items first also clears their junction rows, so the next iteration's detection query naturally advances. Orphan detection must run before Phase 3 touches shared junctions, so the "only in this folder" test stays valid.

### Phase 2 — folder `media_files` (batched loop)

`media_files` is folder-tied (`media_folder_id` FK, `ON DELETE CASCADE`), not item-tied, so it is deleted by folder. Repeat until a batch deletes fewer than the batch size:

```sql
DELETE FROM media_files
WHERE id IN (
  SELECT id FROM media_files WHERE media_folder_id = $1 LIMIT $2  -- folderChildDeleteBatch
)
```

### Phase 3 — remaining shared-item junctions (batched loop)

After Phase 1, the only `media_item_libraries` rows left for this folder belong to items that also live in other folders. Delete them in batches (dropping the membership only, never the shared item):

```sql
DELETE FROM media_item_libraries
WHERE ctid IN (
  SELECT ctid FROM media_item_libraries WHERE media_folder_id = $1 LIMIT $2  -- folderChildDeleteBatch
)
```

### Phase 4 — folder row

```sql
DELETE FROM media_folders WHERE id = $1
```

Now lightweight. Tolerates 0 rows affected (a resumed re-run may already have removed it) — it does not re-raise `ErrFolderNotFound` here. The folder row is deleted **last**, so a partial deletion leaves the library present-but-disabled and a re-run continues.

## Deadlock-retry helper

New unexported helper in the `catalog` package:

```go
func retryOnDeadlock(ctx context.Context, op func() error) error
```

- Retries when the error is a `*pgconn.PgError` with code `40P01` (deadlock) or `40001` (serialization failure).
- Up to 5 attempts, exponential backoff starting at 50ms, doubling each retry.
- Honors `ctx` cancellation between attempts (returns `ctx.Err()`).
- Any non-retryable error returns immediately; the final retryable error is returned after the cap.

Every batch statement runs through `retryOnDeadlock`. It has a single caller today, so it stays package-local; it can be promoted to a shared DB utility if a second caller appears.

## Batch sizes

Named constants in `internal/catalog/folder_repo.go`, tunable:

- `orphanDeleteBatch = 1000` — small, because each `media_items` row cascades across 15 tables.
- `folderChildDeleteBatch = 5000` — for the lighter folder-scoped `media_files` and junction deletes.

## Transaction model, stats, progress

- No surrounding transaction. Each batch is a single `pool.Exec` (its own implicit transaction), mirroring the existing batched-cleanup pattern in `internal/opslog/cleanup.go`. This is the core change that eliminates the multi-minute lock window.
- Stats: `MediaFiles` and `MediaItemLinks` come from the Phase 0 counts; `OrphanedItems` accumulates across Phase 1 batches; `OrphanedImageDirs` is the accumulated deduped dir set. Image-dir volume for the affected workloads is modest; the deduped set is the bounded-memory point of note.
- Progress: the callback reports orphan items processed against the Phase 0 orphan total, plus phase-transition messages. The admin-job runner's existing 10s heartbeat keeps the job alive across the drain.

## Error handling & resumability

- Because batches autocommit, a failure or cancellation persists partial progress. There is no rollback of hundreds of thousands of rows and no long lock hold.
- The job is resumable by re-running it: the folder still exists (deleted last), and Phase 1 re-detects whatever orphans remain.
- New items inserted concurrently during deletion are picked up by the loop's re-detection.

## Testing

The `catalog` package uses pure-Go unit tests; there is no Postgres integration harness, so tests avoid a live database.

- **`retryOnDeadlock` unit test:** a fake `op` returns a `*pgconn.PgError{Code: "40P01"}` N times then `nil` — assert it retries, respects the attempt cap, backs off, and returns immediately on a non-retryable error; a separate case asserts `ctx` cancellation stops retries.
- **Batch-loop unit test:** extract the "loop until a batch is under the batch size" iteration behind a small injected exec function so the looping/chunking logic is testable without Postgres.
- **Integration verification on dev:** deploy and re-trigger the deletion against the real stuck library; confirm it drains to completion via application logs and database row counts. This is the integration test, given the absence of a catalog DB harness. (Commands assume the repository root is the cwd and follow the deployment-debugging runbook.)

## Out of scope

- Pausing or coordinating background writers (e.g., having metadata/scan tasks skip disabled or pending-deletion libraries). The batched, retrying delete is robust against any concurrent writer; writer coordination is a separate potential improvement.
- Changing the admin-job runner, the executor's image-cleanup queueing, or home-section cleanup.
- Schema or FK changes.
