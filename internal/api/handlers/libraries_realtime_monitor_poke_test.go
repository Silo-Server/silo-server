package handlers

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type countingMonitorPoker struct{ pokes atomic.Int32 }

func (p *countingMonitorPoker) Poke() { p.pokes.Add(1) }

type noopLibraryScanQueue struct{}

func (noopLibraryScanQueue) EnqueueLibraryScan(context.Context, int, string) (bool, error) {
	return true, nil
}

func (noopLibraryScanQueue) EnqueueScan(context.Context, int, string, string, string) (bool, error) {
	return true, nil
}

func (noopLibraryScanQueue) CancelAcceptedByLibrary(context.Context, int) (int, error) { return 0, nil }

func (noopLibraryScanQueue) CancelByLibrary(context.Context, int) (int, error) { return 0, nil }

// deletionJobCreator queues library deletions the way the real repository
// does as far as monitoring cares: the library is disabled.
type deletionJobCreator struct {
	fakeAdminJobCreator
	pool *pgxpool.Pool
}

func (d *deletionJobCreator) CreateLibraryDeletion(ctx context.Context, _ int, req adminjob.DeleteLibraryRequest) (*models.AdminJob, error) {
	if _, err := d.pool.Exec(ctx, `UPDATE media_folders SET enabled = false WHERE id = $1`, req.LibraryID); err != nil {
		return nil, err
	}
	return &models.AdminJob{ID: "job-delete"}, nil
}

// A library create, update or delete handled on this node reconciles
// real-time monitoring immediately; a failed mutation does not, and a
// handler without a monitor still works.
func TestLibraryMutationsPokeRealtimeMonitor(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(pool.Close)
	repo := catalog.NewFolderRepository(pool)

	poker := &countingMonitorPoker{}
	h := NewLibraryHandler(repo, nil, nil, pool, nil)
	h.ScanQueue = noopLibraryScanQueue{}
	h.JobRepo = &deletionJobCreator{pool: pool}
	h.RealtimeMonitor = poker

	suffix := time.Now().UnixNano()
	created, err := h.CreateLibrary(ctx, LibraryCreateRequest{
		Paths: []string{fmt.Sprintf("/media/rtmon-poke-%d", suffix)},
		Type:  "movies",
		Name:  fmt.Sprintf("rtmon-poke-%d", suffix),
	})
	if err != nil {
		t.Fatalf("CreateLibrary: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), created.ID) })
	if got := poker.pokes.Load(); got != 1 {
		t.Fatalf("pokes after create = %d, want 1", got)
	}

	off := false
	if _, err := h.UpdateLibrary(ctx, created.ID, 0, LibraryUpdateRequest{RealtimeMonitoring: &off}); err != nil {
		t.Fatalf("UpdateLibrary: %v", err)
	}
	if got := poker.pokes.Load(); got != 2 {
		t.Fatalf("pokes after update = %d, want 2", got)
	}

	var missing int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) + 1000 FROM media_folders`).Scan(&missing); err != nil {
		t.Fatalf("pick missing id: %v", err)
	}
	name := "x"
	if _, err := h.UpdateLibrary(ctx, missing, 0, LibraryUpdateRequest{Name: &name}); err == nil {
		t.Fatal("UpdateLibrary of a missing library succeeded")
	}
	if got := poker.pokes.Load(); got != 2 {
		t.Fatalf("pokes after a failed update = %d, want 2", got)
	}

	if _, err := h.DeleteLibrary(ctx, created.ID, 0); err != nil {
		t.Fatalf("DeleteLibrary: %v", err)
	}
	if got := poker.pokes.Load(); got != 3 {
		t.Fatalf("pokes after delete = %d, want 3", got)
	}

	h.RealtimeMonitor = nil
	if _, err := h.UpdateLibrary(ctx, created.ID, 0, LibraryUpdateRequest{Name: &name}); err != nil {
		t.Fatalf("UpdateLibrary without a monitor: %v", err)
	}
}
