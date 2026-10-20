package watchsync

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type admissionOrderRepo struct {
	*serviceFakeRepo
	scheduledEntered  chan struct{}
	releaseScheduled  chan struct{}
	completionEntered chan struct{}
	releaseCompletion chan struct{}
	completed         chan struct{}
}

func (r *admissionOrderRepo) AdmitSyncRun(ctx context.Context, conn Connection, run SyncRun) (syncRunAdmission, error) {
	if run.Trigger == "scheduled" {
		close(r.scheduledEntered)
		select {
		case <-r.releaseScheduled:
		case <-ctx.Done():
			return syncRunAdmission{}, ctx.Err()
		}
	}
	return r.serviceFakeRepo.AdmitSyncRun(ctx, conn, run)
}

func (r *admissionOrderRepo) CompleteSyncRun(ctx context.Context, run SyncRun) (SyncRun, error) {
	close(r.completionEntered)
	select {
	case <-r.releaseCompletion:
	case <-ctx.Done():
		return SyncRun{}, ctx.Err()
	}
	completed, err := r.serviceFakeRepo.CompleteSyncRun(ctx, run)
	close(r.completed)
	return completed, err
}

func TestServiceSyncAdmissionPrecedesLocalGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	repo := &admissionOrderRepo{
		serviceFakeRepo:  newServiceFakeRepo(),
		scheduledEntered: make(chan struct{}), releaseScheduled: make(chan struct{}),
		completionEntered: make(chan struct{}), releaseCompletion: make(chan struct{}), completed: make(chan struct{}),
	}
	releaseScheduled := sync.OnceFunc(func() { close(repo.releaseScheduled) })
	releaseCompletion := sync.OnceFunc(func() { close(repo.releaseCompletion) })
	defer releaseScheduled()
	defer releaseCompletion()
	conn := repo.withConnection(Connection{ID: "synthetic-connection", Provider: "no-transport", UserID: 7, ProfileID: "profile"})
	// An empty registry ensures no provider or plugin transport can run. The
	// worker must reach unknown-provider failure, rather than lose its local guard.
	service := NewService(repo, NewRegistry())
	scheduled := make(chan error, 1)
	go func() { scheduled <- service.SyncConnection(ctx, conn, "scheduled") }()
	select {
	case <-repo.scheduledEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	manual, err := service.RequestManualSync(ctx, conn.UserID, conn.ProfileID, conn.Provider)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-repo.completionEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	releaseScheduled()
	select {
	case err := <-scheduled:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	releaseCompletion()
	select {
	case <-repo.completed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	runs := repo.syncRunsSnapshot()
	if len(runs) != 1 || runs[0].ID != manual.Run.ID || runs[0].Status != string(SyncRunStatusFailed) || !strings.Contains(runs[0].Error, "unknown provider") || runs[0].Warning != "" {
		t.Fatalf("expected the admitted manual worker to execute its terminal path: %+v", runs)
	}
}

type terminalContextRepo struct {
	Repository
	contextErr error
	value      any
	deadline   time.Time
}

type terminalContextKey struct{}

func (r *terminalContextRepo) CompleteSyncRun(ctx context.Context, run SyncRun) (SyncRun, error) {
	r.contextErr = ctx.Err()
	r.value = ctx.Value(terminalContextKey{})
	r.deadline, _ = ctx.Deadline()
	return run, nil
}

func TestServiceSyncCompletionSurvivesCancellationWithBoundedContext(t *testing.T) {
	repo := &terminalContextRepo{}
	service := NewService(repo, NewRegistry())
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), terminalContextKey{}, "synthetic-request"))
	cancel()
	run, err := service.completeSyncRun(ctx, SyncRun{Status: string(SyncRunStatusFailed)})
	if err != nil || run.CompletedAt == nil {
		t.Fatalf("terminal result = %+v, %v", run, err)
	}
	if repo.contextErr != nil || repo.value != "synthetic-request" || repo.deadline.IsZero() || time.Until(repo.deadline) <= 0 || time.Until(repo.deadline) > signalsNotifyTimeout {
		t.Fatalf("terminal context: err=%v value=%v deadline=%v", repo.contextErr, repo.value, repo.deadline)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("provider context must remain canceled")
	}
}
