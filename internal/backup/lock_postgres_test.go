package backup

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type recordingStarter struct{ calls int }

func (r *recordingStarter) StartTask(string) (taskmanager.TaskInfo, error) {
	r.calls++
	return taskmanager.TaskInfo{}, nil
}

// TestSubsystemLockSerializesBackupsAndDeletesDB: while one session holds the
// subsystem lock (a backup or restore on any server), deleting, "Back up
// now", and a scheduled run all refuse without touching the destination; and
// concurrent attempts to take the lock admit exactly one.
func TestSubsystemLockSerializesBackupsAndDeletesDB(t *testing.T) {
	pool, _ := testDB(t)
	ctx := t.Context()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serverID := "lock-" + randomHex(t, 4)
	id := FolderName(time.Date(2026, 10, 30, 4, 0, 0, 0, time.UTC), KindNightly)
	manifest, _ := json.Marshal(Manifest{Format: manifestFormat, Version: manifestVersion, ID: id, Kind: KindNightly, ServerID: serverID})
	if err := store.Put(ctx, objectKey(serverID, id, ManifestName), manifest); err != nil {
		t.Fatal(err)
	}
	locker := PostgresLocker{Pool: pool}
	starter := &recordingStarter{}
	svc := New(Options{
		Pool: pool, Stores: blobstore.Stores{Assets: store, Operational: store}, Destination: NewStoreDestination(store),
		Locker: locker, ServerID: func(context.Context) (string, error) { return serverID, nil },
		FindTools: func(context.Context) (Tools, error) { return Tools{}, ErrToolMissing },
	})
	svc.SetTaskStarter(starter)

	other, err := locker.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, busy, err := locker.Holder(ctx); err != nil || !busy {
		t.Fatalf("Busy = %v, %v while held", busy, err)
	}
	if err := svc.Delete(ctx, id); !errors.Is(err, ErrBusy) {
		t.Fatalf("Delete while locked = %v, want ErrBusy", err)
	}
	if _, err := svc.StartManual(ctx); !errors.Is(err, ErrBusy) || starter.calls != 0 {
		t.Fatalf("StartManual while locked = %v (task started %d times), want ErrBusy", err, starter.calls)
	}
	result, err := svc.RunTask(ctx, nil)
	if err != nil || result.Skipped != SkippedBusy {
		t.Fatalf("scheduled run while locked = %+v, %v; want a skip", result, err)
	}
	if _, err := store.Stat(ctx, objectKey(serverID, id, ManifestName)); err != nil {
		t.Fatalf("the backup is gone after refused operations: %v", err)
	}
	other.Release(ctx)
	if _, busy, err := locker.Holder(ctx); err != nil || busy {
		t.Fatalf("Busy = %v, %v after release", busy, err)
	}
	if err := svc.Delete(ctx, id); err != nil {
		t.Fatalf("Delete after release: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []Held
	busy := 0
	for range 8 {
		wg.Go(func() {
			held, err := locker.TryLock(ctx)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, held)
			case errors.Is(err, ErrBusy):
				busy++
			default:
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(winners) != 1 || busy != 7 {
		t.Fatalf("concurrent TryLock: %d winners, %d busy; want 1 and 7", len(winners), busy)
	}
	winners[0].Release(ctx)
}

// TestLostLockSessionCancelsWorkDB: when the lock's session dies, the work
// holding it is canceled, because another server may already hold the lock.
func TestLostLockSessionCancelsWorkDB(t *testing.T) {
	pool, _ := testDB(t)
	ctx := t.Context()
	held, err := PostgresLocker{Pool: pool, CheckInterval: 20 * time.Millisecond}.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release(ctx)
	work, cancel := withLost(ctx, held)
	defer cancel()
	// Only this lock's session: other packages' database tests hold the same
	// key in their own databases at the same time.
	if _, err := pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, held.Session().PID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-work.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("work kept running after its lock session ended")
	}
}

// TestListReportsThePublishedOperationDB: the list believes the active
// operation only while the backend it names holds the lock. A delete holding
// the lock does not make an abandoned folder look in progress; a backup does,
// with its progress, on any server; and a row whose holder died reads as an
// interrupted run, even while an unrelated operation holds the lock.
func TestListReportsThePublishedOperationDB(t *testing.T) {
	pool, _ := testDB(t)
	ctx := t.Context()
	resetStatus := func() { _, _ = pool.Exec(context.Background(), `DELETE FROM backup_status`) }
	resetStatus()
	t.Cleanup(resetStatus)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serverID := "status-" + randomHex(t, 4)
	id := FolderName(time.Date(2026, 10, 30, 4, 0, 0, 0, time.UTC), KindNightly)
	if err := store.Put(ctx, objectKey(serverID, id, DumpName), []byte("partial")); err != nil {
		t.Fatal(err)
	}
	status := PostgresStatus{Pool: pool}
	locker := PostgresLocker{Pool: pool}
	// Another API server's view.
	reader := New(Options{
		Pool: pool, Stores: blobstore.Stores{Assets: store, Operational: store}, Destination: NewStoreDestination(store),
		Locker: locker, Status: status, ServerID: func(context.Context) (string, error) { return serverID, nil },
		FindTools: func(context.Context) (Tools, error) { return Tools{}, ErrToolMissing },
	})
	list := func() Listing {
		t.Helper()
		l, err := reader.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Backups) != 1 {
			t.Fatalf("backups = %+v", l.Backups)
		}
		return l
	}

	held, err := locker.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := status.Begin(ctx, held, ActiveOperation{Operation: OperationDelete, BackupID: "20261029T040000Z-nightly", StartedAt: time.Now(), Lock: held.Session()}); err != nil {
		t.Fatal(err)
	}
	if l := list(); l.Backups[0].Status != StatusIncomplete || l.Active == nil || l.Active.Operation != OperationDelete {
		t.Fatalf("during a delete: status %s, active %+v", l.Backups[0].Status, l.Active)
	}
	token, err := status.Begin(ctx, held, ActiveOperation{Operation: OperationBackup, BackupID: id, Kind: KindNightly, StartedAt: time.Now(), Lock: held.Session()})
	if err != nil {
		t.Fatal(err)
	}
	if err := status.Progress(ctx, held, token, 40, "Dumping the database"); err != nil {
		t.Fatal(err)
	}
	l := list()
	if l.Backups[0].Status != StatusInProgress || l.Active == nil || l.Active.Progress != 40 || l.Active.Message != "Dumping the database" {
		t.Fatalf("during the backup: status %s, active %+v", l.Backups[0].Status, l.Active)
	}
	held.Release(ctx) // the holder dies without ending its run
	if l := list(); l.Backups[0].Status != StatusIncomplete || l.Active != nil || l.LastRun == nil || l.LastRun.ID != id || l.LastRun.Status != RunInterrupted {
		t.Fatalf("after the holder died: status %s, active %+v, last %+v", l.Backups[0].Status, l.Active, l.LastRun)
	}
	// An unrelated holder takes the lock but has not published yet: the
	// stale row still names another backend, so it is not believed.
	other, err := locker.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release(ctx)
	if l := list(); l.Backups[0].Status != StatusIncomplete || l.Active != nil || l.LastRun == nil || l.LastRun.Status != RunInterrupted {
		t.Fatalf("with an unrelated holder: status %s, active %+v, last %+v", l.Backups[0].Status, l.Active, l.LastRun)
	}
}

// TestStatusBelongsToTheOperationHoldingTheLockDB: A loses the lock while its
// session lives on, and B takes it. B's Begin records A's backup as
// interrupted before publishing its own; A's delayed Begin, progress, and end
// then change nothing, because every status write runs on its holder's
// session and requires that session to hold the lock.
func TestStatusBelongsToTheOperationHoldingTheLockDB(t *testing.T) {
	pool, _ := testDB(t)
	ctx := t.Context()
	resetStatus := func() { _, _ = pool.Exec(context.Background(), `DELETE FROM backup_status`) }
	resetStatus()
	t.Cleanup(resetStatus)
	status := PostgresStatus{Pool: pool}
	locker := PostgresLocker{Pool: pool}
	started := time.Date(2026, 10, 30, 4, 0, 0, 0, time.UTC)

	a, err := locker.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	staleOp := ActiveOperation{Operation: OperationBackup, BackupID: FolderName(started, KindNightly), Kind: KindNightly, StartedAt: started, Lock: a.Session()}
	staleToken, err := status.Begin(ctx, a, staleOp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, `SELECT pg_advisory_unlock($1)`, SubsystemLockKey); err != nil {
		t.Fatal(err)
	}
	b, err := locker.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release(ctx)
	next := ActiveOperation{Operation: OperationBackup, BackupID: FolderName(started.Add(time.Hour), KindManual), Kind: KindManual, StartedAt: started.Add(time.Hour), Lock: b.Session()}
	nextToken, err := status.Begin(ctx, b, next)
	if err != nil || nextToken == staleToken {
		t.Fatalf("B's Begin: %v, token reused %v", err, nextToken == staleToken)
	}
	rec, err := status.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if l := rec.Last; l == nil || l.ID != staleOp.BackupID || l.Kind != KindNightly || l.Status != RunInterrupted ||
		!l.StartedAt.Equal(started) || !l.FinishedAt.IsZero() || l.Error != interruptedRunError {
		t.Fatalf("reconciled last run = %+v, want A's backup, interrupted", rec.Last)
	}
	if rec.Active == nil || rec.Active.BackupID != next.BackupID || !rec.Active.Lock.Same(b.Session()) {
		t.Fatalf("active = %+v", rec.Active)
	}

	// A wakes up: a delayed Begin, then its old token's progress and end.
	if _, err := status.Begin(ctx, a, staleOp); !errors.Is(err, ErrNotLockHolder) {
		t.Fatalf("A's delayed Begin = %v, want ErrNotLockHolder", err)
	}
	if err := status.Progress(ctx, a, staleToken, 99, "stale"); err != nil {
		t.Fatal(err)
	}
	if err := status.End(ctx, a, staleToken, &LastRun{ID: staleOp.BackupID, Kind: KindNightly, Status: string(StatusComplete), StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	// B's token reaching A's session must not apply either.
	if err := status.End(ctx, a, nextToken, nil); err != nil {
		t.Fatal(err)
	}
	after, err := status.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Active == nil || after.Active.BackupID != next.BackupID || after.Active.Progress != 0 || after.Last == nil || after.Last.Status != RunInterrupted {
		t.Fatalf("a stale holder changed the row: active %+v, last %+v", after.Active, after.Last)
	}
	if err := status.End(ctx, b, nextToken, nil); err != nil {
		t.Fatal(err)
	}
	if done, _ := status.Read(ctx); done.Active != nil {
		t.Fatalf("the holder's own End did not clear its operation: %+v", done.Active)
	}
}

// TestStatusRecordsTheLastSuccessDB: a run that succeeded records its finish
// as the last success, at whichever destination it wrote; a failed run after
// it is the latest run and leaves the last success.
func TestStatusRecordsTheLastSuccessDB(t *testing.T) {
	pool, _ := testDB(t)
	ctx := t.Context()
	resetStatus := func() { _, _ = pool.Exec(context.Background(), `DELETE FROM backup_status`) }
	resetStatus()
	t.Cleanup(resetStatus)
	status := PostgresStatus{Pool: pool}
	held, err := PostgresLocker{Pool: pool}.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release(ctx)
	started := time.Date(2026, 10, 30, 4, 0, 0, 0, time.UTC)
	run := func(outcome string, finished time.Time) {
		t.Helper()
		token, err := status.Begin(ctx, held, ActiveOperation{Operation: OperationBackup, BackupID: "b", Kind: KindNightly, StartedAt: started, Lock: held.Session()})
		if err != nil {
			t.Fatal(err)
		}
		if err := status.End(ctx, held, token, &LastRun{ID: "b", Kind: KindNightly, Status: outcome, StartedAt: started, FinishedAt: finished}); err != nil {
			t.Fatal(err)
		}
	}
	succeeded := started.Add(time.Hour)
	run(string(StatusCompleteWithWarnings), succeeded)
	run(RunFailed, succeeded.Add(time.Hour))
	rec, err := status.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Last == nil || rec.Last.Status != RunFailed || rec.LastSuccessAt == nil || !rec.LastSuccessAt.Equal(succeeded) {
		t.Fatalf("last run %+v, last success %v; want the failure, and the success before it", rec.Last, rec.LastSuccessAt)
	}
}
