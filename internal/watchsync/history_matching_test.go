package watchsync

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestServiceExportWatchedConsumesEachRemotePlayOnce(t *testing.T) {
	for _, remoteCount := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d remote plays", remoteCount), func(t *testing.T) {
			db, err := sql.Open("sqlite3", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := userdb.InitSchema(db); err != nil {
				t.Fatal(err)
			}
			minute := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
			for i := range 2 {
				if err := userdb.AddHistory(db, userstore.WatchHistoryEntry{
					ID: fmt.Sprintf("history-%d", i), ProfileID: "profile-1", MediaItemID: testMovieMediaID,
					WatchedAt: minute.Add(time.Duration(10+i*20) * time.Second).Format(time.RFC3339),
					Completed: true, Source: userstore.WatchHistorySourcePlayback,
					Identity: userstore.WatchIdentity{StableType: "movie", ProviderIDs: map[string]string{"tmdb": "603"}},
				}); err != nil {
					t.Fatal(err)
				}
			}
			var exported []LocalPlay
			provider := watchedExporterStub{
				exported: &exported, precision: time.Minute,
				exportResult: ExportResult{Sent: []string{"history-0", "history-1"}},
			}
			for range remoteCount {
				provider.remote = append(provider.remote, RemotePlay{ProviderItemKey: "tmdb:603", WatchedAt: minute})
			}
			repo := newServiceFakeRepo()
			service := NewService(repo, NewRegistry()).WithUserStoreProvider(staticStoreProvider{store: userdb.NewSQLiteUserStore(db)})
			conn := seedTestConnection(t, repo, Connection{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1"})
			result, err := service.ExportWatched(t.Context(), conn, ServerConfig{}, provider)
			if err != nil {
				t.Fatal(err)
			}
			if result.RemotePresent != remoteCount || result.Sent != 2-remoteCount || len(exported) != 2-remoteCount {
				t.Fatalf("remote plays=%d: result=%+v exported=%d", remoteCount, result, len(exported))
			}
			// A repeat of the same snapshot must not send a play already recorded as sent.
			if _, err := service.ExportWatched(t.Context(), conn, ServerConfig{}, provider); err != nil {
				t.Fatal(err)
			}
			if len(exported) != 2-remoteCount {
				t.Fatalf("repeated export sent duplicate plays: %d", len(exported))
			}
		})
	}
}
