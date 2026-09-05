package userdb

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestSQLiteHistoryWitness(t *testing.T) { storetest.RunHistoryWitness(t, newConformanceStore) }

func TestHistoryWitnessIndexUpgradeFromV20(t *testing.T) {
	store := newConformanceStore(t).(*SQLiteUserStore)
	if _, err := store.db.Exec(`DROP INDEX idx_watch_history_item_witness; PRAGMA user_version = 20;`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(store.db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_watch_history_item_witness'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("history witness index was not installed on upgrade")
	}
	version, err := userVersion(store.db)
	if err != nil || version != 21 {
		t.Fatalf("version=%d, error=%v", version, err)
	}
}
