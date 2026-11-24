package userdb

import (
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func newConformanceStore(t *testing.T) userstore.UserStore {
	t.Helper()
	db, err := NewUserDB(filepath.Join(t.TempDir(), "user.db"), 1)
	if err != nil {
		t.Fatalf("open user database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewSQLiteUserStore(db.DB)
}

// TestSQLiteUnmarkKeepsHistory runs the mark-unwatched conformance test
// against the SQLite backend; the Postgres backend runs it in
// internal/userstore/pgstore.
func TestSQLiteUnmarkKeepsHistory(t *testing.T) {
	storetest.RunUnmarkKeepsHistory(t, newConformanceStore)
	storetest.RunPlayFinishedAfterUnmark(t, newConformanceStore)
}

func TestSQLiteDatedMarkWatchedBatchAtomic(t *testing.T) {
	storetest.RunDatedMarkWatchedBatch(t, newConformanceStore(t))
}

func TestSQLiteAtomicJellycompatProgressHistoryRollback(t *testing.T) {
	store, ok := newConformanceStore(t).(*SQLiteUserStore)
	if !ok {
		t.Fatal("SQLite fixture unavailable")
	}
	for _, operation := range []string{"INSERT", "UPDATE"} {
		if _, err := store.db.Exec("CREATE TRIGGER fail_progress_" + operation + " BEFORE " + operation + " ON watch_progress WHEN NEW.position_seconds = 321 BEGIN SELECT RAISE(ABORT, 'forced progress failure'); END"); err != nil {
			t.Fatal(err)
		}
	}
	storetest.RunAtomicJellycompatProgress(t, store)
}

// TestSQLiteProfilePINRevision runs the profile PIN revision conformance test
// against the per-user SQLite backend; the Postgres backend runs the same suite
// in internal/userstore/pgstore.
func TestSQLiteProfilePINRevision(t *testing.T) {
	storetest.RunProfilePINRevision(t, newConformanceStore)
}

func TestSQLiteProfilePINRevisionTransactions(t *testing.T) {
	storetest.RunProfilePINRevisionTransactions(t, newConformanceStore)
}

func TestSQLiteDeleteProfileRemovesProfileState(t *testing.T) {
	storetest.RunDeleteProfileRemovesProfileState(t, newConformanceStore, false)
}

func TestSQLiteCollectionsHolding(t *testing.T) {
	storetest.RunCollectionsHolding(t, newConformanceStore(t))
}
