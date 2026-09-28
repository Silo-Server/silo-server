package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeviceSettingsRollback(t *testing.T) {
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
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("device-rollback-%d", time.Now().UnixNano())).Scan(&id); err != nil {
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
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{Key: "theme", Scope: settingscontract.ScopeProfileDevice, ProfileID: "p", DeviceID: "d"}, json.RawMessage(`"dark"`)); err != nil {
		t.Fatal(err)
	}
	// A real failure at the final registry delete must restore canonical settings.
	name := fmt.Sprintf("device_rollback_%d", id)
	sqlText := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.user_id=%d THEN RAISE EXCEPTION 'injected device deletion failure'; END IF; RETURN OLD; END $$; CREATE TRIGGER %s BEFORE DELETE ON user_devices FOR EACH ROW EXECUTE FUNCTION %s()`, name, id, name, name)
	if _, err := pool.Exec(ctx, sqlText); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER %s ON user_devices; DROP FUNCTION %s()`, name, name))
	}()
	if _, err := store.RemoveDeviceSettings(ctx, "p", "d", true); err == nil {
		t.Fatal("expected injected error")
	}
	page, err := store.ListDeviceSettingsPage(ctx, userstore.DevicePageOptions{ProfileID: "p", Limit: 10})
	if err != nil || len(page) != 1 || page[0].ChangedCount != 1 {
		t.Fatalf("partial cleanup: %+v %v", page, err)
	}
	// An account-scoped store cannot observe or remove another account's device.
	other := newStore(pool, id+1000000)
	page, err = other.ListDeviceSettingsPage(ctx, userstore.DevicePageOptions{Limit: 10})
	if err != nil || len(page) != 0 {
		t.Fatalf("account isolation: %+v %v", page, err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "q", Name: "Q"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterDevice(ctx, userstore.DeviceEntry{ProfileID: "q", DeviceID: "d"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_devices SET last_seen_at='2026-01-02T03:04:05.123456Z' WHERE user_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	opts := userstore.DevicePageOptions{Limit: 1}
	for _, want := range []string{"p", "q"} {
		page, err := store.ListDeviceSettingsPage(ctx, opts)
		if err != nil || len(page) != 1 || page[0].ProfileID != want {
			t.Fatalf("tie continuation: %+v %v", page, err)
		}
		item := page[0]
		if item.LastSeenAt != "2026-01-02T03:04:05.123456Z" {
			t.Fatalf("device timestamp lost RFC3339 precision: %q", item.LastSeenAt)
		}
		opts.After = &userstore.DevicePosition{LastSeenAt: item.LastSeenAt, ProfileID: item.ProfileID, DeviceID: item.DeviceID}
	}
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx) //nolint:errcheck
	if _, err := lock.Exec(ctx, "SELECT pg_advisory_xact_lock($1,$2)", preferenceSettingsAdvisoryClass, int32(id)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := store.RemoveDeviceSettings(ctx, "q", "d", false); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=$2 AND NOT granted)`, uint32(preferenceSettingsAdvisoryClass), uint32(id)).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("reset bypassed account lock: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("reset did not wait on account lock")
		}
		runtime.Gosched()
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
