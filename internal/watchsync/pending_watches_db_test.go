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
	var rebindPID int
	if err := rebind.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&rebindPID); err != nil {
		t.Fatal(err)
	}
	saved := make(chan error, 1)
	go func() {
		saved <- repo.SavePendingWatches(ctx, conn.ID, conn.ProviderAccountID, []RemoteWatch{stale})
	}()
	// The save must wait for the rebind. The rebind holds only this test's
	// connection row, so a session it blocks is the save.
	blocked := false
	for deadline := time.Now().Add(10 * time.Second); !blocked && time.Now().Before(deadline); {
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE $1::integer = ANY(pg_blocking_pids(pid)))`, rebindPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-saved:
			t.Fatalf("the save finished without waiting for the rebind (err=%v)", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !blocked {
		t.Fatal("the save never waited for the rebind")
	}
	if err := rebind.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatalf("SavePendingWatches: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM watch_provider_pending_watches WHERE connection_id=$1`, conn.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("kept %d watches of the previous account, want none", rows)
	}
}

// A connection keeps at most maxPendingWatches; past it the oldest go.
func TestSavePendingWatchesKeepsTheNewestDB(t *testing.T) {
	ctx := t.Context()
	repo, conn, _ := newSyncStateTestConnection(t)
	oldest := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	watches := make([]RemoteWatch, maxPendingWatches+1)
	for i := range watches {
		at := oldest.Add(time.Duration(i) * time.Minute)
		watches[i] = RemoteWatch{
			Provider: conn.Provider, ProviderItemKey: "tmdb:" + strconv.Itoa(i), Kind: historyimport.KindMovie,
			TMDBID: strconv.Itoa(i), PlayCount: 1, LastWatchedAt: &at,
		}
	}
	if err := repo.SavePendingWatches(ctx, conn.ID, conn.ProviderAccountID, watches); err != nil {
		t.Fatal(err)
	}
	var kept int
	var earliest time.Time
	if err := repo.pool.QueryRow(ctx, `SELECT count(*), min(last_watched_at) FROM watch_provider_pending_watches WHERE connection_id=$1`,
		conn.ID).Scan(&kept, &earliest); err != nil {
		t.Fatal(err)
	}
	if kept != maxPendingWatches || !earliest.After(oldest) {
		t.Fatalf("kept %d watches from %v, want %d without the oldest", kept, earliest, maxPendingWatches)
	}
}

// At the cap, a sync that imports a kept watch and keeps a new one drops no
// older kept watch: the imported one leaves before the new one is saved.
func TestImportWatchedAtTheCapKeepsOlderWatchesDB(t *testing.T) {
	ctx := t.Context()
	repo, conn, _ := newSyncStateTestConnection(t)
	pool := repo.pool
	oldest := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	base := 700_000_000 + rand.IntN(99_000_000)
	watch := func(i int, at time.Time) RemoteWatch {
		id := strconv.Itoa(base + i)
		return RemoteWatch{
			Provider: conn.Provider, ProviderItemKey: "tmdb:" + id, Kind: historyimport.KindMovie,
			TMDBID: id, PlayCount: 1, LastWatchedAt: &at,
		}
	}
	kept := make([]RemoteWatch, maxPendingWatches)
	for i := range kept {
		kept[i] = watch(i, oldest.Add(time.Duration(i)*time.Minute))
	}
	if err := repo.SavePendingWatches(ctx, conn.ID, conn.ProviderAccountID, kept); err != nil {
		t.Fatal(err)
	}
	// The newest kept watch's title arrives, and the provider returns a new
	// watch of a title the library lacks.
	arrived := kept[len(kept)-1]
	movie := "movie-tmdb-" + arrived.TMDBID
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,genres,tmdb_id,status) VALUES ($1,'movie','Arrived','{}',$2,'matched')`,
		movie, arrived.TMDBID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, movie) })
	service := NewService(repo, NewRegistry()).
		WithMatcher(historyimport.NewMatcherOn(pool)).
		WithWatchState(&recordingWatchState{})
	if _, err := service.ImportWatched(ctx, conn, ServerConfig{}, watchedImporterStub{
		historyStubBase: historyStubBase{key: conn.Provider, source: userstore.WatchHistorySourceTrakt},
		rows:            []RemoteWatch{watch(maxPendingWatches, oldest.Add(time.Duration(maxPendingWatches)*time.Minute))},
	}); err != nil {
		t.Fatalf("ImportWatched: %v", err)
	}
	var count int
	var earliest time.Time
	if err := pool.QueryRow(ctx, `SELECT count(*), min(last_watched_at) FROM watch_provider_pending_watches WHERE connection_id=$1`,
		conn.ID).Scan(&count, &earliest); err != nil {
		t.Fatal(err)
	}
	if count != maxPendingWatches || !earliest.Equal(oldest) {
		t.Fatalf("kept %d watches from %v, want %d with the oldest from %v", count, earliest, maxPendingWatches, oldest)
	}
}
