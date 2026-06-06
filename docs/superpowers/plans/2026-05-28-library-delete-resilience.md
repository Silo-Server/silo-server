# Resilient Library Deletion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite `delete_library` so it deletes a library in small, autocommitted, deadlock-retrying batches instead of one multi-minute transaction that deadlocks and rolls back on large libraries.

**Architecture:** `FolderRepository.DeleteWithStats` becomes a phased, batched operation (orphan items → folder media_files → remaining shared junctions → folder row). Each batch is one autocommit statement wrapped in a `retryOnDeadlock` helper, so locks are held briefly and the job is resumable. Two small helpers (`retryOnDeadlock`, `deleteInBatches`) are unit-tested in pure Go; the full path is verified against the live dev database.

**Tech Stack:** Go, pgx v5 (`pgxpool`, `pgconn`), PostgreSQL. Spec: `docs/superpowers/specs/2026-05-28-library-delete-resilience-design.md`.

All commands assume the repository root is the cwd.

---

## File Structure

- **Modify** `internal/catalog/folder_repo.go`
  - Add constants `orphanDeleteBatch`, `folderChildDeleteBatch`.
  - Add package vars `deadlockMaxAttempts`, `deadlockBaseBackoff` and func `retryOnDeadlock`.
  - Add func `deleteInBatches`.
  - Add interface `rowQuerier`; change `collectImageDirs` and `filterUnreferencedImageDirs` to take `rowQuerier` instead of `pgx.Tx`; extract `collectRawImageDirs`.
  - Rewrite `DeleteWithStats`; add helpers `collectOrphanBatch`, `dirSetToSlice`.
  - Leave `collectOrphanIDs` and `Delete` unchanged.
- **Create** `internal/catalog/folder_delete_test.go` — pure-Go unit tests for `retryOnDeadlock` and `deleteInBatches`.
- **Do not modify** `internal/catalog/library_repo.go` — its `collectImageDirs`/`collectOrphanIDs` calls keep compiling because `pgx.Tx` satisfies `rowQuerier`. (Verified by build.)
- **Unchanged behavior** in `internal/adminjob/library_delete.go` (executor), `internal/adminjob/runner.go` (heartbeat).

---

## Task 1: Deadlock-retry helper

**Files:**
- Modify: `internal/catalog/folder_repo.go`
- Test: `internal/catalog/folder_delete_test.go` (create)

- [ ] **Step 1: Write the failing tests**

Create `internal/catalog/folder_delete_test.go`:

```go
package catalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// withFastDeadlockRetry shrinks retry timing/attempts for tests and restores
// the originals on cleanup. Tests using it must not call t.Parallel().
func withFastDeadlockRetry(t *testing.T, maxAttempts int) {
	t.Helper()
	oldMax, oldBackoff := deadlockMaxAttempts, deadlockBaseBackoff
	deadlockMaxAttempts = maxAttempts
	deadlockBaseBackoff = time.Millisecond
	t.Cleanup(func() {
		deadlockMaxAttempts = oldMax
		deadlockBaseBackoff = oldBackoff
	})
}

func TestRetryOnDeadlockRetriesThenSucceeds(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	calls := 0
	err := retryOnDeadlock(context.Background(), func() error {
		calls++
		if calls < 3 {
			return &pgconn.PgError{Code: "40P01"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetryOnDeadlockReturnsNonRetryableImmediately(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	sentinel := errors.New("boom")
	calls := 0
	err := retryOnDeadlock(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestRetryOnDeadlockGivesUpAfterMaxAttempts(t *testing.T) {
	withFastDeadlockRetry(t, 4)
	calls := 0
	err := retryOnDeadlock(context.Background(), func() error {
		calls++
		return &pgconn.PgError{Code: "40P01"}
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
		t.Fatalf("expected 40P01 pg error, got %v", err)
	}
	if calls != 4 {
		t.Fatalf("expected 4 calls, got %d", calls)
	}
}

func TestRetryOnDeadlockStopsOnCanceledContext(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryOnDeadlock(ctx, func() error {
		calls++
		return &pgconn.PgError{Code: "40P01"}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call before cancel, got %d", calls)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/catalog/ -run TestRetryOnDeadlock -v`
