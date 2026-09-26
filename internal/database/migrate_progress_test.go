package database

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

type fakeMigrationStepper struct {
	statuses []*goose.MigrationStatus
	// steps are returned by successive UpByOne calls.
	steps []fakeMigrationStep
	calls int
}

type fakeMigrationStep struct {
	result *goose.MigrationResult
	err    error
	delay  time.Duration
}

func (f *fakeMigrationStepper) Status(context.Context) ([]*goose.MigrationStatus, error) {
	return f.statuses, nil
}

func (f *fakeMigrationStepper) UpByOne(context.Context) (*goose.MigrationResult, error) {
	if f.calls >= len(f.steps) {
		return nil, goose.ErrNoNextVersion
	}
	step := f.steps[f.calls]
	f.calls++
	time.Sleep(step.delay)
	return step.result, step.err
}

// syncBuffer lets the heartbeat goroutine and the test share a log buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
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
		t.Fatalf("UpByOne calls = %d, want 2", stepper.calls)
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

func TestApplyMigrationsLoggedHeartbeatsWhileAMigrationRuns(t *testing.T) {
	slow := source(301, "301_slow_backfill.sql")
	stepper := &fakeMigrationStepper{
		statuses: []*goose.MigrationStatus{{Source: slow, State: goose.StatePending}},
		steps: []fakeMigrationStep{
			{result: &goose.MigrationResult{Source: slow}, delay: 120 * time.Millisecond},
		},
	}
	logger, out := newMigrationTestLogger()
	if err := applyMigrationsLogged(t.Context(), stepper, logger, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	logs := out.String()
	if !strings.Contains(logs, `msg="database migration still running" version=301 name=301_slow_backfill.sql progress=1/1 elapsed=`) {
		t.Fatalf("no heartbeat naming the running migration:\n%s", logs)
	}
	// The heartbeat stops with the migration.
	heartbeats := strings.Count(logs, "still running")
	time.Sleep(80 * time.Millisecond)
	if after := strings.Count(out.String(), "still running"); after != heartbeats {
		t.Fatalf("heartbeat kept running after the migration: %d then %d", heartbeats, after)
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
	stepper := &fakeMigrationStepper{
		statuses: []*goose.MigrationStatus{
			{Source: first, State: goose.StatePending},
			{Source: source(502, "502_second.sql"), State: goose.StatePending},
		},
		// The second step finds nothing left: another node applied it.
		steps: []fakeMigrationStep{{result: &goose.MigrationResult{Source: first}}},
	}
	logger, out := newMigrationTestLogger()
	if err := applyMigrationsLogged(t.Context(), stepper, logger, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `msg="database migrations finished" applied=1`) {
		t.Fatalf("logs:\n%s", out.String())
	}
}
