package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDashboardRevisionPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_DASHBOARD_TEST_DSN")
	if dsn == "" {
		t.Skip("SILO_DASHBOARD_TEST_DSN must name the isolated operations_dashboard_guard database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var database string
	if err = pool.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil || database != "operations_dashboard_guard" {
		t.Fatalf("isolated database guard failed: %v", err)
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("refuse nonempty synthetic database: tables=%d err=%v", count, err)
	}
	if _, err = pool.Exec(ctx, "CREATE TABLE users (id integer PRIMARY KEY); INSERT INTO users VALUES (1),(2)"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), "DROP TABLE admin_dashboard_layout_revisions, admin_dashboard_layouts, users")
	}()
	for _, name := range []string{"20260827013713_admin_dashboard_layouts.sql", "20260906190327_admin_dashboard_layout_revisions.sql"} {
		raw, readErr := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "sql", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		up := strings.Split(string(raw), "-- +goose Down")[0]
		if _, err = pool.Exec(ctx, up); err != nil {
			t.Fatal(err)
		}
	}
	h := &AdminHandler{pool: pool}
	initial, err := h.ReadAdminDashboardLayout(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	stale := errors.New("stale")
	guard := func(revision string) func(AdminDashboardLayoutView) error {
		return func(v AdminDashboardLayoutView) error {
			if v.Revision != revision {
				return stale
			}
			return nil
		}
	}
	saved, err := h.SaveAdminDashboardLayout(ctx, 1, json.RawMessage(`{"value":"A"}`), guard(initial.Revision))
	if err != nil || saved.Revision == initial.Revision {
		t.Fatal(saved, err)
	}
	// Two clients with the same read version: exactly one persists.
	var successes, conflicts atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, e := h.SaveAdminDashboardLayout(ctx, 1, json.RawMessage(`{"value":"race"}`), guard(saved.Revision))
			if e == nil {
				successes.Add(1)
			} else if errors.Is(e, stale) {
				conflicts.Add(1)
			} else {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 || conflicts.Load() != 7 {
		t.Fatal(successes.Load(), conflicts.Load())
	}
	// Bridge writes share the same serialized helper but retain unconditional wire behavior.
	current, err := h.ReadAdminDashboardLayout(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := h.writeAdminDashboardLayout(ctx, 1, json.RawMessage(`{"value":"bridge"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.SaveAdminDashboardLayout(ctx, 1, json.RawMessage(`{"value":"stale"}`), guard(current.Revision)); !errors.Is(err, stale) {
		t.Fatal(err)
	}
	final, err := h.ReadAdminDashboardLayout(ctx, 1)
	if err != nil || final.Revision != bridge.Revision || string(final.Layout) != string(bridge.Layout) {
		t.Fatal(final, err)
	}
	// Reset retains a tombstone: neither a saved nor initial-absent version can resurrect it.
	if err = h.ResetAdminDashboardLayout(ctx, 1); err != nil {
		t.Fatal(err)
	}
	reset, err := h.ReadAdminDashboardLayout(ctx, 1)
	if err != nil || len(reset.Layout) != 0 || reset.Revision == initial.Revision {
		t.Fatal(reset, err)
	}
	for _, revision := range []string{initial.Revision, bridge.Revision} {
		if _, err = h.SaveAdminDashboardLayout(ctx, 1, json.RawMessage(`{}`), guard(revision)); !errors.Is(err, stale) {
			t.Fatal(err)
		}
	}
	// Observe a real blocked account-row writer before releasing the guarded transaction.
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "dashboard-bridge-order"
	other, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherHandler := &AdminHandler{pool: other}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		_, e := h.SaveAdminDashboardLayout(ctx, 1, json.RawMessage(`{"value":"guarded"}`), func(v AdminDashboardLayoutView) error {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return guard(reset.Revision)(v)
		})
		done <- e
	}()
	<-entered
	go func() {
		_, e := otherHandler.writeAdminDashboardLayout(ctx, 1, json.RawMessage(`{"value":"later-bridge"}`), nil)
		done <- e
	}()
	blocked := false
	for ctx.Err() == nil {
		if err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name='dashboard-bridge-order' AND wait_event_type='Lock')").Scan(&blocked); err != nil {
			break
		}
		if blocked {
			break
		}
		runtime.Gosched()
	}
	close(release)
	if !blocked {
		t.Fatal("did not observe queued account lock", err)
	}
	for range 2 {
		if err = <-done; err != nil {
			t.Fatal(err)
		}
	}
	final, err = h.ReadAdminDashboardLayout(ctx, 1)
	if err != nil || !strings.Contains(string(final.Layout), "later-bridge") {
		t.Fatal(final, err)
	}
	t.Log("PASS: original migrations, 1/8 guarded winner, stale bridge refusal, reset ABA refusal, observed account-lock ordering and final stored effects")
}