Expected: compile failure / FAIL — `undefined: retryOnDeadlock`, `deadlockMaxAttempts`, `deadlockBaseBackoff`.

- [ ] **Step 3: Implement the helper**

In `internal/catalog/folder_repo.go`, add near the top of the file (after the `import` block, before the sentinel errors var is fine):

```go
// Retry parameters for transient serialization/deadlock failures. They are
// package vars (not consts) only so tests can shrink them; production code
// never mutates them.
var (
	deadlockMaxAttempts = 5
	deadlockBaseBackoff = 50 * time.Millisecond
)

// retryOnDeadlock runs op, retrying when Postgres reports a deadlock (40P01) or
// serialization failure (40001), with exponential backoff. It returns
// immediately for any other error, and honors context cancellation between
// attempts.
func retryOnDeadlock(ctx context.Context, op func() error) error {
	backoff := deadlockBaseBackoff
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		var pgErr *pgconn.PgError
		if attempt < deadlockMaxAttempts && errors.As(err, &pgErr) &&
			(pgErr.Code == "40P01" || pgErr.Code == "40001") {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}
		return err
	}
}
```

(`context`, `errors`, `time`, and `github.com/jackc/pgx/v5/pgconn` are already imported in this file.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/catalog/ -run TestRetryOnDeadlock -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Format and commit**

```bash
gofmt -w internal/catalog/folder_repo.go internal/catalog/folder_delete_test.go
git add internal/catalog/folder_repo.go internal/catalog/folder_delete_test.go
git commit -m "feat(catalog): add deadlock-retry helper for batched deletes"
```

---

## Task 2: Batched-delete loop helper

**Files:**
- Modify: `internal/catalog/folder_repo.go`
- Test: `internal/catalog/folder_delete_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/catalog/folder_delete_test.go`:

```go
func TestDeleteInBatchesLoopsUntilUnderBatchSize(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	counts := []int64{5, 5, 2}
	idx := 0
	total, err := deleteInBatches(context.Background(), 5, func(context.Context) (int64, error) {
		n := counts[idx]
		idx++
		return n, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 12 {
		t.Fatalf("expected total 12, got %d", total)
	}
	if idx != 3 {
		t.Fatalf("expected 3 batches, got %d", idx)
	}
}

func TestDeleteInBatchesStopsImmediatelyWhenFirstBatchUnderSize(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	calls := 0
	total, err := deleteInBatches(context.Background(), 5, func(context.Context) (int64, error) {
		calls++
		return 0, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || calls != 1 {
		t.Fatalf("expected total 0 and 1 call, got total=%d calls=%d", total, calls)
	}
}

func TestDeleteInBatchesReturnsError(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	sentinel := errors.New("delete failed")
	_, err := deleteInBatches(context.Background(), 5, func(context.Context) (int64, error) {
		return 0, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/catalog/ -run TestDeleteInBatches -v`
Expected: FAIL — `undefined: deleteInBatches`.

- [ ] **Step 3: Implement the helper**

In `internal/catalog/folder_repo.go`, add after `retryOnDeadlock`:

```go
// deleteInBatches repeatedly runs deleteBatch (each a single autocommit
// statement) until a batch removes fewer than batchSize rows. Each batch is
// retried on deadlock. It returns the total number of rows deleted.
func deleteInBatches(
	ctx context.Context,
	batchSize int,
	deleteBatch func(ctx context.Context) (int64, error),
) (int64, error) {
	var total int64
	for {
		var affected int64
		if err := retryOnDeadlock(ctx, func() error {
			n, e := deleteBatch(ctx)
			affected = n
			return e
		}); err != nil {
			return total, err
		}
		total += affected
		if affected < int64(batchSize) {
			return total, nil
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/catalog/ -run TestDeleteInBatches -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Format and commit**

```bash
gofmt -w internal/catalog/folder_repo.go internal/catalog/folder_delete_test.go
git add internal/catalog/folder_repo.go internal/catalog/folder_delete_test.go
git commit -m "feat(catalog): add deleteInBatches loop helper"
```

---

## Task 3: Generalize image-dir helpers to a querier interface

This lets the image-dir helpers run on either a `*pgxpool.Pool` (new batched path) or a `pgx.Tx` (existing `library_repo.go` path), and splits raw collection from filtering so the batched path can collect cheaply per batch and filter once.

**Files:**
- Modify: `internal/catalog/folder_repo.go:547-635` (the `collectImageDirs` / `filterUnreferencedImageDirs` block)

- [ ] **Step 1: Add the `rowQuerier` interface**

In `internal/catalog/folder_repo.go`, add (near the other type declarations, e.g. just above `collectOrphanIDs`):

```go
// rowQuerier is satisfied by both *pgxpool.Pool and pgx.Tx, letting read
// helpers run inside or outside an explicit transaction.
type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
```

- [ ] **Step 2: Split `collectImageDirs` into raw collection + filtering**

Replace the existing `collectImageDirs` function (currently `internal/catalog/folder_repo.go:549-584`) with these two functions:

```go
// collectImageDirs returns S3 directory prefixes for images belonging to the
// given content IDs that are not still referenced by other surviving content.
func collectImageDirs(ctx context.Context, q rowQuerier, contentIDs []string) ([]string, error) {
	dirs, err := collectRawImageDirs(ctx, q, contentIDs)
	if err != nil {
		return nil, err
	}
	return filterUnreferencedImageDirs(ctx, q, dirs, contentIDs)
}

// collectRawImageDirs returns the deduped S3 directory prefixes referenced by
// the given content IDs (items, their seasons, and their episodes), without
// filtering out dirs still used by other content.
func collectRawImageDirs(ctx context.Context, q rowQuerier, contentIDs []string) ([]string, error) {
	imgRows, err := q.Query(ctx, `
		SELECT poster_path, backdrop_path, logo_path FROM media_items WHERE content_id = ANY($1)
		UNION ALL
		SELECT poster_path, '', '' FROM seasons WHERE series_id = ANY($1)
		UNION ALL
		SELECT still_path, '', '' FROM episodes WHERE series_id = ANY($1)
	`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("collecting image paths: %w", err)
	}
	defer imgRows.Close()
	dirSet := make(map[string]struct{})
	for imgRows.Next() {
		var p1, p2, p3 string
		if err := imgRows.Scan(&p1, &p2, &p3); err != nil {
			return nil, fmt.Errorf("scanning image path: %w", err)
		}
		for _, p := range []string{p1, p2, p3} {
			if p != "" && !strings.Contains(p, "://") {
				if dir := pathDir(p); dir != "" {
					dirSet[dir] = struct{}{}
				}
			}
		}
	}
	if err := imgRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating image paths: %w", err)
	}
	dirs := make([]string, 0, len(dirSet))
	for dir := range dirSet {
		dirs = append(dirs, dir)
	}
	return dirs, nil
}
```

- [ ] **Step 3: Change `filterUnreferencedImageDirs` to take `rowQuerier`**

In `internal/catalog/folder_repo.go`, change the signature (currently `internal/catalog/folder_repo.go:586`) from:

```go
func filterUnreferencedImageDirs(ctx context.Context, tx pgx.Tx, dirs, deletingContentIDs []string) ([]string, error) {
```

to:

```go
func filterUnreferencedImageDirs(ctx context.Context, q rowQuerier, dirs, deletingContentIDs []string) ([]string, error) {
```

and change the single `tx.Query(ctx, ...)` call inside it to `q.Query(ctx, ...)`. Leave the SQL and the rest of the body unchanged.

- [ ] **Step 4: Verify the build (proves `library_repo.go` still compiles with `pgx.Tx`)**

Run: `go build ./... && go vet ./internal/catalog/`
Expected: no errors. (`library_repo.go` passes a `pgx.Tx` to `collectImageDirs`, which now accepts `rowQuerier`; `pgx.Tx` satisfies it.)

- [ ] **Step 5: Run the catalog tests**

Run: `go test ./internal/catalog/...`
Expected: PASS (existing tests plus Tasks 1–2 helpers).

- [ ] **Step 6: Format and commit**

```bash
gofmt -w internal/catalog/folder_repo.go
git add internal/catalog/folder_repo.go
git commit -m "refactor(catalog): make image-dir helpers querier-agnostic"
```

---

## Task 4: Rewrite `DeleteWithStats` as a batched, phased operation

**Files:**
- Modify: `internal/catalog/folder_repo.go:415-511` (the `DeleteWithStats` body) and add two helpers + two constants.

- [ ] **Step 1: Add the batch-size constants**

In `internal/catalog/folder_repo.go`, add near the retry vars from Task 1:

```go
const (
	// orphanDeleteBatch is small because each media_items row cascades across
	// ~15 child tables.
	orphanDeleteBatch = 1000
	// folderChildDeleteBatch covers the lighter folder-scoped media_files and
	// junction deletes.
	folderChildDeleteBatch = 5000
)
```

- [ ] **Step 2: Replace the `DeleteWithStats` body**

Replace the entire existing `DeleteWithStats` function (`internal/catalog/folder_repo.go:415-511`) with:

```go
func (r *FolderRepository) DeleteWithStats(
	ctx context.Context,
	id int,
	progress func(current, total int, message string),
) (*DeleteFolderStats, error) {
	stats := &DeleteFolderStats{}

	// Phase 0: preflight reads (no long-lived transaction).
	if err := r.pool.QueryRow(ctx, `SELECT name FROM media_folders WHERE id = $1`, id).Scan(&stats.LibraryName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFolderNotFound
		}
		return nil, fmt.Errorf("loading folder before delete: %w", err)
	}
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM media_files WHERE media_folder_id = $1`, id).Scan(&stats.MediaFiles); err != nil {
		return nil, fmt.Errorf("counting media files: %w", err)
	}
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM media_item_libraries WHERE media_folder_id = $1`, id).Scan(&stats.MediaItemLinks); err != nil {
		return nil, fmt.Errorf("counting media item links: %w", err)
	}
	var orphanTotal int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_item_libraries mil
		WHERE mil.media_folder_id = $1
		AND NOT EXISTS (
			SELECT 1 FROM media_item_libraries other
			WHERE other.content_id = mil.content_id
			AND other.media_folder_id <> $1
		)`, id).Scan(&orphanTotal); err != nil {
		return nil, fmt.Errorf("counting orphaned items: %w", err)
	}

	// Phase 1: delete orphaned media_items in detect-then-delete batches.
	// Cascade removes their junctions, provider IDs, episodes, seasons, etc.
	if progress != nil {
		progress(0, orphanTotal, "Deleting orphaned items")
	}
	rawDirs := make(map[string]struct{})
	for {
		ids, err := r.collectOrphanBatch(ctx, id, orphanDeleteBatch)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			break
		}
		dirs, err := collectRawImageDirs(ctx, r.pool, ids)
		if err != nil {
			return nil, err
		}
		for _, d := range dirs {
			rawDirs[d] = struct{}{}
		}
		if err := retryOnDeadlock(ctx, func() error {
			_, e := r.pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
			return e
		}); err != nil {
			return nil, fmt.Errorf("deleting orphaned items: %w", err)
		}
		stats.OrphanedItems += len(ids)
		if progress != nil {
			progress(stats.OrphanedItems, orphanTotal, "Deleting orphaned items")
		}
	}

	// Filter accumulated image dirs once, now that orphans are gone, against
	// any surviving content. Empty deleting-set means "exclude nothing".
	if len(rawDirs) > 0 {
		filtered, err := filterUnreferencedImageDirs(ctx, r.pool, dirSetToSlice(rawDirs), []string{})
		if err != nil {
			return nil, err
		}
		stats.OrphanedImageDirs = filtered
	}

	// Phase 2: delete this folder's media_files (folder-tied, not item-tied).
	if progress != nil {
		progress(orphanTotal, orphanTotal, "Deleting media files")
	}
	if _, err := deleteInBatches(ctx, folderChildDeleteBatch, func(ctx context.Context) (int64, error) {
		tag, e := r.pool.Exec(ctx, `
			DELETE FROM media_files
			WHERE id IN (
				SELECT id FROM media_files WHERE media_folder_id = $1 LIMIT $2
			)`, id, folderChildDeleteBatch)
		if e != nil {
			return 0, e
		}
		return tag.RowsAffected(), nil
	}); err != nil {
		return nil, fmt.Errorf("deleting media files: %w", err)
	}

	// Phase 3: delete remaining folder memberships (shared items kept; only the
	// membership in this folder is removed).
	if progress != nil {
		progress(orphanTotal, orphanTotal, "Removing library memberships")
	}
	if _, err := deleteInBatches(ctx, folderChildDeleteBatch, func(ctx context.Context) (int64, error) {
		tag, e := r.pool.Exec(ctx, `
			DELETE FROM media_item_libraries
			WHERE ctid IN (
				SELECT ctid FROM media_item_libraries WHERE media_folder_id = $1 LIMIT $2
			)`, id, folderChildDeleteBatch)
		if e != nil {
			return 0, e
		}
		return tag.RowsAffected(), nil
	}); err != nil {
		return nil, fmt.Errorf("deleting media item links: %w", err)
	}

	// Phase 4: delete the now-lightweight folder row. Tolerate 0 rows so a
	// resumed run that already removed it still succeeds.
	if err := retryOnDeadlock(ctx, func() error {
		_, e := r.pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, id)
		return e
	}); err != nil {
		return nil, fmt.Errorf("deleting folder: %w", err)
	}

	if progress != nil {
		progress(orphanTotal, orphanTotal, "Library deletion completed")
	}
	return stats, nil
}

// collectOrphanBatch returns up to limit content IDs whose only library
// membership is the given folder.
func (r *FolderRepository) collectOrphanBatch(ctx context.Context, folderID, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT mil.content_id
		FROM media_item_libraries mil
		WHERE mil.media_folder_id = $1
		AND NOT EXISTS (
			SELECT 1 FROM media_item_libraries other
			WHERE other.content_id = mil.content_id
			AND other.media_folder_id <> $1
		)
		LIMIT $2`, folderID, limit)
	if err != nil {
		return nil, fmt.Errorf("finding orphaned items: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning orphan content_id: %w", err)
		}
		ids = append(ids, contentID)
	}
	return ids, rows.Err()
}

