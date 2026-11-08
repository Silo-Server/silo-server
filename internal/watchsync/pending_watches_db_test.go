package watchsync

import (
	"context"
	"math/rand/v2"
	"reflect"
	"slices"
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

// A whole-series watched marker marks the episodes in a library that had aired
// by its time, leaving out specials, and stays kept: an episode added to a
// library later is marked on the next sync, though the provider never returns
// the marker again.
func TestImportWatchedSeriesMarkerDB(t *testing.T) {
	ctx := t.Context()
	repo, conn, _ := newSyncStateTestConnection(t)
	pool := repo.pool
	finishedAt := time.Date(2021, 9, 19, 5, 17, 0, 0, time.UTC)
	tvdbID := strconv.Itoa(900_000_000 + rand.IntN(99_999_999))
	series := "series-tvdb-" + tvdbID
	special, first, second, unaired, fourth := series+"-s0e1", series+"-s1e1", series+"-s1e2", series+"-s1e3", series+"-s1e4"
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('tv',$1) RETURNING id`, "series-marker-"+tvdbID).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder) })
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,year,genres,tvdb_id,status)
		VALUES ($1,'series','Brooklyn Nine-Nine',2013,'{}',$2,'matched')`, series, tvdbID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, series) })
	if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id,series_id,season_number,episode_number,title,air_date) VALUES
		($2,$1,0,1,'Special','2015-01-01'),($3,$1,1,1,'Pilot','2013-09-17'),($4,$1,1,2,'Two','2013-09-24'),($5,$1,1,3,'Later','2030-01-01'),
		($6,$1,1,4,'Four','2013-10-01')`,
		series, special, first, second, unaired, fourth); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM episodes WHERE content_id IN ($1,$2,$3,$4,$5)`, special, first, second, unaired, fourth)
	})
	inLibrary := func(episodes ...string) {
		t.Helper()
		for _, episode := range episodes {
			if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES ($1,$2)`, episode, folder); err != nil {
				t.Fatal(err)
			}
		}
	}
	inLibrary(special, first, unaired)

	watchState := &recordingWatchState{}
	service := NewService(repo, NewRegistry()).
		WithMatcher(historyimport.NewMatcherOn(pool)).
		WithWatchState(watchState)
	sync := func(rows ...RemoteWatch) ImportWatchedResult {
		t.Helper()
		result, err := service.ImportWatched(ctx, conn, ServerConfig{}, watchedImporterStub{
			historyStubBase: historyStubBase{key: conn.Provider, source: userstore.WatchHistorySourceTrakt},
			rows:            rows,
		})
		if err != nil {
			t.Fatalf("ImportWatched: %v", err)
		}
		return result
	}
	marker := RemoteWatch{
		Provider: conn.Provider, ProviderItemKey: "tvdb:" + tvdbID, Kind: historyimport.KindSeries,
		Title: "Brooklyn Nine-Nine", Year: 2013, TVDBID: tvdbID, PlayCount: 1, LastWatchedAt: &finishedAt,
	}

	if got := sync(marker); got.Found != 1 || got.Imported != 1 {
		t.Fatalf("first sync = %+v, want the one aired regular episode in a library", got)
	}
	if !reflect.DeepEqual(watchState.targetIDs, []string{first}) {
		t.Fatalf("marked %v, want only %s", watchState.targetIDs, first)
	}
	inLibrary(second)
	// The grown marker is applied again; the store keeps an episode already
	// marked as it was.
	before := len(watchState.targetIDs)
	if sync(); !slices.Contains(watchState.targetIDs[before:], second) {
		t.Fatalf("sync after an episode arrives marked %v, want %s", watchState.targetIDs[before:], second)
	}
	marked := len(watchState.targetIDs)
	if got := sync(); got.Imported != 0 || len(watchState.targetIDs) != marked {
		t.Fatalf("sync with nothing new = %+v, marked %v", got, watchState.targetIDs)
	}
	// Between two syncs an episode leaves the library and another arrives:
	// the marker covers as many episodes as before, and still marks the new
	// one.
	if _, err := pool.Exec(ctx, `DELETE FROM episode_libraries WHERE episode_id=$1`, first); err != nil {
		t.Fatal(err)
	}
	inLibrary(fourth)
	before = len(watchState.targetIDs)
	if sync(); !slices.Contains(watchState.targetIDs[before:], fourth) {
		t.Fatalf("sync after an episode is swapped marked %v, want %s", watchState.targetIDs[before:], fourth)
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
		saved <- repo.SavePendingWatches(ctx, conn.ID, conn.ProviderAccountID, []PendingWatch{{Watch: stale}})
	}()
	// Wait until the save is blocked by the rebind, or has finished without
	// waiting for it, which the check below then catches.
	var done error
	finished, blocked := false, false
	for deadline := time.Now().Add(10 * time.Second); !finished && !blocked && time.Now().Before(deadline); {
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE $1::integer = ANY(pg_blocking_pids(pid)))`, rebindPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case done = <-saved:
			finished = true
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !finished && !blocked {
		t.Fatal("the save neither finished nor waited for the rebind")
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

// A whole-series marker marks only episodes in the libraries the profile may
// see, on its first sync and when a kept marker is applied again.
func TestImportWatchedSeriesMarkerStaysInTheProfilesLibrariesDB(t *testing.T) {
	ctx := t.Context()
	repo, conn, _ := newSyncStateTestConnection(t)
	pool := repo.pool
	finishedAt := time.Date(2021, 9, 19, 5, 17, 0, 0, time.UTC)
	tvdbID := strconv.Itoa(900_000_000 + rand.IntN(99_999_999))
	series := "series-tvdb-" + tvdbID
	visible, hidden, later := series+"-s1e1", series+"-s1e2", series+"-s1e3"
	folder := func(name string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('tv',$1) RETURNING id`, name+"-"+tvdbID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, id) })
		return id
	}
	allowed, other := folder("allowed"), folder("other")
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,year,genres,tvdb_id,status)
		VALUES ($1,'series','Brooklyn Nine-Nine',2013,'{}',$2,'matched')`, series, tvdbID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, series) })
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2),($1,$3)`, series, allowed, other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id,series_id,season_number,episode_number,title,air_date) VALUES
		($2,$1,1,1,'Pilot','2013-09-17'),($3,$1,1,2,'Two','2013-09-24'),($4,$1,1,3,'Three','2013-10-01')`,
		series, visible, hidden, later); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM episodes WHERE content_id IN ($1,$2,$3)`, visible, hidden, later)
	})
	inLibrary := func(episode string, folder int) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES ($1,$2)`, episode, folder); err != nil {
			t.Fatal(err)
		}
	}
	inLibrary(visible, allowed)
	inLibrary(hidden, other)

	watchState := &recordingWatchState{}
	service := NewService(repo, NewRegistry()).
		WithMatcher(historyimport.NewMatcherOn(pool)).
		WithWatchState(watchState).
		WithLibraryScope(func(context.Context, int, string) ([]int, error) { return []int{allowed}, nil })
	sync := func(rows ...RemoteWatch) {
		t.Helper()
		if _, err := service.ImportWatched(ctx, conn, ServerConfig{}, watchedImporterStub{
			historyStubBase: historyStubBase{key: conn.Provider, source: userstore.WatchHistorySourceTrakt},
			rows:            rows,
		}); err != nil {
			t.Fatalf("ImportWatched: %v", err)
		}
	}
	sync(RemoteWatch{
		Provider: conn.Provider, ProviderItemKey: "tvdb:" + tvdbID, Kind: historyimport.KindSeries,
		Title: "Brooklyn Nine-Nine", Year: 2013, TVDBID: tvdbID, PlayCount: 1, LastWatchedAt: &finishedAt,
	})
	if !reflect.DeepEqual(watchState.targetIDs, []string{visible}) {
		t.Fatalf("marked %v, want only %s from the profile's library", watchState.targetIDs, visible)
	}
	// Episodes reach both libraries; the kept marker marks only the allowed one.
	inLibrary(later, other)
	sync()
	inLibrary(later, allowed)
	sync()
	if slices.Contains(watchState.targetIDs, hidden) || !slices.Contains(watchState.targetIDs, later) {
		t.Fatalf("marked %v, want %s and never %s", watchState.targetIDs, later, hidden)
	}
}
