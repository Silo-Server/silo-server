package repository

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/migrations"
)

const triggerSetsMigration = "sql/20260906235758_preserve_empty_task_schedules.sql"

// Isolate the real tables and migrations in a schema, without needing the rest
// of the catalog or modifying an existing test database's scheduler tables.
func triggerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	control, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)
	schema := fmt.Sprintf("trigger_test_%d", time.Now().UnixNano())
	if _, err := control.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := control.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	applyTriggerMigration(t, pool, "sql/002_task_framework.sql", false)
	// This custom schedule predates the new parent table.
	if _, err := pool.Exec(ctx, `INSERT INTO task_triggers (task_key, type, time_of_day)
		VALUES ('legacy', 'daily', '09:45')`); err != nil {
		t.Fatal(err)
	}
	applyTriggerMigration(t, pool, triggerSetsMigration, false)
	return pool
}

func applyTriggerMigration(t *testing.T, pool *pgxpool.Pool, path string, down bool) {
	t.Helper()
	raw, err := migrations.FS.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(raw), "-- +goose Down")
	sql := parts[0]
	if down {
		sql = parts[1]
	}
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("apply %s (down=%t): %v", path, down, err)
	}
}

func TestPgTriggersPreserveSchedules(t *testing.T) {
	pool := triggerTestPool(t)
	repo := NewPgTriggerRepository(pool)
	ctx := context.Background()
	defaults := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "03:30"}}
	legacy := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "09:45"}}
	if got, exists, err := repo.GetTriggers(ctx, "never_saved"); err != nil || exists || len(got) != 0 {
		t.Fatalf("unconfigured schedule = %+v, exists=%t, %v", got, exists, err)
	}
	if got, err := repo.GetOrCreateTriggers(ctx, "never_saved", defaults); err != nil || !slices.Equal(got, defaults) {
		t.Fatalf("read of absent schedule interfered with first seed: %+v, %v", got, err)
	}
	var initialized bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_trigger_sets WHERE task_key = 'legacy')`).Scan(&initialized); err != nil || !initialized {
		t.Fatalf("legacy schedule was not backfilled: %t, %v", initialized, err)
	}
	if got, err := repo.GetOrCreateTriggers(ctx, "legacy", defaults); err != nil || !slices.Equal(got, legacy) {
		t.Fatalf("migrated schedule = %+v, %v", got, err)
	}
	// An older instance may save a newly registered task after backfill.
	if _, err := pool.Exec(ctx, `INSERT INTO task_triggers (task_key, type, time_of_day)
		VALUES ('late_legacy', 'daily', '09:45')`); err != nil {
		t.Fatal(err)
	}
	if got, exists, err := repo.GetTriggers(ctx, "late_legacy"); err != nil || !exists || !slices.Equal(got, legacy) {
		t.Fatalf("late legacy lookup = %+v, exists=%t, %v", got, exists, err)
	}
	if got, err := repo.GetOrCreateTriggers(ctx, "late_legacy", defaults); err != nil || !slices.Equal(got, legacy) {
		t.Fatalf("late legacy schedule = %+v, %v", got, err)
	}

	for _, tc := range []struct {
		name  string
		saved []taskmanager.TriggerConfig
	}{
		{"empty", nil},
		{"modified", []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 123000, MaxRuntimeMs: 45000}}},
		{"added", []taskmanager.TriggerConfig{
			{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "03:30"},
			{Type: taskmanager.TriggerTypeWeekly, TimeOfDay: "11:15", DayOfWeek: 0, MaxRuntimeMs: 60000},
			{Type: taskmanager.TriggerTypeStartup},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := repo.GetOrCreateTriggers(ctx, tc.name, defaults); err != nil || !slices.Equal(got, defaults) {
				t.Fatalf("first seed = %+v, %v", got, err)
			}
			if err := repo.SetTriggers(ctx, tc.name, tc.saved); err != nil {
				t.Fatal(err)
			}
			// A fresh repository represents another server or a restarted one.
			fresh := NewPgTriggerRepository(pool)
			if got, exists, err := fresh.GetTriggers(ctx, tc.name); err != nil || !exists || !slices.Equal(got, tc.saved) {
				t.Fatalf("saved schedule lookup = %+v, exists=%t, %v; want %+v", got, exists, err, tc.saved)
			}
			if got, err := fresh.GetOrCreateTriggers(ctx, tc.name, legacy); err != nil || !slices.Equal(got, tc.saved) {
				t.Fatalf("saved schedule = %+v, %v; want %+v", got, err, tc.saved)
			}
		})
	}
	// A task whose original defaults were empty is also initialized only once.
	if _, err := repo.GetOrCreateTriggers(ctx, "empty_default", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetOrCreateTriggers(ctx, "empty_default", defaults); err != nil || len(got) != 0 {
		t.Fatalf("empty default replaced on upgrade: %+v, %v", got, err)
	}
	// Saving before the task has ever started must also suppress default seeding.
	if err := repo.SetTriggers(ctx, "saved_before_start", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetOrCreateTriggers(ctx, "saved_before_start", defaults); err != nil || len(got) != 0 {
		t.Fatalf("pre-start empty schedule replaced: %+v, %v", got, err)
	}
	applyTriggerMigration(t, pool, triggerSetsMigration, true)
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM task_triggers WHERE task_key = 'legacy'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback changed trigger rows: %d, %v", count, err)
	}
}

func TestPgTriggersFailedWriteRollsBack(t *testing.T) {
	pool := triggerTestPool(t)
	repo := NewPgTriggerRepository(pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE task_triggers ADD CONSTRAINT reject_test_interval CHECK (interval <> 13)`); err != nil {
		t.Fatal(err)
	}
	invalid := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 13}}
	if _, err := repo.GetOrCreateTriggers(ctx, "failed_seed", invalid); err == nil {
		t.Fatal("expected seed failure")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM task_trigger_sets WHERE task_key = 'failed_seed'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed seed left an initialized record: %d, %v", count, err)
	}
	if err := repo.SetTriggers(ctx, "legacy", invalid); err == nil {
		t.Fatal("expected replacement failure")
	}
	want := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "09:45"}}
	if got, err := repo.GetOrCreateTriggers(ctx, "legacy", nil); err != nil || !slices.Equal(got, want) {
		t.Fatalf("failed replacement lost original schedule: %+v, %v", got, err)
	}
}