// dirSetToSlice returns the keys of set as a slice (nil if empty).
func dirSetToSlice(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(set))
	for d := range set {
		dirs = append(dirs, d)
	}
	return dirs
}
```

Note: this removes the old single-transaction body (`tx, err := r.pool.Begin(...)`, the inline orphan-collection loop, the `collectImageDirs` call, the CASCADE folder drop, and `tx.Commit`). `collectOrphanIDs` and `Delete` are untouched. `collectImageDirs` remains (still used by `library_repo.go`).

- [ ] **Step 3: Verify the build and vet**

Run: `go build ./... && go vet ./internal/catalog/`
Expected: no errors. If `go vet` flags an unused function, confirm it is genuinely unused before removing — `collectImageDirs`, `collectOrphanIDs`, and `pathDir` must all remain (still referenced).

- [ ] **Step 4: Run the full catalog + adminjob test suites**

Run: `go test ./internal/catalog/... ./internal/adminjob/...`
Expected: PASS. (No DB-backed test exercises `DeleteWithStats`; the helper unit tests and existing tests must stay green.)

- [ ] **Step 5: Lint**

Run: `golangci-lint run ./internal/catalog/...`
Expected: no findings. (If `golangci-lint` is unavailable, run `gofmt -l internal/catalog/` and ensure it prints nothing.)

- [ ] **Step 6: Format and commit**

```bash
gofmt -w internal/catalog/folder_repo.go
git add internal/catalog/folder_repo.go
git commit -m "fix(catalog): delete libraries in deadlock-retrying batches

