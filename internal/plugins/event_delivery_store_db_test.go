package plugins

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// TestEventDeliveryOutboxPostgres covers the plugin_event_deliveries claim
// protocol every API server shares: a claim takes at most its limit, no row
// goes to two claimers, a lease hides a claimed row until it runs out, and
// only the latest claim records an outcome.
func TestEventDeliveryOutboxPostgres(t *testing.T) {
	pool := builtinGuardTestPool(t)
	store := NewEventDeliveryStore(pool)
	ctx := context.Background()

	var stray int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM plugin_event_deliveries`).Scan(&stray); err != nil {
		t.Fatal(err)
	}
	if stray > 0 {
		t.Skipf("%d plugin event deliveries already in the database", stray)
	}

	enqueue := func(t *testing.T, installationID, n int) {
		t.Helper()
		deliveries := make([]EventDelivery, n)
		for i := range deliveries {
			deliveries[i] = EventDelivery{EventID: "evt", EventName: "scan_complete", InstallationID: installationID, CapabilityID: "events"}
		}
		if err := store.Enqueue(ctx, deliveries); err != nil {
			t.Fatal(err)
		}
	}
	claim := func(t *testing.T, limit int, lease time.Duration) []EventDelivery {
		t.Helper()
		claimed, err := store.Claim(ctx, limit, lease)
		if err != nil {
			t.Fatal(err)
		}
		return claimed
	}
	drain := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx, `DELETE FROM plugin_event_deliveries`); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("claim takes at most its limit", func(t *testing.T) {
		installation := seedResidentQueryInstallation(t, pool, true)
		t.Cleanup(func() { drain(t) })
		enqueue(t, installation, 6)
		if got := claim(t, 4, time.Hour); len(got) != 4 {
			t.Fatalf("first claim = %d rows, want 4", len(got))
		}
		second := claim(t, 4, time.Hour)
		if len(second) != 2 {
			t.Fatalf("second claim = %d rows, want 2", len(second))
		}
		if second[0].Attempts != 1 || second[0].InstallationID != installation || second[0].CapabilityID != "events" {
			t.Fatalf("claimed delivery = %+v", second[0])
		}
		if got := claim(t, 4, time.Hour); len(got) != 0 {
			t.Fatalf("claim while every row is leased = %d rows, want 0", len(got))
		}
	})

	t.Run("concurrent claimers never share a row", func(t *testing.T) {
		installation := seedResidentQueryInstallation(t, pool, true)
		t.Cleanup(func() { drain(t) })
		const rows, claimers, limit = 60, 8, 3
		enqueue(t, installation, rows)

		var mu sync.Mutex
		seen := map[int64]int{}
		var wg sync.WaitGroup
		for range claimers {
			wg.Go(func() {
				for {
					claimed, err := store.Claim(ctx, limit, time.Hour)
					if err != nil {
						t.Error(err)
						return
					}
					if len(claimed) > limit {
						t.Errorf("claim returned %d rows, limit %d", len(claimed), limit)
					}
					if len(claimed) == 0 {
						return
					}
					mu.Lock()
					for _, delivery := range claimed {
						seen[delivery.ID]++
					}
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if len(seen) != rows {
			t.Fatalf("claimed %d distinct rows, want %d", len(seen), rows)
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("row %d claimed %d times", id, n)
			}
		}
	})

	t.Run("only the latest claim records the outcome", func(t *testing.T) {
		installation := seedResidentQueryInstallation(t, pool, true)
		t.Cleanup(func() { drain(t) })
		enqueue(t, installation, 1)
		// A zero lease is a claimer whose lease has already run out.
		first := claim(t, 1, 0)
		second := claim(t, 1, time.Hour)
		if len(first) != 1 || len(second) != 1 || second[0].ID != first[0].ID || second[0].Attempts != 2 {
			t.Fatalf("claims = %+v then %+v, want the same row at attempt 1 then 2", first, second)
		}
		if err := store.Retry(ctx, first[0].ID, first[0].Attempts, 0, "stale"); err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(ctx, first[0].ID, first[0].Attempts); err != nil {
			t.Fatal(err)
		}
		if got := claim(t, 1, time.Hour); len(got) != 0 {
			t.Fatalf("the stale claimer's retry made the row due: %+v", got)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM plugin_event_deliveries WHERE id = $1`, first[0].ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("rows after the stale claimer's delete = %d, %v; want 1", count, err)
		}
		if err := store.Delete(ctx, second[0].ID, second[0].Attempts); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM plugin_event_deliveries WHERE id = $1`, first[0].ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rows after the current claimer's delete = %d, %v; want 0", count, err)
		}
	})

	t.Run("retry waits for its delay", func(t *testing.T) {
		installation := seedResidentQueryInstallation(t, pool, true)
		t.Cleanup(func() { drain(t) })
		enqueue(t, installation, 1)
		claimed := claim(t, 1, time.Hour)
		if err := store.Retry(ctx, claimed[0].ID, claimed[0].Attempts, time.Hour, "plugin restarting"); err != nil {
			t.Fatal(err)
		}
		if got := claim(t, 1, time.Hour); len(got) != 0 {
			t.Fatalf("claim before the retry is due = %+v", got)
		}
		var lastError string
		if err := pool.QueryRow(ctx, `SELECT last_error FROM plugin_event_deliveries WHERE id = $1`, claimed[0].ID).Scan(&lastError); err != nil || lastError != "plugin restarting" {
			t.Fatalf("last_error = %q, %v", lastError, err)
		}
		if err := store.Retry(ctx, claimed[0].ID, claimed[0].Attempts, 0, "plugin restarting"); err != nil {
			t.Fatal(err)
		}
		if got := claim(t, 1, time.Hour); len(got) != 1 || got[0].Attempts != 2 {
			t.Fatalf("claim once the retry is due = %+v, want the row at attempt 2", got)
		}
	})

	t.Run("an uninstalled installation does not fail the others", func(t *testing.T) {
		installation := seedResidentQueryInstallation(t, pool, true)
		gone := seedResidentQueryInstallation(t, pool, true)
		t.Cleanup(func() { drain(t) })
		if _, err := pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, gone); err != nil {
			t.Fatal(err)
		}
		if err := store.Enqueue(ctx, []EventDelivery{
			{EventID: "evt", EventName: "scan_complete", InstallationID: gone, CapabilityID: "events"},
			{EventID: "evt", EventName: "scan_complete", InstallationID: installation, CapabilityID: "events"},
		}); err != nil {
			t.Fatalf("enqueue with an uninstalled installation: %v", err)
		}
		claimed := claim(t, 10, time.Hour)
		if len(claimed) != 1 || claimed[0].InstallationID != installation {
			t.Fatalf("claimed = %+v, want only installation %d's delivery", claimed, installation)
		}
	})

	t.Run("an uninstall racing the insert does not fail the others", func(t *testing.T) {
		installation := seedResidentQueryInstallation(t, pool, true)
		gone := seedResidentQueryInstallation(t, pool, true)
		t.Cleanup(func() { drain(t) })
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, gone); err != nil {
			t.Fatal(err)
		}

		done := make(chan error, 1)
		go func() {
			done <- store.Enqueue(ctx, []EventDelivery{
				{EventID: "evt", EventName: "scan_complete", InstallationID: gone, CapabilityID: "events"},
				{EventID: "evt", EventName: "scan_complete", InstallationID: installation, CapabilityID: "events"},
			})
		}()
		// Wait until the insert waits on the uninstall's row lock.
		deadline := time.Now().Add(10 * time.Second)
		for {
			var waiting int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE wait_event_type = 'Lock' AND query LIKE 'INSERT INTO plugin_event_deliveries%'`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the insert never waited on the uninstall")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("enqueue racing an uninstall: %v", err)
		}
		claimed := claim(t, 10, time.Hour)
		if len(claimed) != 1 || claimed[0].InstallationID != installation {
			t.Fatalf("claimed = %+v, want only installation %d's delivery", claimed, installation)
		}
	})

	t.Run("payload round trip and uninstall", func(t *testing.T) {
		installation := seedResidentQueryInstallation(t, pool, true)
		t.Cleanup(func() { drain(t) })
		if err := store.Enqueue(ctx, []EventDelivery{
			{EventID: "with", EventName: "scan_complete", Payload: json.RawMessage(`{"libraryId":"lib-1"}`), InstallationID: installation, CapabilityID: "events"},
			{EventID: "without", EventName: "scan_complete", InstallationID: installation, CapabilityID: "events"},
			// jsonb would refuse the whole insert over this escape.
			{EventID: "nul", EventName: "scan_complete", Payload: json.RawMessage(`{"name":"a\u0000b"}`), InstallationID: installation, CapabilityID: "events"},
		}); err != nil {
			t.Fatal(err)
		}
		for _, delivery := range claim(t, 3, time.Hour) {
			switch delivery.EventID {
			case "with":
				var payload map[string]any
				if err := json.Unmarshal(delivery.Payload, &payload); err != nil || payload["libraryId"] != "lib-1" {
					t.Fatalf("payload = %s, %v", delivery.Payload, err)
				}
			case "without":
				if delivery.Payload != nil {
					t.Fatalf("payload = %s, want none", delivery.Payload)
				}
			case "nul":
				var payload map[string]string
				if err := json.Unmarshal(delivery.Payload, &payload); err != nil || payload["name"] != "a\x00b" {
					t.Fatalf("payload = %s, %v", delivery.Payload, err)
				}
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, installation); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM plugin_event_deliveries`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("deliveries after uninstall = %d, %v; want 0", count, err)
		}
	})
}
