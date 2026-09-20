package worker

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReconcileNodeSessionsAfterAccountDeletion(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(t.Context(), config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("reconciler_integrity_%d", time.Now().UnixNano())
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(t.Context(), `CREATE TABLE users(id integer PRIMARY KEY);
CREATE TABLE playback_sessions_sync (LIKE public.playback_sessions_sync INCLUDING ALL);
ALTER TABLE playback_sessions_sync ADD FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE;
INSERT INTO users VALUES(1),(2);`); err != nil {
		t.Fatal(err)
	}
	r := NewReconciler(pool, "node", nil)
	sessions := []SessionSync{{SessionID: "deleted-account", UserID: 1}, {SessionID: "surviving-account", UserID: 2}}
	if err = r.ReconcileNodeSessions(t.Context(), "node", sessions); err != nil {
		t.Fatal(err)
	}

	// Default, overlay and unknown must survive SQL independently, including
	// a replan from an overlay back to the default network.
	for _, provider := range []*string{new("tailscale"), new(""), nil} {
		sessions[1].RoutingNetworkProvider = provider
		if err := r.ReconcileNodeSessions(t.Context(), "node", sessions); err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := loadNodeSessionsSnapshot(t.Context(), tx, "node")
		_ = tx.Rollback(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot) != 2 || !equalOptionalString(snapshot[1].RoutingNetworkProvider, provider) {
			t.Fatalf("network route round trip: %#v", snapshot)
		}
	}
	if _, err = pool.Exec(t.Context(), "DELETE FROM users WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	// A worker can report the deleted account until its local session expires.
	// It must not prevent the other account's state from being refreshed.
	sessions[1].PositionSeconds = 42
	if err = r.ReconcileNodeSessions(t.Context(), "node", sessions); err != nil {
		t.Fatal(err)
	}
	var count int
	var position float64
	if err = pool.QueryRow(t.Context(), "SELECT count(*),max(position_seconds) FROM playback_sessions_sync").Scan(&count, &position); err != nil || count != 1 || position != 42 {
		t.Fatalf("snapshot: count=%d position=%v err=%v", count, position, err)
	}
	// A snapshot containing only deleted users must clear the prior live row.
	if err = r.ReconcileNodeSessions(t.Context(), "node", sessions[:1]); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM playback_sessions_sync").Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale-only snapshot: count=%d err=%v", count, err)
	}
}
