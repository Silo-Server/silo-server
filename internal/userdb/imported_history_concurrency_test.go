package userdb

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestImportedHistoryConcurrent(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "history.db")+"?_busy_timeout=10000&_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	db.SetMaxOpenConns(8)
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	storetest.ImportedHistoryConcurrent(t, NewSQLiteUserStore(db))
}
