package watchsync

import (
	"context"
	"math/rand/v2"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A provider watch of a title the library lacks is kept, and imported on a
// later sync once the title is added, even though the provider's incremental
// read never returns it again. A watch kept for one provider account never
// imports after the connection moves to another account.
func TestImportWatchedRetriesPendingWatchesDB(t *testing.T) {
	ctx := t.Context()
	repo, conn, _ := newSyncStateTestConnection(t)
	pool := repo.pool
	watchedAt := time.Date(2026, 9, 6, 3, 21, 0, 0, time.UTC)
	tmdbID := strconv.Itoa(900_000_000 + rand.IntN(99_999_999))
	otherTMDBID := strconv.Itoa(800_000_000 + rand.IntN(99_999_999))
	movie, otherMovie := "movie-tmdb-"+tmdbID, "movie-tmdb-"+otherTMDBID
	watch := func(id string) RemoteWatch {
		return RemoteWatch{
			Provider: conn.Provider, ProviderItemKey: "tmdb:" + id, Kind: historyimport.KindMovie,
			Title: "The Odyssey", Year: 2026, TMDBID: id, PlayCount: 1, LastWatchedAt: &watchedAt,
		}
	}
	watchState := &recordingWatchState{}
	service := NewService(repo, NewRegistry()).
		WithMatcher(historyimport.NewMatcherOn(pool)).
		WithWatchState(watchState)
	sync := func(c Connection, rows ...RemoteWatch) ImportWatchedResult {
		t.Helper()
		result, err := service.ImportWatched(ctx, c, ServerConfig{}, watchedImporterStub{
			historyStubBase: historyStubBase{key: conn.Provider, source: userstore.WatchHistorySourceTrakt},
			rows:            rows,
		})
		if err != nil {
			t.Fatalf("ImportWatched: %v", err)
		}
		return result
	}

	// Neither title is in the library yet.
	if got := sync(conn, watch(tmdbID), watch(otherTMDBID)); got.Found != 2 || got.Unmatched != 2 || got.Imported != 0 {
		t.Fatalf("first sync = %+v, want two unmatched", got)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,year,genres,tmdb_id,status)
		VALUES ($1,'movie','The Odyssey',2026,'{}',$2,'matched'),($3,'movie','Other',2026,'{}',$4,'matched')`,
		movie, tmdbID, otherMovie, otherTMDBID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id IN ($1,$2)`, movie, otherMovie)
	})

	// The provider's read now returns nothing new; the kept watch imports.
	if got := sync(conn); got.Found != 0 || got.Unmatched != 0 || got.Imported != 2 {
		t.Fatalf("sync after the titles arrive = %+v, want both kept watches imported", got)
	}
	if !reflect.DeepEqual(watchState.targetIDs, []string{otherMovie, movie}) &&
		!reflect.DeepEqual(watchState.targetIDs, []string{movie, otherMovie}) {
		t.Fatalf("recorded target ids = %v", watchState.targetIDs)
	}
	for _, at := range watchState.watchedAt {
		if at == nil || !at.Equal(watchedAt) {
			t.Fatalf("recorded watched_at = %v, want the provider's %v", at, watchedAt)
		}
	}
	// Imported watches leave the store.
	if got := sync(conn); got.Imported != 0 {
		t.Fatalf("third sync = %+v, want nothing left to import", got)
	}

	// The connection is rebound to another provider account: a watch kept for
	// the previous one never imports.
	setLibrary := func(present bool) {
		t.Helper()
		query := `DELETE FROM media_items WHERE content_id=$1`
		args := []any{otherMovie}
		if present {
			query = `INSERT INTO media_items (content_id,type,title,year,genres,tmdb_id,status)
				VALUES ($1,'movie','Other',2026,'{}',$2,'matched')`
			args = append(args, otherTMDBID)
		}
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	setLibrary(false)
	sync(conn, watch(otherTMDBID))
	setLibrary(true)
	rebound := conn
	rebound.ProviderAccountID = "another-account"
	if _, err := pool.Exec(ctx, `UPDATE watch_provider_connections SET provider_account_id=$2 WHERE id=$1`, conn.ID, rebound.ProviderAccountID); err != nil {
		t.Fatal(err)
	}
	if got := sync(rebound); got.Imported != 0 {
		t.Fatalf("sync after rebinding = %+v, want the previous account's watch dropped", got)
	}
	// A sync of the previous account still running neither imports its old
	// watch nor replaces or drops the new account's.
	if got := sync(conn); got.Imported != 0 {
		t.Fatalf("stale sync of the previous account = %+v, want nothing imported", got)
	}
	setLibrary(false)
	sync(rebound, watch(otherTMDBID))
	sync(conn, watch(otherTMDBID))
	setLibrary(true)
	if got := sync(rebound); got.Imported != 1 {
		t.Fatalf("sync of the new account = %+v, want its kept watch imported", got)
	}
}

// A save from the connection's previous provider account that runs while the
// connection is being rebound waits for the rebind, then writes nothing.
func TestSavePendingWatchesWaitsForARebindDB(t *testing.T) {
	ctx := t.Context()
	repo, conn, _ := newSyncStateTestConnection(t)
	pool := repo.pool
	watchedAt := time.Date(2026, 9, 6, 3, 21, 0, 0, time.UTC)
	stale := RemoteWatch{
		Provider: conn.Provider, ProviderItemKey: "tmdb:1", Kind: historyimport.KindMovie,
		TMDBID: "1", PlayCount: 1, LastWatchedAt: &watchedAt,
	}
	rebind, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rebind.Rollback(context.Background()) }()
	if _, err := rebind.Exec(ctx, `UPDATE watch_provider_connections SET provider_account_id='another-account' WHERE id=$1`, conn.ID); err != nil {
		t.Fatal(err)
	}
	saved := make(chan error, 1)
	go func() { saved <- repo.SavePendingWatches(ctx, conn.ID, conn.ProviderAccountID, []RemoteWatch{stale}) }()
	// Wait until the save either finished or is waiting on the rebind's lock.
	var done error
	finished := false
	for deadline := time.Now().Add(10 * time.Second); !finished && time.Now().Before(deadline); {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%FOR NO KEY UPDATE%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case done = <-saved:
			finished = true
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := rebind.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !finished {
		done = <-saved
	}
	if done != nil {
		t.Fatalf("SavePendingWatches: %v", done)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM watch_provider_pending_watches WHERE connection_id=$1`, conn.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("kept %d watches of the previous account, want none", rows)
	}
}
