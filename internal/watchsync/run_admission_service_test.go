package watchsync

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type missingAdmissionRepo struct{ Repository }

func (r *missingAdmissionRepo) AdmitSyncRun(context.Context, Connection, SyncRun) (syncRunAdmission, error) {
	return syncRunAdmission{}, fmt.Errorf("admit watch provider sync run: %w", ErrConnectionNotFound)
}

func TestServiceManualSyncPreservesMissingConnectionError(t *testing.T) {
	service := NewService(&missingAdmissionRepo{}, NewRegistry())
	_, err := service.RequestManualSync(t.Context(), 7, "synthetic-profile", "no-transport")
	if !errors.Is(err, ErrConnectionNotFound) || err.Error() != ErrConnectionNotFound.Error() {
		t.Fatalf("missing connection error = %v", err)
	}
}

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
