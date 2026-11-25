package backup

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
)

// TestOtherConnectionsCountsOnlyOtherClientsDB: a restore refuses a live
// database that has other clients. Its own connections, which the server
// lists for a moment after they close, carry its application name and are
// not counted; every other client is, another restore's connections
// included, and so is a session the restoring role may not inspect. It
// catches a restore refusing a database nothing else is using, and one that
// stops counting a client that is really there.
func TestOtherConnectionsCountsOnlyOtherClientsDB(t *testing.T) {
	pool, dsn := backuptest.Database(t)
	ctx := t.Context()
	live, err := databaseName(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// An ordinary role: it sees other roles' sessions with their backend
	// type and application name hidden.
	role, password := "silo_restore_"+randomHex(t, 4), randomHex(t, 16)
	if _, err := pool.Exec(ctx, "CREATE ROLE "+ident(role)+" LOGIN PASSWORD '"+password+"'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP ROLE IF EXISTS "+ident(role)) })
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
}
