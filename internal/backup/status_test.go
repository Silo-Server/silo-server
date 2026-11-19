package backup

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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
