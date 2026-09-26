package userdb

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestDeviceSettingsTiesAndRollback(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	store := NewSQLiteUserStore(db)
	for _, p := range []string{"a", "b"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: p, Name: p}); err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{"one", "two"} {
			if err := store.RegisterDevice(ctx, userstore.DeviceEntry{ProfileID: p, DeviceID: d}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Exec(`UPDATE user_devices SET last_seen_at='2026-01-02T03:04:05.123456Z'`); err != nil {
		t.Fatal(err)
	}
	opts := userstore.DevicePageOptions{Limit: 1}
	for _, want := range []string{"a/one", "a/two", "b/one", "b/two"} {
		page, err := store.ListDeviceSettingsPage(ctx, opts)
		if err != nil || len(page) != 1 || page[0].ProfileID+"/"+page[0].DeviceID != want {
			t.Fatalf("tie page: %+v %v want %s", page, err, want)
		}
		v := page[0]
		opts.After = &userstore.DevicePosition{LastSeenAt: v.LastSeenAt, ProfileID: v.ProfileID, DeviceID: v.DeviceID}
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_device_delete BEFORE DELETE ON user_devices BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveDeviceSettings(ctx, "a", "one", true); err == nil {
		t.Fatal("expected failure")
	}
	exists, err := store.DeviceExists(ctx, "a", "one")
	if err != nil || !exists {
		t.Fatalf("rollback: %v %v", exists, err)
	}
}

func TestDeviceSettingsConcurrentWAL(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "devices.db")+"?_journal_mode=WAL&_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(8)
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	store := NewSQLiteUserStore(db)
	ctx := t.Context()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterDevice(ctx, userstore.DeviceEntry{ProfileID: "p", DeviceID: "d"}); err != nil {
		t.Fatal(err)
	}
	// Production uses WAL and several connections. Mix clear commands with
	// canonical read/merge/write transactions; deferred read transactions fail to
	// upgrade their snapshots under this contention even with a busy timeout.
	start := make(chan struct{})
	results := make(chan error, 8)
	for worker := range 8 {
		go func() {
			<-start
			for range 20 {
				var err error
				if worker%2 == 0 {
					_, err = store.RemoveDeviceSettings(ctx, "p", "d", false)
				} else {
					err = store.WithSettingMutationTransaction(ctx, "", func(w userstore.SettingMutationWriter) error {
						_, err := w.UpsertSettingValue(ctx, userstore.SettingIdentity{Key: "theme", Scope: settingscontract.ScopeProfileDevice, ProfileID: "p", DeviceID: "d"}, json.RawMessage(`"dark"`))
						return err
					})
				}
				if err != nil {
					results <- err
					return
				}
			}
			results <- nil
		}()
	}
	close(start)
	for range 8 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}
