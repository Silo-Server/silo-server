package pgstore

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestImportedHistoryConcurrentDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	name := fmt.Sprintf("import-replay-%d", time.Now().UnixNano())
	var userID int
	if err := pool.QueryRow(t.Context(), "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", name).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(t.Context(), "DELETE FROM users WHERE id=$1", userID) }()
	storetest.ImportedHistoryConcurrent(t, newStore(pool, userID))
	var otherUserID int
	if err := pool.QueryRow(t.Context(), "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", name+"-other").Scan(&otherUserID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(t.Context(), "DELETE FROM users WHERE id=$1", otherUserID) }()
	otherStore := newStore(pool, otherUserID)
	if err := otherStore.CreateProfile(t.Context(), userstore.Profile{ID: "import-p", Name: "Other account"}); err != nil {
		t.Fatal(err)
	}
	created, err := otherStore.AddHistoryIfMissing(t.Context(), userstore.WatchHistoryEntry{ProfileID: "import-p", MediaItemID: "import-movie", WatchedAt: "2026-01-01T00:00:00Z", Completed: true})
	if err != nil || !created {
		t.Fatalf("other account=%v %v", created, err)
	}
}
