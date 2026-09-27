package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ListDevices must report last_seen_at in the same RFC 3339 form every other
// store uses. Reading the column as Postgres text produced a second format that
// callers merging these rows with device-setting timestamps could not parse.
func TestListDevicesReportsRFC3339Timestamps(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var id int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("list-devices-%d", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) }()

	store := newStore(pool, id)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterDevice(ctx, userstore.DeviceEntry{ProfileID: "p", DeviceID: "d"}); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	if _, err := pool.Exec(ctx, `UPDATE user_devices SET last_seen_at=$2 WHERE user_id=$1`, id, want); err != nil {
		t.Fatal(err)
	}

	devices, err := store.ListDevices(ctx)
	if err != nil || len(devices) != 1 {
		t.Fatalf("ListDevices: %+v %v", devices, err)
	}
	got, err := time.Parse(time.RFC3339Nano, devices[0].LastSeenAt)
	if err != nil {
		t.Fatalf("last_seen_at %q is not RFC 3339: %v", devices[0].LastSeenAt, err)
	}
	if !got.Equal(want) {
		t.Fatalf("last_seen_at = %s, want %s", got, want)
	}
}
