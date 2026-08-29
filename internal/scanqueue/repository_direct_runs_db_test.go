package scanqueue

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func openDirectRunTestRepository(t *testing.T) (context.Context, *pgxpool.Pool, *Repository, int) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	name := fmt.Sprintf("scanqueue-direct-run-%d", time.Now().UnixNano())
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('series', $1, true)
		RETURNING id`,
		name,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	return ctx, pool, NewRepository(pool), folderID
}

func createDirectRunTestRow(t *testing.T, ctx context.Context, repo *Repository, folderID int, path, trigger string) string {
	t.Helper()
	run, created, err := repo.Create(ctx, CreateInput{
		LibraryID: folderID,
		Mode:      ModeSubtree,
		Path:      path,
		Trigger:   trigger,
	})
	if err != nil {
		t.Fatalf("create scan run: %v", err)
	}
	if !created {
		t.Fatalf("scan run for %q was not created", path)
	}
	return run.ID
}

func TestClaimNextAcceptedSkipsDirectAdminRuns(t *testing.T) {
	ctx, _, repo, folderID := openDirectRunTestRepository(t)
	directID := createDirectRunTestRow(t, ctx, repo, folderID, "/direct", TriggerAdminItemRefresh)
	queuedID := createDirectRunTestRow(t, ctx, repo, folderID, "/queued", "manual")

	claimed, err := repo.ClaimNextAccepted(ctx, 1, 1)
	if err != nil {
		t.Fatalf("claim next accepted: %v", err)
	}
	if claimed == nil || claimed.ID != queuedID {
		t.Fatalf("claimed run = %#v, want queued run %q", claimed, queuedID)
	}
	direct, err := repo.GetByID(ctx, directID)
	if err != nil {
		t.Fatalf("load direct run: %v", err)
	}
	if direct.Status != StatusAccepted {
		t.Fatalf("direct run status = %q, want %q", direct.Status, StatusAccepted)
	}
}

func TestStaleJanitorFailsDirectRunsAndRequeuesQueuedRuns(t *testing.T) {
	ctx, pool, repo, folderID := openDirectRunTestRepository(t)
	directID := createDirectRunTestRow(t, ctx, repo, folderID, "/direct", TriggerAdminLibraryRefresh)
	acceptedDirectID := createDirectRunTestRow(t, ctx, repo, folderID, "/accepted-direct", TriggerAdminItemRefresh)
	queuedID := createDirectRunTestRow(t, ctx, repo, folderID, "/queued", "manual")

	for _, id := range []string{directID, queuedID} {
		if _, err := repo.Start(ctx, id); err != nil {
			t.Fatalf("start scan run %q: %v", id, err)
		}
	}
	staleAt := time.Now().UTC().Add(-10 * time.Minute)
	if _, err := pool.Exec(ctx, `
		UPDATE scan_runs
		SET started_at = $2, heartbeat_at = $2, updated_at = $2
		WHERE id = ANY($1)`,
		[]string{directID, queuedID},
		staleAt,
	); err != nil {
		t.Fatalf("make started scan runs stale: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE scan_runs
		SET requested_at = $2, started_at = NULL, heartbeat_at = NULL, updated_at = $2
		WHERE id = $1`,
		acceptedDirectID,
		staleAt,
	); err != nil {
		t.Fatalf("make accepted direct scan run stale: %v", err)
	}

	service := NewService(repo, nil, nil, nil, ctx, 1, 1)
	service.staleAfter = 2 * time.Minute
	service.requeueStale()

	direct, err := repo.GetByID(ctx, directID)
	if err != nil {
		t.Fatalf("load direct run: %v", err)
	}
	if direct.Status != StatusFailed || direct.ErrorMessage != "abandoned direct scan run" {
		t.Fatalf("direct run status/error = %q/%q, want %q/%q", direct.Status, direct.ErrorMessage, StatusFailed, "abandoned direct scan run")
	}
	acceptedDirect, err := repo.GetByID(ctx, acceptedDirectID)
	if err != nil {
		t.Fatalf("load accepted direct run: %v", err)
	}
	if acceptedDirect.Status != StatusFailed || acceptedDirect.ErrorMessage != "abandoned direct scan run" {
		t.Fatalf("accepted direct run status/error = %q/%q, want %q/%q", acceptedDirect.Status, acceptedDirect.ErrorMessage, StatusFailed, "abandoned direct scan run")
	}
	if replacement := createDirectRunTestRow(t, ctx, repo, folderID, "/accepted-direct", TriggerAdminItemRefresh); replacement == acceptedDirectID {
		t.Fatalf("replacement direct run reused stale ID %q", replacement)
	}

	queued, err := repo.GetByID(ctx, queuedID)
	if err != nil {
		t.Fatalf("load queued run: %v", err)
	}
	if queued.Status != StatusAccepted {
		t.Fatalf("queued run status = %q, want %q", queued.Status, StatusAccepted)
	}
}

func TestStaleJanitorLeavesHeartbeatingDirectRunRunning(t *testing.T) {
	ctx, pool, repo, folderID := openDirectRunTestRepository(t)
	directID := createDirectRunTestRow(t, ctx, repo, folderID, "/direct", TriggerAdminItemRefresh)
	queuedID := createDirectRunTestRow(t, ctx, repo, folderID, "/queued", "manual")

	for _, id := range []string{directID, queuedID} {
		if _, err := repo.Start(ctx, id); err != nil {
			t.Fatalf("start scan run %q: %v", id, err)
		}
	}
	staleAt := time.Now().UTC().Add(-10 * time.Minute)
	if _, err := pool.Exec(ctx, `
		UPDATE scan_runs
		SET started_at = $2, heartbeat_at = $2, updated_at = $2
		WHERE id = ANY($1)`,
		[]string{directID, queuedID},
		staleAt,
	); err != nil {
		t.Fatalf("make scan runs stale: %v", err)
	}
	if err := repo.TouchHeartbeat(ctx, directID); err != nil {
		t.Fatalf("touch direct heartbeat: %v", err)
	}

	service := NewService(repo, nil, nil, nil, ctx, 1, 1)
	service.staleAfter = 2 * time.Minute
	service.requeueStale()

	direct, err := repo.GetByID(ctx, directID)
	if err != nil {
		t.Fatalf("load direct run: %v", err)
	}
	if direct.Status != StatusRunning {
		t.Fatalf("direct run status = %q, want %q", direct.Status, StatusRunning)
	}
	queued, err := repo.GetByID(ctx, queuedID)
	if err != nil {
		t.Fatalf("load queued run: %v", err)
	}
	if queued.Status != StatusAccepted {
		t.Fatalf("queued run status = %q, want %q", queued.Status, StatusAccepted)
	}
}