Replaces the single multi-minute delete transaction with phased, batched
autocommit deletes (orphan items, media files, memberships, folder row),
each retried on deadlock. Holds only short locks, survives concurrent
writers, and is resumable on failure."
```

---

## Task 5: Verify on the dev server

This is the integration test (no catalog DB harness exists). Follow the deployment-debugging runbook. The Audiobooks library (folder id 8) is currently stuck with ~249K items / ~339K files.

- [ ] **Step 1: Deploy the branch to dev**

Run: `make dev-deploy`
Expected: builds `silo:dev-local`, recreates the `silo` service. Then confirm health:
`ssh "$DEV_HOST" 'curl -s http://localhost:8090/api/v1/ready'`

- [ ] **Step 2: Capture the starting counts**

Run (psql via the dev compose `postgres` service, user/db `continuum`):

```sql
SELECT
  (SELECT COUNT(*) FROM media_files WHERE media_folder_id = 8) AS files,
  (SELECT COUNT(*) FROM media_item_libraries WHERE media_folder_id = 8) AS links,
  (SELECT EXISTS (SELECT 1 FROM media_folders WHERE id = 8)) AS folder_exists;
```

Expected: non-zero files/links, `folder_exists = t`.

- [ ] **Step 3: Trigger the deletion**

Re-trigger the library deletion through the normal path (admin UI "delete library" for Audiobooks, or the admin delete endpoint). Confirm a new job is queued:

```sql
SELECT id, job_type, status, requested_at
FROM admin_jobs WHERE job_type = 'delete_library'
ORDER BY requested_at DESC LIMIT 3;
```

- [ ] **Step 4: Monitor to completion**

Watch the job and the draining counts (re-run periodically):

```sql
SELECT id, status, progress_current, progress_total, error_message, heartbeat_at
FROM admin_jobs WHERE job_type = 'delete_library'
ORDER BY requested_at DESC LIMIT 1;
```

```sql
SELECT COUNT(*) FROM media_item_libraries WHERE media_folder_id = 8;
```

Also confirm deadlock retries (if any) are recovered rather than fatal:
`ssh "$DEV_HOST" "$DEV_COMPOSE_CMD logs postgres 2>&1 | grep -i deadlock | tail -20"`
Expected: the job reaches `status = completed`; counts trend to 0; any deadlocks are transient (the job does not fail).

- [ ] **Step 5: Confirm full removal**

```sql
SELECT
  (SELECT COUNT(*) FROM media_files WHERE media_folder_id = 8) AS files,
  (SELECT COUNT(*) FROM media_item_libraries WHERE media_folder_id = 8) AS links,
  (SELECT EXISTS (SELECT 1 FROM media_folders WHERE id = 8)) AS folder_exists;
