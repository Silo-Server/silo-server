package activitylog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func TestMutationCommitRollbackAndHistoryDB(t *testing.T) {
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
	target := fmt.Sprint(time.Now().UnixNano())
	hash, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var actor int
	if err = pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,role) VALUES($1,$2,$3,'admin') RETURNING id`, "audit-atomic-"+target, "audit-atomic-"+target+"@example.test", string(hash)).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, actor) }()
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM activity_log WHERE target_id LIKE $1`, target+"%") }()
	hub := logstream.NewHub("test", nil)
	tail, unsubscribe := hub.Subscribe(nil)
	defer unsubscribe()
	lc := &LogContext{Request: httptest.NewRequest("PUT", "/api/v2/admin/users/2", nil), Started: time.Now(), UserID: &actor, NodeID: "test",
		Publish: func(entry AuditEntry) {
			_, _ = logstream.PublishAppends(hub, logstream.StreamAudit, []AuditEntry{entry})
		}}
	ctx = SetLogContext(ctx, lc)
	resolver := clientip.NewResolver(nil)
	var scoped context.Context
	clientip.Middleware(resolver)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { scoped = r.Context() })).ServeHTTP(httptest.NewRecorder(), lc.Request.WithContext(ctx))
	ctx = scoped
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `CREATE TEMP TABLE audit_atomic_fixture (value integer); INSERT INTO audit_atomic_fixture VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(ctx, `DROP TABLE audit_atomic_fixture`) }()
	for _, commit := range []bool{false, true} {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE audit_atomic_fixture SET value=1`); err != nil {
			t.Fatal(err)
		}
		entry, err := auditmutation.RecordMutation(ctx, tx, "user.updated", "user", target, 204, auditmutation.Changes(map[string]any{"permissions": []string{}}, map[string]any{"permissions": []string{"marker_edit"}}))
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if commit {
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			auditmutation.CommitMutation(ctx, entry)
		} else {
			_ = tx.Rollback(ctx)
		}
		var value int
		if err = conn.QueryRow(ctx, `SELECT value FROM audit_atomic_fixture`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		if value != map[bool]int{false: 0, true: 1}[commit] {
			t.Fatalf("mutation value %d after commit=%v", value, commit)
		}
		if got := len(tail); got != map[bool]int{false: 0, true: 1}[commit] {
			t.Fatalf("live rows %d", got)
		}
	}
	// A group move must not incur one round trip per account. Verify one bulk
	// insert still rolls back atomically and yields distinct post-commit entries.
	targets := []string{target + "-a", target + "-b", target + "-c"}
	for _, commit := range []bool{false, true} {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := auditmutation.RecordMutations(ctx, tx, "user.updated", "user", targets, 204,
			auditmutation.Changes(map[string]any{"access_group_id": 1}, map[string]any{"access_group_id": 2}))
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if len(entries) != 3 {
			t.Fatalf("batch rows: %d", len(entries))
		}
		if commit {
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				auditmutation.CommitMutation(ctx, entry)
			}
		} else {
			_ = tx.Rollback(ctx)
		}
		var count int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM activity_log WHERE target_id=ANY($1::text[])`, targets).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != map[bool]int{false: 0, true: 3}[commit] {
			t.Fatalf("batch durability: %d, commit=%v", count, commit)
		}
	}
	rows, err := NewRepo(pool).List(ctx, ListOptions{Action: "user.updated", ActorUserID: &actor, TargetType: "user", TargetID: target, Limit: 10})
	if err != nil || len(rows.Entries) != 1 {
		t.Fatalf("history %+v, %v", rows, err)
	}
	var impersonator int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,role) VALUES($1,$2,$3,'admin') RETURNING id`, "audit-impersonator-"+target, "audit-impersonator-"+target+"@example.test", string(hash)).Scan(&impersonator); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, impersonator) }()
	if _, err := pool.Exec(ctx, `UPDATE activity_log SET impersonator_user_id=$1 WHERE target_id=$2`, impersonator, target); err != nil {
		t.Fatal(err)
	}
	for _, account := range []int{actor, impersonator} {
		filtered, err := NewRepo(pool).List(ctx, ListOptions{ActorUserID: &account, TargetID: target, Limit: 10})
		expected := 0
		if account == impersonator {
			expected = 1
		}
		if err != nil || len(filtered.Entries) != expected {
			t.Fatalf("actor precedence for%d: %+v, %v", account, filtered, err)
		}
	}
	if len(rows.Entries[0].Changes) != 1 || rows.Entries[0].Changes[0].Field != "permissions" {
		t.Fatal("missing persisted changes")
	}
}
