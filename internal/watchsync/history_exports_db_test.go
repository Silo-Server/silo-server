package watchsync

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// Paging after a cursor visits each pending export once, in (watched_at, id)
// order, including exports that share a watch time.
func TestListPendingHistoryExportsPagesByCursorDB(t *testing.T) {
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
	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "watch-exports-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,'exports-p','Exports')", userID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("watch-exports-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)
	conn, err := repo.UpsertConnection(ctx, Connection{
		Provider: "exports", UserID: userID, ProfileID: "exports-p", AccessToken: "token",
		ExportWatchedEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seven exports over three watch times, so pages split ties. One is
	// already sent and must never be listed.
	base := time.Date(2020, time.November, 4, 14, 56, 0, 0, time.UTC)
	exports := make([]HistoryExport, 0, 8)
	for i := range 7 {
		exports = append(exports, HistoryExport{
			ConnectionID: conn.ID, HistoryID: fmt.Sprintf("h-%d", i), MediaItemID: "movie:x",
			WatchedAt: base.Add(time.Duration(i/3) * time.Minute), ProviderItemKey: "tmdb:1",
			Status: historyExportStatusPending,
		})
	}
	exports = append(exports, HistoryExport{
		ConnectionID: conn.ID, HistoryID: "h-sent", MediaItemID: "movie:x",
		WatchedAt: base, ProviderItemKey: "tmdb:1", Status: historyExportStatusSent,
	})
	if err := repo.UpsertHistoryExports(ctx, exports); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	var cursor HistoryExportCursor
	var previous HistoryExport
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("paging did not end")
		}
		pending, err := repo.ListPendingHistoryExports(ctx, conn.ID, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 {
			break
		}
		for _, export := range pending {
			if seen[export.HistoryID] {
				t.Fatalf("export %s listed twice", export.HistoryID)
			}
			seen[export.HistoryID] = true
			if previous.ID != "" && (export.WatchedAt.Before(previous.WatchedAt) ||
				(export.WatchedAt.Equal(previous.WatchedAt) && export.ID <= previous.ID)) {
				t.Fatalf("export %s listed out of order after %s", export.HistoryID, previous.HistoryID)
			}
			previous = export
		}
		last := pending[len(pending)-1]
		cursor = HistoryExportCursor{WatchedAt: last.WatchedAt, ID: last.ID}
	}
	if len(seen) != 7 || seen["h-sent"] {
		t.Fatalf("listed %v, want the 7 pending exports", seen)
	}
}
