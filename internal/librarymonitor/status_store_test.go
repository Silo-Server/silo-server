package librarymonitor

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func statusStoreDB(t *testing.T) (*pgxpool.Pool, *catalog.FolderRepository) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, catalog.NewFolderRepository(pool)
}

func createStatusLibrary(t *testing.T, repo *catalog.FolderRepository, name string) *models.MediaFolder {
	t.Helper()
	suffix := time.Now().UnixNano()
	folder, err := repo.Create(context.Background(), catalog.CreateFolderInput{
		Paths: []string{fmt.Sprintf("/media/rtmon-status-%s-%d", name, suffix)},
		Type:  "movies",
		Name:  fmt.Sprintf("rtmon-status-%s-%d", name, suffix),
	})
	if err != nil {
		t.Fatalf("create library %s: %v", name, err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), folder.ID) })
	return folder
}

// nodeReports returns the fresh reports of one node keyed by library.
func nodeReports(t *testing.T, store *StatusStore, nodeID string) map[int]NodeReport {
	t.Helper()
	reports, err := store.FreshReports(context.Background())
	if err != nil {
		t.Fatalf("FreshReports: %v", err)
	}
	out := make(map[int]NodeReport)
	for _, r := range reports {
		if r.NodeID == nodeID {
			out[r.LibraryID] = r
		}
	}
	return out
}

