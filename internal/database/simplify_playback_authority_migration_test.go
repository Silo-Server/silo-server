package database

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/migrations"
)

func simplifyColumns(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table string) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name=$1`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[c] = true
	}
	return out
}

func simplifyTables(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema='public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[c] = true
	}
	return out
}

func TestSimplifyPlaybackAuthorityMigrationRoundTrip(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("up: %v", err)
	}
	dropped := []string{"playback_automatic_admission_intents", "playback_auxiliary_transfer_permits", "playback_first_admissions", "playback_output_transfer_permits", "playback_progress_sinks", "playback_source_markers", "playback_source_registrations"}
	control := []string{"control_state", "control_owner", "control_epoch", "control_lease_expires_at", "control_incarnation", "control_route", "control_grant_not_after", "control_drain_not_before", "control_recipe_locator", "control_activation", "control_reservation_admission_id", "control_retiring_replan"}
	added := []string{"last_sequence", "last_sample", "stopped_at", "stop_id", "stop_receipt"}

	tables := simplifyTables(ctx, t, pool)
	for _, tb := range dropped {
		if tables[tb] {
			t.Errorf("up: table %s still exists", tb)
		}
	}
	cols := simplifyColumns(ctx, t, pool, "playback_v3_attempts")
	for _, c := range control {
		if cols[c] {
			t.Errorf("up: column %s still exists", c)
		}
	}
	for _, c := range added {
		if !cols[c] {
			t.Errorf("up: column %s missing", c)
		}
	}
	if simplifyColumns(ctx, t, pool, "playback_v3_replans")["route_replacement"] {
		t.Error("up: route_replacement still exists")
	}
	if !simplifyColumns(ctx, t, pool, "playback_route_events")["event_id"] {
		t.Error("up: playback_route_events.event_id missing")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='playback_v3_attempts'::regclass AND conname LIKE 'playback_attempt_%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("up: %d playback_attempt_* constraints remain", n)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname IN ('playback_v3_attempts_control_incarnation_idx','playback_one_pending_route_replacement')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("up: %d authority indexes remain", n)
	}

	// Down to the migration just before ours, then verify the restored shape.
	if err := MigrateDownTo(ctx, pool, migrations.FS, "sql", 20260908133802); err != nil {
		t.Fatalf("down: %v", err)
	}
	tables = simplifyTables(ctx, t, pool)
	for _, tb := range dropped {
		if !tables[tb] {
			t.Errorf("down: table %s not restored", tb)
		}
	}
	cols = simplifyColumns(ctx, t, pool, "playback_v3_attempts")
	for _, c := range control {
		if !cols[c] {
			t.Errorf("down: column %s not restored", c)
		}
	}
	for _, c := range added {
		if cols[c] {
			t.Errorf("down: column %s still exists", c)
		}
	}
	if !simplifyColumns(ctx, t, pool, "playback_v3_replans")["route_replacement"] {
		t.Error("down: route_replacement not restored")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='playback_v3_attempts'::regclass AND conname IN ('playback_attempt_authority_state','playback_attempt_authority_incarnation','playback_attempt_grant_bounds','playback_attempt_recipe_locator_binding')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("down: %d/4 authority constraints restored", n)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname IN ('playback_v3_attempts_control_incarnation_idx','playback_one_pending_route_replacement','playback_output_transfer_attempt','playback_auxiliary_transfer_attempt')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("down: %d/4 authority indexes restored", n)
	}
	// And back up again so the database is left on the new schema.
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	cols = simplifyColumns(ctx, t, pool, "playback_v3_attempts")
	for _, c := range added {
		if !cols[c] {
			t.Errorf("re-up: column %s missing", c)
		}
	}
}
