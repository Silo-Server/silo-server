package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// noRollupStore hides the Postgres store's SQL rollup, so the seam folds the
// series' episodes with their progress instead.
type noRollupStore struct {
	userstore.UserStore
}

// countsProgressFailsStore folds like noRollupStore, but every progress read fails.
type countsProgressFailsStore struct {
	noRollupStore
}

func (countsProgressFailsStore) ListProgressByMediaItems(context.Context, string, []string) (map[string]userstore.WatchProgress, error) {
	return nil, errors.New("progress store unavailable")
}

// countsHistoryFailsStore folds like noRollupStore, but every completed-history
// read fails.
type countsHistoryFailsStore struct {
	noRollupStore
}

func (countsHistoryFailsStore) ListCompletedHistoryItems(context.Context, userstore.CompletedHistoryItemQuery) ([]userstore.CompletedHistoryItem, error) {
	return nil, errors.New("history store unavailable")
}

// SeriesWatchCounts answers only the series the viewer may see that have
// available episodes, with the same counts whether the store rolls them up in
// SQL or the seam folds the episodes.
func TestSeriesWatchCountsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := seedWatchCounts(t, pool)

	pgStore, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	authCtx := middleware.SetClaims(ctx, &auth.Claims{UserID: f.userID, Role: "user"})
	viewer := ItemViewer{ProfileID: f.profileID, Access: catalog.AccessFilter{UserID: f.userID, ProfileID: f.profileID, AllowedLibraryIDs: []int{f.allowedFolder}}}
	want := map[string]*catalog.SeasonUserData{
		f.started:  {WatchedCount: 1, UnplayedCount: 3, InProgressCount: 1},
		f.finished: {WatchedCount: 2, Played: true},
	}
	for _, tc := range []struct {
		name    string
		store   userstore.UserStore
		wantErr bool
	}{
		{"rollup", pgStore, false},
		{"fold", noRollupStore{pgStore}, false},
		// A progress or history read that fails is an error, never counts left
		// out or episodes watched only in history counted as unwatched.
		{"fold progress fails", countsProgressFailsStore{noRollupStore{pgStore}}, true},
		{"fold history fails", countsHistoryFailsStore{noRollupStore{pgStore}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &CatalogResourceHandler{items: &ItemsHandler{
				itemRepo:      catalog.NewItemRepository(pool),
				episodeRepo:   catalog.NewEpisodeRepository(pool),
				storeProvider: testUserStoreProvider{store: tc.store},
			}}
			got, err := h.SeriesWatchCounts(authCtx, viewer, []string{f.started, f.finished, f.hidden, f.movie, f.podcast, f.unknown})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("counts = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("counts = %+v, want %+v", got, want)
			}
		})
	}
}

// watchCountsFixture is what seedWatchCounts builds: series a viewer of
// allowedFolder may see with known watch state, and IDs that must not count.
type watchCountsFixture struct {
	started, finished, hidden, movie, podcast, unknown string
	allowedFolder, userID                              int
	profileID                                          string
}

func seedWatchCounts(t *testing.T, pool *pgxpool.Pool) watchCountsFixture {
	t.Helper()
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("watch-counts-%d", time.Now().UnixNano())
	f := watchCountsFixture{
		started: prefix + "-started", finished: prefix + "-finished", hidden: prefix + "-hidden",
		movie: prefix + "-movie", podcast: prefix + "-podcast", unknown: prefix + "-unknown",
	}
	var hiddenFolder int
	for _, folder := range []struct {
		name string
		id   *int
	}{{prefix + "-allowed", &f.allowedFolder}, {prefix + "-hidden", &hiddenFolder}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, folder.name).Scan(folder.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, prefix).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, f.userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM episodes WHERE series_id = ANY($1)`, []string{f.started, f.finished, f.hidden, f.podcast})
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{f.started, f.finished, f.hidden, f.movie, f.podcast})
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{f.allowedFolder, hiddenFolder})
	})

	exec(`INSERT INTO media_items (content_id,type,title) VALUES ($1,'series','Started'), ($2,'series','Finished'), ($3,'series','Hidden'), ($4,'movie','Movie'), ($5,'podcast','Podcast')`,
		f.started, f.finished, f.hidden, f.movie, f.podcast)
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$6), ($2,$6), ($4,$6), ($5,$6), ($3,$7)`,
		f.started, f.finished, f.hidden, f.movie, f.podcast, f.allowedFolder, hiddenFolder)
	// Started has four available episodes and one whose file is gone; Finished
	// has two; Hidden has one in a library the viewer may not see. A podcast
	// show is not a series, though its episodes share the table.
	exec(`INSERT INTO episodes (content_id,series_id,season_number,episode_number,title) VALUES
		($1||'-e1',$1,1,1,'One'), ($1||'-e2',$1,1,2,'Two'), ($1||'-e3',$1,1,3,'Three'), ($1||'-e4',$1,1,4,'Four'), ($1||'-gone',$1,1,5,'Gone'),
		($2||'-e1',$2,1,1,'One'), ($2||'-e2',$2,1,2,'Two'),
		($3||'-e1',$3,1,1,'One'), ($4||'-e1',$4,1,1,'One')`, f.started, f.finished, f.hidden, f.podcast)
	exec(`INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES
		($1||'-e1',$5), ($1||'-e2',$5), ($1||'-e3',$5), ($1||'-e4',$5), ($2||'-e1',$5), ($2||'-e2',$5), ($4||'-e1',$5), ($3||'-e1',$6)`,
		f.started, f.finished, f.hidden, f.podcast, f.allowedFolder, hiddenFolder)

	f.profileID = fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	exec(`INSERT INTO user_profiles (id,user_id,name) VALUES ($1,$2,'Watch counts fixture')`, f.profileID, f.userID)
	// Started: e1 finished, e2 half watched, and the gone episode finished
	// (it no longer counts). Finished: e1 finished, e2 finished only in
	// history. Hidden: finished, but the viewer may not see the series.
	exec(`INSERT INTO user_watch_progress (user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed) VALUES
		($1,$2,$3||'-e1',1800,1800,true), ($1,$2,$3||'-e2',600,1800,false), ($1,$2,$3||'-gone',1800,1800,true),
		($1,$2,$4||'-e1',1800,1800,true), ($1,$2,$5||'-e1',1800,1800,true)`, f.userID, f.profileID, f.started, f.finished, f.hidden)
	exec(`INSERT INTO user_watch_history (id,user_id,profile_id,media_item_id,completed) VALUES ($1,$2,$3,$4||'-e2',true)`,
		prefix+"-history", f.userID, f.profileID, f.finished)
	return f
}