```

Expected: `files = 0`, `links = 0`, `folder_exists = f`.

- [ ] **Step 6: Report**

Summarize: starting counts, time to completion, number of batches/retries observed, and final state. No commit (verification only).

---

## Self-Review

**Spec coverage:**
- Phased batched deletion (Phases 0–4) → Task 4. ✔
- `retryOnDeadlock` (40P01/40001, 5 attempts, backoff, ctx-aware) → Task 1. ✔
- Batch sizes `orphanDeleteBatch=1000`, `folderChildDeleteBatch=5000` → Task 4 Step 1. ✔
- Autocommit per batch / no long transaction → Task 4 Step 2 (uses `r.pool.Exec`, no `Begin`). ✔
- Stats + progress + heartbeat reliance → Task 4 Step 2; runner heartbeat unchanged. ✔
- Resumability (folder row last, tolerate 0 rows) → Task 4 Phase 4. ✔
- Unit tests for retry + batch loop → Tasks 1–2. ✔
- `collectImageDirs` shared with `library_repo.go` preserved → Task 3 (interface widening), Task 4 Step 3 build check. ✔
- Dev integration verification → Task 5. ✔
- Out of scope (writer coordination, runner/executor changes, schema changes) → not present in any task. ✔

**Placeholder scan:** No TBD/TODO; every code step has complete code; every command has expected output. ✔

**Type consistency:** `retryOnDeadlock(ctx, func() error) error`, `deleteInBatches(ctx, int, func(ctx) (int64,error)) (int64,error)`, `rowQuerier{ Query(ctx,string,...any)(pgx.Rows,error) }`, `collectRawImageDirs`/`collectImageDirs`/`filterUnreferencedImageDirs(ctx, rowQuerier, ...)`, `collectOrphanBatch(ctx,int,int)([]string,error)`, `dirSetToSlice(map[string]struct{})[]string`, consts `orphanDeleteBatch`/`folderChildDeleteBatch`, vars `deadlockMaxAttempts`/`deadlockBaseBackoff` — names used consistently across tasks. ✔