func TestStatusStoreReportReplacesNodeRows(t *testing.T) {
	pool, repo := statusStoreDB(t)
	ctx := context.Background()
	store := NewStatusStore(pool)
	a := createStatusLibrary(t, repo, "a")
	b := createStatusLibrary(t, repo, "b")
	node := fmt.Sprintf("node-replace-%d", time.Now().UnixNano())
	other := node + "-other"
	t.Cleanup(func() {
		_ = store.RemoveNode(context.Background(), node)
		_ = store.RemoveNode(context.Background(), other)
	})

	if err := store.Report(ctx, other, []LibraryStatus{{LibraryID: a.ID, State: StateStarting}}); err != nil {
		t.Fatalf("Report other: %v", err)
	}
	err := store.Report(ctx, node, []LibraryStatus{
		{LibraryID: a.ID, State: StateMonitoring, Backend: BackendInotify, Directories: 12},
		{LibraryID: b.ID, State: StateLimitReached, Backend: BackendInotify, Detail: "limit", Directories: 40},
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	got := nodeReports(t, store, node)
	if len(got) != 2 {
		t.Fatalf("rows = %+v, want 2", got)
	}
	if r := got[b.ID]; r.State != StateLimitReached || r.Backend != BackendInotify || r.Detail != "limit" || r.Directories != 40 || r.UpdatedAt.IsZero() {
		t.Fatalf("library b row = %+v", r)
	}
	first := got[a.ID].UpdatedAt

	// A later report upserts the listed library and drops the unlisted one.
	if err := store.Report(ctx, node, []LibraryStatus{{LibraryID: a.ID, State: StateRootUnavailable, Detail: "gone"}}); err != nil {
		t.Fatalf("second Report: %v", err)
	}
	got = nodeReports(t, store, node)
	if len(got) != 1 {
		t.Fatalf("rows after second report = %+v, want only library a", got)
	}
	if r := got[a.ID]; r.State != StateRootUnavailable || r.Backend != "" || r.Detail != "gone" || r.Directories != 0 || r.UpdatedAt.Before(first) {
		t.Fatalf("library a row = %+v", r)
	}

	// An empty report deletes every row of the node and no other node's.
	if err := store.Report(ctx, node, nil); err != nil {
		t.Fatalf("empty Report: %v", err)
	}
	if got := nodeReports(t, store, node); len(got) != 0 {
		t.Fatalf("rows after empty report = %+v", got)
	}
	if got := nodeReports(t, store, other); len(got) != 1 {
		t.Fatalf("other node rows = %+v, want its one row kept", got)
	}
}

func TestStatusStoreReportSkipsMissingAndDuplicateLibraries(t *testing.T) {
	pool, repo := statusStoreDB(t)
	ctx := context.Background()
	store := NewStatusStore(pool)
	a := createStatusLibrary(t, repo, "dup")
	node := fmt.Sprintf("node-dup-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = store.RemoveNode(context.Background(), node) })

	var missing int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) + 1000 FROM media_folders`).Scan(&missing); err != nil {
		t.Fatalf("pick missing id: %v", err)
	}
	err := store.Report(ctx, node, []LibraryStatus{
		{LibraryID: a.ID, State: StateStarting},
		{LibraryID: missing, State: StateMonitoring},
		{LibraryID: a.ID, State: StateMonitoring, Backend: BackendInotify},
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	got := nodeReports(t, store, node)
	if len(got) != 1 || got[a.ID].State != StateMonitoring || got[a.ID].Backend != BackendInotify {
		t.Fatalf("rows = %+v, want one monitoring row for library a", got)
	}
}

func TestStatusStoreRemoveNode(t *testing.T) {
	pool, repo := statusStoreDB(t)
	ctx := context.Background()
	store := NewStatusStore(pool)
	a := createStatusLibrary(t, repo, "remove")
	node := fmt.Sprintf("node-remove-%d", time.Now().UnixNano())
	other := node + "-other"
	t.Cleanup(func() { _ = store.RemoveNode(context.Background(), other) })

	for _, n := range []string{node, other} {
		if err := store.Report(ctx, n, []LibraryStatus{{LibraryID: a.ID, State: StateMonitoring}}); err != nil {
			t.Fatalf("Report %s: %v", n, err)
		}
	}
	if err := store.RemoveNode(ctx, node); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}
	if got := nodeReports(t, store, node); len(got) != 0 {
		t.Fatalf("removed node rows = %+v", got)
	}
	if got := nodeReports(t, store, other); len(got) != 1 {
		t.Fatalf("other node rows = %+v", got)
	}
	if err := store.RemoveNode(ctx, node); err != nil {
		t.Fatalf("RemoveNode twice: %v", err)
	}
}

func TestStatusStoreFreshReportsIgnoresStaleRows(t *testing.T) {
	pool, repo := statusStoreDB(t)
	ctx := context.Background()
	store := NewStatusStore(pool)
	a := createStatusLibrary(t, repo, "stale")
	fresh := fmt.Sprintf("node-fresh-%d", time.Now().UnixNano())
	stale := fresh + "-stale"
	t.Cleanup(func() {
		_ = store.RemoveNode(context.Background(), fresh)
		_ = store.RemoveNode(context.Background(), stale)
	})

	for _, n := range []string{fresh, stale} {
		if err := store.Report(ctx, n, []LibraryStatus{{LibraryID: a.ID, State: StateMonitoring}}); err != nil {
			t.Fatalf("Report %s: %v", n, err)
		}
	}
	// Just inside and just outside the window.
	if _, err := pool.Exec(ctx, `UPDATE library_monitor_status SET updated_at = now() - interval '2 minutes 50 seconds' WHERE node_id = $1`, fresh); err != nil {
		t.Fatalf("age fresh row: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE library_monitor_status SET updated_at = now() - interval '3 minutes 1 second' WHERE node_id = $1`, stale); err != nil {
		t.Fatalf("age stale row: %v", err)
	}
	if got := nodeReports(t, store, fresh); len(got) != 1 {
		t.Fatalf("fresh node rows = %+v, want 1", got)
	}
	if got := nodeReports(t, store, stale); len(got) != 0 {
		t.Fatalf("stale node rows = %+v, want none", got)
	}

	// A refresh makes the stale row fresh again.
	if err := store.Report(ctx, stale, []LibraryStatus{{LibraryID: a.ID, State: StateMonitoring}}); err != nil {
		t.Fatalf("refresh Report: %v", err)
	}
	if got := nodeReports(t, store, stale); len(got) != 1 {
		t.Fatalf("refreshed node rows = %+v, want 1", got)
	}
}

func TestStatusReaderSnapshot(t *testing.T) {
	pool, repo := statusStoreDB(t)
	ctx := context.Background()
	store := NewStatusStore(pool)
	a := createStatusLibrary(t, repo, "reader")
	node := fmt.Sprintf("node-reader-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = store.RemoveNode(context.Background(), node) })
	if err := store.Report(ctx, node, []LibraryStatus{{LibraryID: a.ID, State: StateMonitoring}}); err != nil {
		t.Fatalf("Report: %v", err)
	}

	reader := &StatusReader{Store: store, Folders: repo, ServerEnabled: func() bool { return false }}
	snap, err := reader.RealtimeMonitoringStatus(ctx)
	if err != nil {
		t.Fatalf("RealtimeMonitoringStatus: %v", err)
	}
	if snap.ServerEnabled {
		t.Fatal("ServerEnabled = true, want the live value false")
	}
	foundLibrary := false
	for _, f := range snap.Libraries {
		foundLibrary = foundLibrary || f.ID == a.ID
	}
	foundReport := false
	for _, r := range snap.Reports {
		foundReport = foundReport || (r.NodeID == node && r.LibraryID == a.ID)
	}
	if !foundLibrary || !foundReport {
		t.Fatalf("snapshot misses library %d (%v) or its report (%v)", a.ID, foundLibrary, foundReport)
	}
}
