package backup

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
)

// TestOtherConnectionsCountsOnlyOtherClientsDB: a restore refuses a live
// database that has other clients. Its own connections, which the server
// lists for a moment after they close, carry its application name and are
// not counted; every other session is, another restore's connections
// included, a session the restoring role may not inspect, and a session
// that is not a client backend but can still change the database (here a
// walsender in database mode). It catches a restore refusing a database
// nothing else is using, and one that stops counting a session that is
// really there.
func TestOtherConnectionsCountsOnlyOtherClientsDB(t *testing.T) {
	pool, dsn := backuptest.Database(t)
	ctx := t.Context()
	live, err := databaseName(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// admin is on a maintenance database, so it is never one of the live
	// database's sessions, and it closes after every other connection the
	// test opens: cleanups run last first. The role is dropped over it once
	// the role's own connections have closed.
	admin, err := ConnectControl(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	// An ordinary role: it sees other roles' sessions with their backend
	// type hidden.
	role, password := "silo_restore_"+randomHex(t, 4), randomHex(t, 16)
	if _, err := admin.Conn.Exec(ctx, "CREATE ROLE "+ident(role)+" LOGIN PASSWORD '"+password+"'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Conn.Exec(context.Background(), "DROP ROLE "+ident(role)); err != nil {
			t.Errorf("dropping the test role %s: %v", role, err)
		}
	})
	pool.Close()
	asRole, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	asRole.User = url.UserPassword(role, password)

	connect := func(dbURL string) *PGControl {
		t.Helper()
		ctl, err := ConnectControl(ctx, dbURL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ctl.Close(context.Background()) })
		return ctl
	}
	restore, another, ordinary := connect(dsn), connect(dsn), connect(asRole.String())
	count := func(ctl *PGControl) int {
		t.Helper()
		n, err := ctl.OtherConnections(ctx, live)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	client := func(dbURL string) {
		t.Helper()
		conn, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
	}
	// The pool closed above was a client like any other; start from none.
	for deadline := time.Now().Add(30 * time.Second); count(restore) != 0; {
		if time.Now().After(deadline) {
			t.Fatalf("%d clients of the closed pool are still listed", count(restore))
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // a closed connection's backend signals nothing but its pg_stat_activity row going away
	}

	own, err := WithApplicationName(dsn, restore.OwnApplicationName())
	if err != nil {
		t.Fatal(err)
	}
	client(own)
	if n := count(restore); n != 0 {
		t.Fatalf("with one of the restore's own connections open: count = %d, want 0", n)
	}
	if n := count(another); n != 1 {
		t.Fatalf("another restore counting that connection: count = %d, want 1", n)
	}
	if n := count(ordinary); n != 1 {
		t.Fatalf("a role that may not inspect that session: count = %d, want 1", n)
	}
	client(dsn)
	if n := count(restore); n != 1 {
		t.Fatalf("with another client connected: count = %d, want 1", n)
	}
	// A walsender in database mode is not a client backend, and it can
	// still apply changes to the database through its client.
	walsender, err := WithApplicationName(dsn, "silo-test-walsender")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgconn.Connect(ctx, walsender+"&replication=database")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if n := count(restore); n != 2 {
		t.Fatalf("with a walsender connected too: count = %d, want 2", n)
	}
}
