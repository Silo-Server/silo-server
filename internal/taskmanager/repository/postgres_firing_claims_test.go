package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// Each scheduled firing of a claiming task runs on exactly one server: of
// servers claiming at once exactly one wins; a server whose timer fires
// seconds later loses too, even across an interval slot boundary; a startup
// firing runs once per cluster start; a later firing is claimed again; and
// expired claims of a task that stopped firing are pruned by another task's
// claim, while a long-interval task's claim outlives a short-interval prune.
func TestPgFiringClaimsRunEachFiringOnceDB(t *testing.T) {
	pool := taskHistoryTestPool(t)
	ctx := context.Background()
	claims := NewPgFiringClaims(pool)
	newKey := func(name string) string {
		key := fmt.Sprintf("plugin:test:%s_%s_%d", t.Name(), name, time.Now().UnixNano())
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM task_firing_claims WHERE task_key = $1`, key)
		})
		return key
	}
	claim := func(t *testing.T, key string, trigger taskmanager.TriggerConfig, firedAt time.Time) bool {
		t.Helper()
		slot, window := taskmanager.FiringSlot(trigger, firedAt)
		won, err := claims.ClaimFiring(ctx, key, slot, window)
		if err != nil {
			t.Error(err) // also called off the test goroutine
		}
		return won
	}
	now := time.Now()

	t.Run("concurrent servers", func(t *testing.T) {
		key := newKey("concurrent")
		daily := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: now.Format("15:04")}
		var wins atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if claim(t, key, daily, now) {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("%d servers won one firing, want 1", wins.Load())
		}
	})

	t.Run("daily timers seconds apart", func(t *testing.T) {
		key := newKey("daily")
		daily := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "03:00"}
		slot := time.Date(2026, 11, 6, 3, 0, 0, 0, time.Local)
		if !claim(t, key, daily, slot.Add(time.Second)) || claim(t, key, daily, slot.Add(40*time.Second)) {
			t.Fatal("want the first server to win and the later one to lose")
		}
		if !claim(t, key, daily, slot.AddDate(0, 0, 1).Add(2*time.Second)) {
			t.Fatal("the next day's firing must be claimable")
		}
	})

	t.Run("interval timers straddling a slot boundary", func(t *testing.T) {
		key := newKey("interval")
		hourly := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeInterval, IntervalMs: time.Hour.Milliseconds()}
		boundary := now.Truncate(time.Hour)
		if !claim(t, key, hourly, boundary.Add(-2*time.Second)) || claim(t, key, hourly, boundary.Add(3*time.Second)) {
			t.Fatal("want the first server to win and the one firing after the slot boundary to lose")
		}
	})

	t.Run("startup once per cluster start", func(t *testing.T) {
		key := newKey("startup")
		startup := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeStartup}
		if !claim(t, key, startup, now) || claim(t, key, startup, now.Add(90*time.Second)) {
			t.Fatal("want one startup run for servers starting together")
		}
		if _, err := pool.Exec(ctx, `UPDATE task_firing_claims SET claimed_at = claimed_at - $2 * interval '1 second' WHERE task_key = $1`,
			key, (taskmanager.StartupClaimWindow + time.Minute).Seconds()); err != nil {
			t.Fatal(err)
		}
		if !claim(t, key, startup, now.Add(20*time.Minute)) {
			t.Fatal("a start after the window must run")
		}
	})

	t.Run("pruning keeps each claim for its own window", func(t *testing.T) {
		gone, monthly, live := newKey("uninstalled"), newKey("monthly"), newKey("live")
		if _, err := pool.Exec(ctx, `INSERT INTO task_firing_claims (task_key, slot, claimed_at, expires_at) VALUES ($1, $2::timestamptz, $2::timestamptz, $2::timestamptz + interval '8 days')`,
			gone, now.Add(-9*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		thirtyDays := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeInterval, IntervalMs: (30 * 24 * time.Hour).Milliseconds()}
		if !claim(t, monthly, thirtyDays, now) {
			t.Fatal("the monthly task's first claim must win")
		}
		// Age the monthly claim past the fixed retention, still inside its window.
		if _, err := pool.Exec(ctx, `UPDATE task_firing_claims SET claimed_at = claimed_at - interval '10 days', expires_at = expires_at - interval '10 days' WHERE task_key = $1`, monthly); err != nil {
			t.Fatal(err)
		}
		hourly := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeInterval, IntervalMs: time.Hour.Milliseconds()}
		if !claim(t, live, hourly, now) {
			t.Fatal("the live task's claim must win")
		}
		count := func(key string) (n int) {
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM task_firing_claims WHERE task_key = $1`, key).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		if n := count(gone); n != 0 {
			t.Fatalf("%d expired claims of a task that stopped firing remain", n)
		}
		if n := count(monthly); n != 1 {
			t.Fatalf("the monthly task has %d claims after another task pruned, want 1", n)
		}
		if claim(t, monthly, thirtyDays, now.Add(10*24*time.Hour)) {
			t.Fatal("a server firing ten days into the monthly window must lose")
		}
	})
}
