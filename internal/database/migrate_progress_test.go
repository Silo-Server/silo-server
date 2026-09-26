package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestApplyMigrationsLoggedLogsEachMigration(t *testing.T) {
	first, second := source(201, "201_first.sql"), source(202, "202_second.sql")
	stepper := &fakeMigrationStepper{
		statuses: []*goose.MigrationStatus{
			{Source: source(200, "200_done.sql"), State: goose.StateApplied},
			{Source: first, State: goose.StatePending},
			{Source: second, State: goose.StatePending},
		},
		steps: []fakeMigrationStep{
			{result: &goose.MigrationResult{Source: first, Duration: 1500 * time.Millisecond}},
			{result: &goose.MigrationResult{Source: second, Duration: 20 * time.Millisecond}},
		},
	}
	logger, out := newMigrationTestLogger()

	if err := applyMigrationsLogged(t.Context(), stepper, logger, 0); err != nil {
		t.Fatal(err)
	}

	logs := out.String()
	for _, want := range []string{
		`msg="applying database migrations" pending=2 from_version=201 to_version=202`,
		`msg="applying database migration" version=201 name=201_first.sql progress=1/2`,
		`msg="database migration applied" version=201 name=201_first.sql progress=1/2 duration=1.5s`,
		`msg="database migration applied" version=202 name=202_second.sql progress=2/2 duration=20ms`,
		`msg="database migrations finished" applied=2`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("missing %q in logs:\n%s", want, logs)
		}
	}
	if stepper.calls != 2 {
		t.Fatalf("ApplyVersion calls = %d, want 2", stepper.calls)
	}
}

func TestApplyMigrationsLoggedReportsNothingPending(t *testing.T) {
	stepper := &fakeMigrationStepper{statuses: []*goose.MigrationStatus{
		{Source: source(200, "200_done.sql"), State: goose.StateApplied},
	}}
	logger, out := newMigrationTestLogger()
	if err := applyMigrationsLogged(t.Context(), stepper, logger, 0); err != nil {
		t.Fatal(err)
	}
	if stepper.calls != 0 || !strings.Contains(out.String(), "database schema is up to date") {
		t.Fatalf("calls=%d logs:\n%s", stepper.calls, out.String())
	}
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

func TestApplyMigrationsLoggedNamesTheFailedMigration(t *testing.T) {
	broken := source(401, "401_broken.sql")
	boom := errors.New("syntax error at or near \"TABL\"")
	stepper := &fakeMigrationStepper{
		statuses: []*goose.MigrationStatus{
			{Source: broken, State: goose.StatePending},
			{Source: source(402, "402_after.sql"), State: goose.StatePending},
		},
		steps: []fakeMigrationStep{{err: boom}},
	}
	logger, out := newMigrationTestLogger()

	err := applyMigrationsLogged(t.Context(), stepper, logger, 0)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped %v", err, boom)
	}
	if stepper.calls != 1 {
		t.Fatalf("kept applying after a failure: %d calls", stepper.calls)
	}
	if !strings.Contains(out.String(), `level=ERROR msg="database migration failed" version=401 name=401_broken.sql progress=1/2`) {
		t.Fatalf("failure not logged with the migration:\n%s", out.String())
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

func TestLogMigrationRollbackResultsReportsPartialFailure(t *testing.T) {
	logger, out := newMigrationTestLogger()
	boom := errors.New("rollback failed")
	partial := &goose.PartialError{
		Applied: []*goose.MigrationResult{{
			Source: source(702, "702_rolled_back.sql"), Duration: 2 * time.Second,
		}},
		Failed: &goose.MigrationResult{
			Source: source(701, "701_failed.sql"), Duration: 3 * time.Second, Error: boom,
		},
		Err: boom,
	}
	if count := logMigrationRollbackResults(t.Context(), logger, 700, nil, fmt.Errorf("down to 700: %w", partial)); count != 1 {
		t.Fatalf("rolled back = %d, want 1", count)
	}
	logs := out.String()
	if !strings.Contains(logs, `msg="database migration rolled back" version=702 name=702_rolled_back.sql duration=2s`) ||
		!strings.Contains(logs, `msg="database migration rollback failed" version=701 name=701_failed.sql duration=3s`) {
		t.Fatalf("partial rollback results missing:\n%s", logs)
	}
}