func TestPgTriggersConcurrentStartupAndEdits(t *testing.T) {
	pool := triggerTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defaults := []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "03:30"}}
	for _, tc := range []struct {
		name    string
		editing bool
		saved   []taskmanager.TriggerConfig
	}{
		{name: "startup"},
		{name: "clear", editing: true},
		{name: "replace", editing: true, saved: []taskmanager.TriggerConfig{
			{Type: taskmanager.TriggerTypeWeekly, DayOfWeek: 4, TimeOfDay: "08:15"},
			{Type: taskmanager.TriggerTypeInterval, IntervalMs: 120000, MaxRuntimeMs: 30000},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "concurrent_" + tc.name
			start := make(chan struct{})
			errs := make(chan error, 16)
			var wg sync.WaitGroup
			for i := range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					repo := NewPgTriggerRepository(pool)
					if tc.editing && i%2 == 0 {
						errs <- repo.SetTriggers(ctx, key, tc.saved)
					} else {
						_, err := repo.GetOrCreateTriggers(ctx, key, defaults)
						errs <- err
					}
				}()
			}
			close(start)
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			want := defaults
			if tc.editing {
				want = tc.saved
			}
			if got, err := NewPgTriggerRepository(pool).GetOrCreateTriggers(ctx, key, defaults); err != nil || !slices.Equal(got, want) {
				t.Fatalf("concurrent schedule = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}
