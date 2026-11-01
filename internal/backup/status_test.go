package backup

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

type recordingStatus struct {
	calls []string
	rec   StatusRecord
}

func (r *recordingStatus) Begin(context.Context, Held, ActiveOperation) (string, error) {
	r.calls = append(r.calls, "begin")
	return "token", nil
}
func (r *recordingStatus) Progress(_ context.Context, _ Held, token string, _ float64, _ string) error {
	r.calls = append(r.calls, "progress "+token)
	return nil
}
func (r *recordingStatus) End(_ context.Context, _ Held, token string, _ *LastRun) error {
	r.calls = append(r.calls, "end "+token)
	return nil
}
func (r *recordingStatus) Read(context.Context) (StatusRecord, error) { return r.rec, nil }

type fakeHeld struct{ lost chan struct{} }

func (h fakeHeld) Lost() <-chan struct{}   { return h.lost }
func (h fakeHeld) Release(context.Context) {}
func (h fakeHeld) Session() LockSession    { return LockSession{PID: 1} }
func (h fakeHeld) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

// TestOperationPublishesOnlyWhileItHoldsTheLock: once the lock's session has
// ended, another server may hold the lock and own the status row, so the
// operation writes nothing more, not even with its own token.
func TestOperationPublishesOnlyWhileItHoldsTheLock(t *testing.T) {
	store := &recordingStatus{}
	held := fakeHeld{lost: make(chan struct{})}
	svc := New(Options{Status: store})
	op := svc.beginOperation(t.Context(), held, ActiveOperation{Operation: OperationBackup})
	op.progress(t.Context(), 10, "working")
	close(held.lost)
	op.progress(t.Context(), 20, "after the lock was lost")
	op.end(t.Context(), &LastRun{})
	if want := []string{"begin", "progress token"}; len(store.calls) != len(want) || store.calls[0] != want[0] || store.calls[1] != want[1] {
		t.Fatalf("status writes = %v, want %v", store.calls, want)
	}
}

// TestRecentBackupWarningSpansDestinations: the 48-hour warning reads the
// latest success at any destination, so switching to a destination with no
// copies yet says that instead, and a backup there clears it.
func TestRecentBackupWarningSpansDestinations(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := now.Add(-2 * time.Hour)
	status := &recordingStatus{rec: StatusRecord{LastSuccessAt: &elsewhere}}
	svc := New(Options{Destination: NewStoreDestination(store), Status: status, Now: func() time.Time { return now },
		ServerID: func(context.Context) (string, error) { return "server", nil }})
	codes := func() []string {
		t.Helper()
		listing, err := svc.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, w := range listing.Warnings {
			if w.Code == WarningNoRecentBackup || w.Code == WarningNoBackupsAtDestination {
				out = append(out, w.Code)
			}
		}
		return out
	}
	if got := codes(); !slices.Equal(got, []string{WarningNoBackupsAtDestination}) {
		t.Fatalf("a recent backup elsewhere: warnings %v, want only %s", got, WarningNoBackupsAtDestination)
	}
	old := now.Add(-72 * time.Hour)
	status.rec.LastSuccessAt = &old
	if got := codes(); !slices.Equal(got, []string{WarningNoBackupsAtDestination, WarningNoRecentBackup}) {
		t.Fatalf("an old backup elsewhere: warnings %v", got)
	}
	putPlainBackup(t, store, "server", FolderName(now.Add(-time.Hour), KindNightly))
	if got := codes(); len(got) != 0 {
		t.Fatalf("a recent backup here: warnings %v", got)
	}
	status.rec.LastSuccessAt = nil
	if got := codes(); len(got) != 0 {
		t.Fatalf("a backup here and no recorded success (a full restore emptied the status): warnings %v", got)
	}
}
