package userdb

import (
	"database/sql"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func newConformanceStore(t *testing.T) userstore.UserStore {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return NewSQLiteUserStore(db)
}

// TestSQLiteProgressSince runs the offline-sync progress-reconciliation
// conformance test (invariant 1) against the real SQLite backend, exercising the
// synced_seq stamping triggers and event_at LWW comparison.
func TestSQLiteProgressSince(t *testing.T) {
	storetest.RunProgressSince(t, newConformanceStore)
}
