package database

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

type fakeMigrationStepper struct {
	statuses      []*goose.MigrationStatus
	statusStarted chan struct{}
	statusRelease chan struct{}
	statusErr     error
	pendingErr    error
	// steps are returned by successive ApplyVersion calls.
	steps    []fakeMigrationStep
	calls    int
	versions []int64
}

type fakeMigrationStep struct {
	result  *goose.MigrationResult
	err     error
	started chan struct{}
	release chan struct{}
}

func (f *fakeMigrationStepper) Status(ctx context.Context) ([]*goose.MigrationStatus, error) {
	if f.statusStarted != nil {
		close(f.statusStarted)
	}
	if f.statusRelease != nil {
		select {
		case <-f.statusRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.statuses, f.statusErr
}

func (f *fakeMigrationStepper) HasPending(context.Context) (bool, error) {
	if f.pendingErr != nil {
		return false, f.pendingErr
	}
	for _, status := range f.statuses {
		if status != nil && status.State == goose.StatePending {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeMigrationStepper) ApplyVersion(ctx context.Context, version int64, direction bool) (*goose.MigrationResult, error) {
	if !direction {
		panic("expected an up migration")
	}
	f.versions = append(f.versions, version)
	if f.calls >= len(f.steps) {
		f.calls++
		return nil, goose.ErrAlreadyApplied
	}
	step := f.steps[f.calls]
	f.calls++
	if step.started != nil {
		close(step.started)
	}
	if step.release != nil {
		select {
		case <-step.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return step.result, step.err
}

// syncBuffer lets the heartbeat goroutine and the test share a log buffer.
type syncBuffer struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	writes chan string
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.writes != nil {
		select {
		case b.writes <- string(p):
		default:
		}
	}
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newMigrationTestLogger() (*slog.Logger, *syncBuffer) {
	var out syncBuffer
	return slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})), &out
}

func source(version int64, name string) *goose.Source {
	return &goose.Source{Type: goose.TypeSQL, Path: "migrations/sql/" + name, Version: version}
}

func TestApplyMigrationsLoggedPreservesGoosePendingCheck(t *testing.T) {
	boom := errors.New("applied migration source is missing")
	stepper := &fakeMigrationStepper{
		statuses:   []*goose.MigrationStatus{{Source: source(201, "201_pending.sql"), State: goose.StatePending}},
		pendingErr: boom,
	}
	logger, _ := newMigrationTestLogger()
	err := applyMigrationsLogged(t.Context(), stepper, logger, 0)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want pending check error", err)
	}
	if stepper.calls != 0 {
		t.Fatalf("applied migrations after pending check failed: %d calls", stepper.calls)
	}
}

func TestApplyMigrationsLoggedHeartbeatsWhileAMigrationRuns(t *testing.T) {
	slow := source(301, "301_slow_backfill.sql")
	stepStarted := make(chan struct{})
	stepRelease := make(chan struct{})
	stepper := &fakeMigrationStepper{
		statuses: []*goose.MigrationStatus{{Source: slow, State: goose.StatePending}},
		steps: []fakeMigrationStep{
			{result: &goose.MigrationResult{Source: slow}, started: stepStarted, release: stepRelease},
		},
	}
	logger, out := newMigrationTestLogger()
	out.writes = make(chan string, 16)
	done := make(chan error, 1)
	go func() {
		done <- applyMigrationsLogged(t.Context(), stepper, logger, 10*time.Millisecond)
	}()
	<-stepStarted
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case line := <-out.writes:
			if strings.Contains(line, `msg="database migration still running" version=301 name=301_slow_backfill.sql progress=1/1 elapsed=`) {
				close(stepRelease)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				return
			}
		case <-deadline.C:
			close(stepRelease)
			<-done
			t.Fatalf("no heartbeat naming the running migration:\n%s", out.String())
		}
	}
}

func TestApplyMigrationsLoggedStopsWhenAnotherNodeFinished(t *testing.T) {
	first := source(501, "501_first.sql")
	second := source(502, "502_second.sql")
	stepper := &fakeMigrationStepper{
		statuses: []*goose.MigrationStatus{
			{Source: first, State: goose.StatePending},
			{Source: second, State: goose.StatePending},
		},
		steps: []fakeMigrationStep{
			{err: goose.ErrAlreadyApplied},
			{result: &goose.MigrationResult{Source: second}},
		},
	}
	logger, out := newMigrationTestLogger()
	if err := applyMigrationsLogged(t.Context(), stepper, logger, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `msg="database migrations finished" applied=1`) {
		t.Fatalf("logs:\n%s", out.String())
	}
	if !slices.Equal(stepper.versions, []int64{501, 502}) {
		t.Fatalf("versions = %v, want [501 502]", stepper.versions)
	}
	if !strings.Contains(out.String(), `msg="database migration already applied" version=501`) ||
		!strings.Contains(out.String(), `msg="database migration applied" version=502`) {
		t.Fatalf("concurrent completion logged incorrectly:\n%s", out.String())
	}
}

func TestApplyMigrationsLoggedHeartbeatsWhileStatusWaits(t *testing.T) {
	statusStarted := make(chan struct{})
	statusRelease := make(chan struct{})
	item := source(601, "601_pending.sql")
	stepper := &fakeMigrationStepper{
		statuses:      []*goose.MigrationStatus{{Source: item, State: goose.StatePending}},
		statusStarted: statusStarted,
		statusRelease: statusRelease,
		steps:         []fakeMigrationStep{{result: &goose.MigrationResult{Source: item}}},
	}
	logger, out := newMigrationTestLogger()
	out.writes = make(chan string, 16)
	done := make(chan error, 1)
	go func() {
		done <- applyMigrationsLogged(t.Context(), stepper, logger, 10*time.Millisecond)
	}()
	<-statusStarted

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case line := <-out.writes:
			if strings.Contains(line, "database migration status check still running") {
				close(statusRelease)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				return
			}
		case <-deadline.C:
			close(statusRelease)
			<-done
			t.Fatal("no heartbeat while migration status waited for the lock")
		}
	}
}

func TestMigrationRollbackCandidatesListAppliedMigrationsAboveTheTarget(t *testing.T) {
	statuses := []*goose.MigrationStatus{
		{Source: source(100, "100_keep.sql"), State: goose.StateApplied},
		{Source: source(103, "103_newest.sql"), State: goose.StateApplied},
		{Source: source(101, "101_old.sql"), State: goose.StateApplied},
		{Source: source(102, "102_pending.sql"), State: goose.StatePending},
		nil,
	}
	plan := migrationRollbackCandidates(statuses, 100)
	names := migrationNames(plan)
	if len(names) != 2 || names[0] != "103_newest.sql" || names[1] != "101_old.sql" {
		t.Fatalf("plan = %v, want the applied migrations above 100, newest first", names)
	}
	if got := migrationRollbackCandidates(statuses, 103); len(got) != 0 {
		t.Fatalf("plan above the newest applied = %v, want none", migrationNames(got))
	}
}
